package source

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// envelope is Prometheus's standard /api/v1 response wrapper.
type envelope struct {
	Status    string          `json:"status"`
	Data      json.RawMessage `json:"data"`
	ErrorType string          `json:"errorType"`
	Error     string          `json:"error"`
}

// RemoteSource pulls operational data from a running Prometheus's public
// HTTP API. It never touches the on-disk TSDB directly, so it works against
// any remote instance and carries none of the API-stability risk that
// LocalSource (direct tsdb package use) does.
type RemoteSource struct {
	baseURL string
	client  *http.Client
}

// NewRemoteSource builds a RemoteSource against the given Prometheus base
// URL, e.g. "http://localhost:9090".
func NewRemoteSource(baseURL string) *RemoteSource {
	return &RemoteSource{
		baseURL: baseURL,
		client:  &http.Client{Timeout: 30 * time.Second},
	}
}

func (s *RemoteSource) get(ctx context.Context, path string, query url.Values, out any) error {
	u := s.baseURL + "/api/v1" + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("requesting %s: %w", path, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("%s returned HTTP %d: %s", path, resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var env envelope
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		return fmt.Errorf("decoding %s response: %w", path, err)
	}
	if env.Status != "success" {
		return fmt.Errorf("%s returned %s: %s (%s)", path, env.Status, env.Error, env.ErrorType)
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(env.Data, out)
}

type tsdbStat struct {
	Name  string `json:"name"`
	Value uint64 `json:"value"`
}

func toStats(in []tsdbStat) []Stat {
	out := make([]Stat, len(in))
	for i, s := range in {
		out[i] = Stat{Name: s.Name, Value: s.Value}
	}
	return out
}

func (s *RemoteSource) tsdbStatus(ctx context.Context, limit int) (HeadStats, Cardinality, error) {
	var resp struct {
		HeadStats struct {
			NumSeries     uint64 `json:"numSeries"`
			NumLabelPairs int    `json:"numLabelPairs"`
			ChunkCount    int64  `json:"chunkCount"`
			MinTime       int64  `json:"minTime"`
			MaxTime       int64  `json:"maxTime"`
		} `json:"headStats"`
		SeriesCountByMetricName     []tsdbStat `json:"seriesCountByMetricName"`
		LabelValueCountByLabelName  []tsdbStat `json:"labelValueCountByLabelName"`
		MemoryInBytesByLabelName    []tsdbStat `json:"memoryInBytesByLabelName"`
		SeriesCountByLabelValuePair []tsdbStat `json:"seriesCountByLabelValuePair"`
	}

	q := url.Values{}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	if err := s.get(ctx, "/status/tsdb", q, &resp); err != nil {
		return HeadStats{}, Cardinality{}, err
	}

	head := HeadStats{
		NumSeries:     resp.HeadStats.NumSeries,
		NumLabelPairs: resp.HeadStats.NumLabelPairs,
		ChunkCount:    resp.HeadStats.ChunkCount,
		MinTime:       resp.HeadStats.MinTime,
		MaxTime:       resp.HeadStats.MaxTime,
	}
	card := Cardinality{
		// Prometheus's /status/tsdb hardcodes this breakdown to __name__;
		// there is currently no query param to change it remotely.
		LabelName:                   "__name__",
		SeriesCountByMetricName:     toStats(resp.SeriesCountByMetricName),
		LabelValueCountByLabelName:  toStats(resp.LabelValueCountByLabelName),
		MemoryInBytesByLabelName:    toStats(resp.MemoryInBytesByLabelName),
		SeriesCountByLabelValuePair: toStats(resp.SeriesCountByLabelValuePair),
	}
	return head, card, nil
}

// HeadStats implements Source.
func (s *RemoteSource) HeadStats(ctx context.Context) (HeadStats, error) {
	head, _, err := s.tsdbStatus(ctx, 1)
	return head, err
}

// CardinalityBy implements Source. RemoteSource can only honor
// labelName == "__name__" since that's all the remote API exposes; any
// other value returns an error pointing at LocalSource as the workaround.
func (s *RemoteSource) CardinalityBy(ctx context.Context, labelName string, limit int) (Cardinality, error) {
	if labelName != "" && labelName != "__name__" {
		return Cardinality{}, fmt.Errorf("remote source only supports cardinality by __name__ (Prometheus's /status/tsdb hardcodes this); use a local source for label %q", labelName)
	}
	_, card, err := s.tsdbStatus(ctx, limit)
	return card, err
}

// Blocks implements Source.
func (s *RemoteSource) Blocks(ctx context.Context) ([]Block, error) {
	var resp struct {
		Blocks []struct {
			ULID    string `json:"ulid"`
			MinTime int64  `json:"minTime"`
			MaxTime int64  `json:"maxTime"`
			Stats   struct {
				NumSamples uint64 `json:"numSamples"`
				NumSeries  uint64 `json:"numSeries"`
				NumChunks  uint64 `json:"numChunks"`
			} `json:"stats"`
			Compaction struct {
				Level   int      `json:"level"`
				Sources []string `json:"sources"`
				Parents []struct {
					ULID string `json:"ulid"`
				} `json:"parents"`
				Failed bool `json:"failed"`
			} `json:"compaction"`
		} `json:"blocks"`
	}
	if err := s.get(ctx, "/status/tsdb/blocks", nil, &resp); err != nil {
		return nil, err
	}

	blocks := make([]Block, len(resp.Blocks))
	for i, b := range resp.Blocks {
		parents := make([]string, len(b.Compaction.Parents))
		for j, p := range b.Compaction.Parents {
			parents[j] = p.ULID
		}
		blocks[i] = Block{
			ULID:       b.ULID,
			MinTime:    b.MinTime,
			MaxTime:    b.MaxTime,
			NumSeries:  b.Stats.NumSeries,
			NumSamples: b.Stats.NumSamples,
			NumChunks:  b.Stats.NumChunks,
			// Remote mode has no direct on-disk size figure; the HTTP API
			// doesn't report per-block byte size today.
			SizeBytes: -1,
			Compaction: BlockCompaction{
				Level:   b.Compaction.Level,
				Sources: b.Compaction.Sources,
				Parents: parents,
				Failed:  b.Compaction.Failed,
			},
		}
	}
	return blocks, nil
}

// RuntimeInfo implements Source.
func (s *RemoteSource) RuntimeInfo(ctx context.Context) (RuntimeInfo, error) {
	var resp struct {
		StartTime           time.Time `json:"startTime"`
		CWD                 string    `json:"CWD"`
		Hostname            string    `json:"hostname"`
		ServerTime          time.Time `json:"serverTime"`
		ReloadConfigSuccess bool      `json:"reloadConfigSuccess"`
		LastConfigTime      time.Time `json:"lastConfigTime"`
		CorruptionCount     int64     `json:"corruptionCount"`
		GoroutineCount      int       `json:"goroutineCount"`
		GOMAXPROCS          int       `json:"GOMAXPROCS"`
		GOMEMLIMIT          int64     `json:"GOMEMLIMIT"`
		GOGC                string    `json:"GOGC"`
		StorageRetention    string    `json:"storageRetention"`
	}
	if err := s.get(ctx, "/status/runtimeinfo", nil, &resp); err != nil {
		return RuntimeInfo{}, err
	}
	return RuntimeInfo{
		StartTime:           resp.StartTime.Format(time.RFC3339),
		CWD:                 resp.CWD,
		Hostname:            resp.Hostname,
		ServerTime:          resp.ServerTime.Format(time.RFC3339),
		LastConfigTime:      resp.LastConfigTime.Format(time.RFC3339),
		ReloadConfigSuccess: resp.ReloadConfigSuccess,
		CorruptionCount:     resp.CorruptionCount,
		GoroutineCount:      resp.GoroutineCount,
		GOMAXPROCS:          resp.GOMAXPROCS,
		GOMEMLIMIT:          resp.GOMEMLIMIT,
		GOGC:                resp.GOGC,
		StorageRetention:    resp.StorageRetention,
	}, nil
}

// BuildInfo implements Source.
func (s *RemoteSource) BuildInfo(ctx context.Context) (BuildInfo, error) {
	var resp struct {
		Version   string `json:"version"`
		Revision  string `json:"revision"`
		Branch    string `json:"branch"`
		GoVersion string `json:"goVersion"`
	}
	if err := s.get(ctx, "/status/buildinfo", nil, &resp); err != nil {
		return BuildInfo{}, err
	}
	return BuildInfo{
		Version:   resp.Version,
		Revision:  resp.Revision,
		Branch:    resp.Branch,
		GoVersion: resp.GoVersion,
	}, nil
}

// Flags implements Source.
func (s *RemoteSource) Flags(ctx context.Context) (map[string]string, error) {
	var resp map[string]string
	if err := s.get(ctx, "/status/flags", nil, &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// WALReplayStatus implements Source.
func (s *RemoteSource) WALReplayStatus(ctx context.Context) (WALReplayStatus, error) {
	var resp struct {
		Min     int `json:"min"`
		Max     int `json:"max"`
		Current int `json:"current"`
	}
	if err := s.get(ctx, "/status/walreplay", nil, &resp); err != nil {
		return WALReplayStatus{}, err
	}
	return WALReplayStatus{Min: resp.Min, Max: resp.Max, Current: resp.Current}, nil
}

// scrapeMetrics fetches the plain-text /metrics exposition and returns the
// values of the requested unlabeled gauge/counter names. It's a minimal
// line scanner rather than a full exposition-format parser: sufficient for
// the handful of single-sample, unlabeled prometheus_tsdb_* self-metrics
// this source needs, without pulling in expfmt/client_model as dependencies.
func (s *RemoteSource) scrapeMetrics(ctx context.Context, want ...string) (map[string]float64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.baseURL+"/metrics", nil)
	if err != nil {
		return nil, err
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("requesting /metrics: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("/metrics returned status %d", resp.StatusCode)
	}

	wanted := make(map[string]bool, len(want))
	for _, name := range want {
		wanted[name] = true
	}

	found := make(map[string]float64, len(want))
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for scanner.Scan() && len(found) < len(wanted) {
		line := scanner.Text()
		if line == "" || line[0] == '#' {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 || !wanted[fields[0]] {
			continue
		}
		value, err := strconv.ParseFloat(fields[1], 64)
		if err != nil {
			continue
		}
		found[fields[0]] = value
	}
	if err := scanner.Err(); err != nil && err != io.EOF {
		return nil, fmt.Errorf("reading /metrics: %w", err)
	}
	return found, nil
}

// WALInfo implements Source.
func (s *RemoteSource) WALInfo(ctx context.Context) (WALInfo, error) {
	metrics, err := s.scrapeMetrics(ctx, "prometheus_tsdb_wal_storage_size_bytes", "prometheus_tsdb_wal_segment_current")
	if err != nil {
		return WALInfo{}, err
	}
	return WALInfo{
		SizeBytes:      int64(metrics["prometheus_tsdb_wal_storage_size_bytes"]),
		CurrentSegment: int(metrics["prometheus_tsdb_wal_segment_current"]),
	}, nil
}

// Features implements Source.
func (s *RemoteSource) Features(ctx context.Context) (map[string]map[string]bool, error) {
	var resp map[string]map[string]bool
	if err := s.get(ctx, "/features", nil, &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// instantQueryScalar runs a PromQL instant query expected to yield a single
// vector sample (e.g. a count() aggregation) and returns its value. Prometheus
// aggregations over an empty vector return an empty result rather than a
// zero sample, so ok=false means "no data" (query itself succeeded).
func (s *RemoteSource) instantQueryScalar(ctx context.Context, query string) (value float64, ok bool, err error) {
	var resp struct {
		Result []struct {
			Value [2]any `json:"value"`
		} `json:"result"`
	}
	q := url.Values{"query": {query}}
	if err := s.get(ctx, "/query", q, &resp); err != nil {
		return 0, false, err
	}
	if len(resp.Result) == 0 {
		return 0, false, nil
	}
	valStr, isString := resp.Result[0].Value[1].(string)
	if !isString {
		return 0, false, fmt.Errorf("unexpected instant query result value type for %q", query)
	}
	f, err := strconv.ParseFloat(valStr, 64)
	if err != nil {
		return 0, false, fmt.Errorf("parsing instant query result for %q: %w", query, err)
	}
	return f, true, nil
}

// escapePromQLString escapes a string for embedding inside a double-quoted
// PromQL string literal, e.g. {__name__="<escaped>"}.
func escapePromQLString(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	return strings.ReplaceAll(s, `"`, `\"`)
}

// MetricDetail implements Source. It costs one instant query (series count),
// one /metadata lookup, one /labels lookup, and one /label/<name>/values
// call per label name found on the metric — cheap in aggregate since it's
// only run on-demand for a single metric, not on every page load.
func (s *RemoteSource) MetricDetail(ctx context.Context, metricName string) (MetricDetail, error) {
	detail := MetricDetail{Name: metricName}
	selector := fmt.Sprintf("{__name__=%q}", escapePromQLString(metricName))

	if v, ok, err := s.instantQueryScalar(ctx, "count("+selector+")"); err != nil {
		return MetricDetail{}, err
	} else if ok {
		detail.SeriesCount = int64(v)
	}

	var metaResp map[string][]MetricMetadata
	if err := s.get(ctx, "/metadata", url.Values{"metric": {metricName}}, &metaResp); err != nil {
		return MetricDetail{}, err
	}
	detail.Metadata = metaResp[metricName]

	matchQuery := url.Values{"match[]": {selector}}
	var labelNames []string
	if err := s.get(ctx, "/labels", matchQuery, &labelNames); err != nil {
		return MetricDetail{}, err
	}

	for _, name := range labelNames {
		if name == "__name__" {
			continue
		}
		var values []string
		if err := s.get(ctx, "/label/"+url.PathEscape(name)+"/values", matchQuery, &values); err != nil {
			return MetricDetail{}, err
		}
		detail.Labels = append(detail.Labels, LabelCardinality{Name: name, Count: len(values)})
	}
	sort.Slice(detail.Labels, func(i, j int) bool { return detail.Labels[i].Count > detail.Labels[j].Count })

	return detail, nil
}

// QueryRange implements Source.
func (s *RemoteSource) QueryRange(ctx context.Context, query string, start, end time.Time, step time.Duration) ([]SamplePoint, error) {
	q := url.Values{
		"query": {query},
		"start": {start.Format(time.RFC3339Nano)},
		"end":   {end.Format(time.RFC3339Nano)},
		"step":  {step.String()},
	}
	var resp struct {
		ResultType string `json:"resultType"`
		Result     []struct {
			Metric map[string]string `json:"metric"`
			Values [][]any           `json:"values"`
		} `json:"result"`
	}
	if err := s.get(ctx, "/query_range", q, &resp); err != nil {
		return nil, err
	}
	if len(resp.Result) == 0 {
		return nil, nil
	}
	raw := resp.Result[0].Values
	out := make([]SamplePoint, 0, len(raw))
	for _, v := range raw {
		if len(v) != 2 {
			continue
		}
		var ts float64
		switch t := v[0].(type) {
		case float64:
			ts = t
		case json.Number:
			f, _ := t.Float64()
			ts = f
		default:
			continue
		}
		sVal, ok := v[1].(string)
		if !ok {
			continue
		}
		f, err := strconv.ParseFloat(sVal, 64)
		if err != nil {
			continue
		}
		sec := int64(ts)
		nsec := int64((ts - float64(sec)) * 1e9)
		out = append(out, SamplePoint{Timestamp: time.Unix(sec, nsec).UTC(), Value: f})
	}
	return out, nil
}

// UniqueMetricCount implements Source.
func (s *RemoteSource) UniqueMetricCount(ctx context.Context) (int, error) {
	var names []string
	if err := s.get(ctx, "/label/__name__/values", nil, &names); err != nil {
		return 0, err
	}
	return len(names), nil
}

var _ Source = (*RemoteSource)(nil)
