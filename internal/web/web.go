// Package web renders prom-viewer's server-side HTML pages.
package web

import (
	"context"
	"embed"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/kvsvishnukumar/prom-viewer/internal/source"
)

// maxMetricSearchScan is the server-side cap Prometheus itself enforces on
// /status/tsdb's limit param (web/api/v1/api.go's maxTSDBLimit). Since the
// underlying postings-index walk costs the same regardless of the limit
// requested, asking for the max and filtering locally is how a "search"
// over every metric name is implemented without any extra server cost.
const maxMetricSearchScan = 10000

// defaultCardinalityLimit is the default row count shown in the "top N
// series by metric name" table when no search is active.
const defaultCardinalityLimit = 10

// maxSearchResults caps how many matches a metric-name search renders, so a
// broad/generic search term can't blow up the page.
const maxSearchResults = 50

//go:embed templates/*.html
var templateFS embed.FS

//go:embed static
var staticFS embed.FS

// Handler serves prom-viewer's HTML pages against a Source.
type Handler struct {
	src    source.Source
	tmpl   *template.Template
	logger *slog.Logger
}

// NewHandler builds a Handler that reads from src.
func NewHandler(src source.Source, logger *slog.Logger) (*Handler, error) {
	tmpl, err := template.New("").Funcs(template.FuncMap{
		"unixMillis": func(ms int64) string {
			if ms <= 0 {
				return "n/a"
			}
			return time.UnixMilli(ms).UTC().Format(time.RFC3339)
		},
		"humanBytes": humanBytes,
		"humanBytesF": func(v float64) string { return humanBytes(int64(v)) },
		"humanCPU": func(v float64) string {
			if v < 0 {
				return "n/a"
			}
			return fmt.Sprintf("%.2f cores (%.0f%%)", v, v*100)
		},
	}).ParseFS(templateFS, "templates/*.html")
	if err != nil {
		return nil, err
	}
	return &Handler{src: src, tmpl: tmpl, logger: logger}, nil
}

// humanBytes formats a byte count as a human-readable size, e.g. "4.2 MiB".
func humanBytes(n int64) string {
	if n < 0 {
		return "n/a"
	}
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for x := n / unit; x >= unit; x /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

// Routes registers prom-viewer's HTTP routes on mux.
func (h *Handler) Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /", h.overview)
	// Partial routes back the htmx-driven sections: each renders just its
	// own fragment so a search/lookup swaps in-place instead of reloading
	// the whole page. They also work as plain full navigations (a form's
	// method=get/action=/ fallback still hits "/" directly), so the page
	// is fully functional with JS disabled too.
	mux.HandleFunc("GET /partials/cardinality", h.cardinalityPartial)
	mux.HandleFunc("GET /partials/metric-detail", h.metricDetailPartial)
	mux.Handle("GET /static/", http.FileServerFS(staticFS))
}

type overviewData struct {
	Head        source.HeadStats
	Build       source.BuildInfo
	Runtime     source.RuntimeInfo
	WALReplay   source.WALReplayStatus
	WALInfo     source.WALInfo
	BlockCount  int
	Flags       []flagRow
	Err         string

	// WALReplayInProgress and WALReplayPercent are derived from WALReplay
	// so the template can show a live progress bar instead of a table
	// that's almost always just zeroes (replay finishes in well under a
	// second for most Prometheus instances).
	WALReplayInProgress bool
	WALReplayPercent    int

	Resource ResourceData

	cardinalityData
	metricDetailData
}

// ResourceData holds 1h CPU/memory graphs.
type ResourceData struct {
	CPU GraphData
	Mem GraphData
	Err string
}

// GraphData is a single sparkline.
type GraphData struct {
	Points  []source.SamplePoint
	Path    string // SVG path
	AreaPath string // filled area path
	Min     float64
	Max     float64
	Current float64
	Empty   bool
}

// cardinalityData backs the "top N / search metric names" section. It's
// also rendered standalone by the /partials/cardinality htmx endpoint.
type cardinalityData struct {
	Limit        int
	MetricSearch string
	Cardinality  source.Cardinality
}

// metricDetailData backs the "cardinality of a metric" lookup section. It's
// also rendered standalone by the /partials/metric-detail htmx endpoint.
type metricDetailData struct {
	DetailMetric    string
	MetricDetail    source.MetricDetail
	MetricDetailErr string
}

type flagRow struct {
	Name  string
	Value string
}

// filterStats returns the entries in stats whose Name contains query
// (case-insensitive), preserving order, capped at max results. stats is
// expected to already be sorted descending by Value, so the cap naturally
// keeps the highest-count matches.
func filterStats(stats []source.Stat, query string, max int) []source.Stat {
	q := strings.ToLower(query)
	out := make([]source.Stat, 0, max)
	for _, s := range stats {
		if !strings.Contains(strings.ToLower(s.Name), q) {
			continue
		}
		out = append(out, s)
		if len(out) >= max {
			break
		}
	}
	return out
}

func (h *Handler) fetchCardinality(ctx context.Context, r *http.Request) cardinalityData {
	var data cardinalityData

	limit := defaultCardinalityLimit
	if s := r.URL.Query().Get("limit"); s != "" {
		if v, err := strconv.Atoi(s); err == nil && v > 0 {
			limit = v
		}
	}
	metricSearch := strings.TrimSpace(r.URL.Query().Get("metric"))
	data.Limit = limit
	data.MetricSearch = metricSearch

	cardinalityLimit := limit
	if metricSearch != "" {
		cardinalityLimit = maxMetricSearchScan
	}
	if v, err := h.src.CardinalityBy(ctx, "__name__", cardinalityLimit); err != nil {
		h.logger.Error("cardinality", "err", err)
	} else {
		if metricSearch != "" {
			v.SeriesCountByMetricName = filterStats(v.SeriesCountByMetricName, metricSearch, maxSearchResults)
		}
		data.Cardinality = v
	}
	return data
}

func (h *Handler) fetchMetricDetail(ctx context.Context, r *http.Request) metricDetailData {
	var data metricDetailData
	data.DetailMetric = strings.TrimSpace(r.URL.Query().Get("detail_metric"))
	if data.DetailMetric != "" {
		if v, err := h.src.MetricDetail(ctx, data.DetailMetric); err != nil {
			h.logger.Error("metric detail", "metric", data.DetailMetric, "err", err)
			data.MetricDetailErr = err.Error()
		} else {
			data.MetricDetail = v
		}
	}
	return data
}

// pushURL tells an htmx partial response to make the browser address bar
// show the equivalent full-page "/" URL instead of the partial's own
// "/partials/..." path, so the current search/lookup stays bookmarkable.
func pushURL(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("HX-Push-Url", "/?"+r.URL.RawQuery)
}

func (h *Handler) render(w http.ResponseWriter, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := h.tmpl.ExecuteTemplate(w, name, data); err != nil {
		h.logger.Error("render template", "template", name, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}

func (h *Handler) cardinalityPartial(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	pushURL(w, r)
	h.render(w, "cardinality.html", h.fetchCardinality(ctx, r))
}

func (h *Handler) metricDetailPartial(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	pushURL(w, r)
	h.render(w, "metric_detail.html", h.fetchMetricDetail(ctx, r))
}

func buildGraph(points []source.SamplePoint) GraphData {
	if len(points) == 0 {
		return GraphData{Empty: true}
	}
	min, max := points[0].Value, points[0].Value
	for _, p := range points[1:] {
		if p.Value < min {
			min = p.Value
		}
		if p.Value > max {
			max = p.Value
		}
	}
	// pad flat line so SVG has height
	if min == max {
		min -= 1
		max += 1
		if min < 0 {
			min = 0
		}
	}
	const w, h = 600.0, 80.0
	const pad = 2.0
	n := len(points)
	var sb strings.Builder
	var area strings.Builder
	for i, p := range points {
		x := float64(i) / float64(n-1) * w
		norm := (p.Value - min) / (max - min)
		y := h - norm*(h-pad*2) - pad
		if i == 0 {
			sb.WriteString(fmt.Sprintf("M %.2f %.2f", x, y))
			area.WriteString(fmt.Sprintf("M %.2f %.2f L %.2f %.2f", x, h-pad, x, y))
		} else {
			sb.WriteString(fmt.Sprintf(" L %.2f %.2f", x, y))
			area.WriteString(fmt.Sprintf(" L %.2f %.2f", x, y))
		}
		if i == n-1 {
			area.WriteString(fmt.Sprintf(" L %.2f %.2f Z", w, h-pad))
		}
	}
	return GraphData{
		Points:   points,
		Path:     sb.String(),
		AreaPath: area.String(),
		Min:      min,
		Max:      max,
		Current:  points[len(points)-1].Value,
	}
}

func (h *Handler) fetchResource(ctx context.Context) ResourceData {
	end := time.Now()
	start := end.Add(-1 * time.Hour)
	step := 30 * time.Second
	cpuQ := "rate(process_cpu_seconds_total[2m])"
	memQ := "process_resident_memory_bytes"
	ctx2, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cpu, err1 := h.src.QueryRange(ctx2, cpuQ, start, end, step)
	if err1 != nil {
		h.logger.Error("cpu query_range", "err", err1)
		return ResourceData{Err: err1.Error()}
	}
	mem, err2 := h.src.QueryRange(ctx2, memQ, start, end, step)
	if err2 != nil {
		h.logger.Error("mem query_range", "err", err2)
		return ResourceData{CPU: buildGraph(cpu), Err: err2.Error()}
	}
	return ResourceData{CPU: buildGraph(cpu), Mem: buildGraph(mem)}
}

func (h *Handler) overview(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	data := overviewData{}

	if v, err := h.src.HeadStats(ctx); err != nil {
		h.logger.Error("head stats", "err", err)
		data.Err = err.Error()
	} else {
		data.Head = v
	}
	data.Resource = h.fetchResource(ctx)
	data.cardinalityData = h.fetchCardinality(ctx, r)
	data.metricDetailData = h.fetchMetricDetail(ctx, r)
	if v, err := h.src.BuildInfo(ctx); err != nil {
		h.logger.Error("build info", "err", err)
	} else {
		data.Build = v
	}
	if v, err := h.src.RuntimeInfo(ctx); err != nil {
		h.logger.Error("runtime info", "err", err)
	} else {
		data.Runtime = v
	}
	if v, err := h.src.WALReplayStatus(ctx); err != nil {
		h.logger.Error("wal replay status", "err", err)
	} else {
		data.WALReplay = v
		data.WALReplayInProgress = v.Current < v.Max
		if data.WALReplayInProgress {
			if span := v.Max - v.Min; span > 0 {
				data.WALReplayPercent = (v.Current - v.Min) * 100 / span
			}
		}
	}
	if v, err := h.src.WALInfo(ctx); err != nil {
		h.logger.Error("wal info", "err", err)
	} else {
		data.WALInfo = v
	}
	if blocks, err := h.src.Blocks(ctx); err != nil {
		h.logger.Error("blocks", "err", err)
	} else {
		data.BlockCount = len(blocks)
	}
	if flags, err := h.src.Flags(ctx); err != nil {
		h.logger.Error("flags", "err", err)
	} else {
		names := make([]string, 0, len(flags))
		for name := range flags {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			data.Flags = append(data.Flags, flagRow{Name: name, Value: flags[name]})
		}
	}

	h.render(w, "overview.html", data)
}
