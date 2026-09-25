package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/kvsvishnukumar/prom-viewer/internal/source"
	"github.com/spf13/cobra"
)

type cardinalityOptions struct {
	metricRegex   string
	labelMatchers []string
	groupBy       []string
	format        string
	queryTime     string
}

type cardinalityQuery struct {
	expression string
	groupBy    []string
	metricMode bool
}

type countRow struct {
	Metric       string            `json:"metric,omitempty"`
	Labels       map[string]string `json:"labels,omitempty"`
	ActiveSeries int64             `json:"active_series"`
}

type cardinalityResult struct {
	Query             string     `json:"query"`
	GroupBy           []string   `json:"group_by"`
	TotalActiveSeries int64      `json:"total_active_series"`
	Rows              []countRow `json:"rows"`
}

var (
	labelNamePattern = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)
	matcherPattern   = regexp.MustCompile(`^([a-zA-Z_][a-zA-Z0-9_]*)(=~|!~|!=|=)(.*)$`)
)

func newCardinalityCommand(rootOpts *rootOptions) *cobra.Command {
	opts := &cardinalityOptions{}

	cmd := &cobra.Command{
		Use:     "cardinality",
		Aliases: []string{"count"},
		Short:   "Count active time series by metric or label",
		Long: `Run a read-only Prometheus instant query and report the number of active
series in each result group. The query is evaluated at the current time unless
--time is provided.`,
		Example: `  # Count active series for every metric matching an Envoy-style prefix
  promviewerctl cardinality --metric-regex '^envoy_.*'

  # Count active series for every metric, grouped by a label
  promviewerctl cardinality --metric-regex '.+' --group-by actual_destination

  # Count matching series by an Envoy label value
  promviewerctl cardinality \
    --metric-regex '^envoy_.*' \
    --label-regex 'envoy_cluster_name=~"cluster-a|cluster-b"' \
    --group-by envoy_cluster_name

  # Emit machine-readable JSON, grouped by metric and label
  promviewerctl cardinality \
    --metric-regex '^envoy_.*' \
    --group-by __name__,envoy_cluster_name \
    --format json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runCardinality(cmd, rootOpts, opts)
		},
	}

	flags := cmd.Flags()
	flags.StringVar(&opts.metricRegex, "metric-regex", "", "Required regex to match against the __name__ label. Use \".+\" to match every metric; \".*\" is rejected by Prometheus because it also matches the empty string.")
	flags.StringArrayVar(&opts.labelMatchers, "label-regex", nil, "Optional Prometheus label matcher; repeat for multiple matchers (for example 'envoy_cluster_name=~\"foo.*\"').")
	flags.StringSliceVar(&opts.groupBy, "group-by", nil, "Label to group by; repeat or comma-separate labels. Defaults to __name__.")
	flags.StringVarP(&opts.format, "format", "o", "table", "Output format: table or json.")
	flags.StringVar(&opts.queryTime, "time", "", "Optional RFC3339 evaluation time; defaults to now.")
	_ = cmd.MarkFlagRequired("metric-regex")

	return cmd
}

func runCardinality(cmd *cobra.Command, rootOpts *rootOptions, opts *cardinalityOptions) error {
	if err := rootOpts.validate(); err != nil {
		return err
	}
	if opts.format != "table" && opts.format != "json" {
		return fmt.Errorf("unsupported output format %q: use table or json", opts.format)
	}

	query, err := buildCardinalityQuery(opts.metricRegex, opts.labelMatchers, opts.groupBy)
	if err != nil {
		return err
	}

	var at time.Time
	if strings.TrimSpace(opts.queryTime) != "" {
		at, err = time.Parse(time.RFC3339, strings.TrimSpace(opts.queryTime))
		if err != nil {
			return fmt.Errorf("invalid --time %q: expected RFC3339: %w", opts.queryTime, err)
		}
	}

	ctx, cancel := context.WithTimeout(cmd.Context(), rootOpts.Timeout)
	defer cancel()

	samples, err := rootOpts.remoteSource().QueryAt(ctx, query.expression, at)
	if err != nil {
		return fmt.Errorf("query Prometheus: %w", err)
	}

	result, err := makeCardinalityResult(query, samples)
	if err != nil {
		return err
	}
	return writeCardinalityResult(cmd.OutOrStdout(), opts.format, result)
}

func buildCardinalityQuery(metricRegex string, rawMatchers, rawGroupBy []string) (cardinalityQuery, error) {
	if metricRegex == "" {
		return cardinalityQuery{}, fmt.Errorf("--metric-regex must not be empty")
	}
	metricPattern, err := regexp.Compile(metricRegex)
	if err != nil {
		return cardinalityQuery{}, fmt.Errorf("invalid --metric-regex %q: %w", metricRegex, err)
	}

	groupBy, err := normalizeGroupBy(rawGroupBy)
	if err != nil {
		return cardinalityQuery{}, err
	}

	// Prometheus rejects a vector selector when every matcher also matches the
	// empty string, so "{ __name__=~\".*\" }" is a parse error rather than a
	// query for all metrics. Detect that here to report it as a flag mistake
	// instead of an opaque upstream 400. See matchesEmptyLabel.
	hasNonEmptyMatcher := !metricPattern.MatchString("")

	selectors := make([]string, 0, len(rawMatchers)+1)
	selectors = append(selectors, "__name__=~"+quotePromQLString(metricRegex))
	for _, rawMatcher := range rawMatchers {
		name, operator, value, err := parseMatcher(rawMatcher)
		if err != nil {
			return cardinalityQuery{}, err
		}
		if !matchesEmptyLabel(operator, value) {
			hasNonEmptyMatcher = true
		}
		selectors = append(selectors, name+operator+quotePromQLString(value))
	}

	if !hasNonEmptyMatcher {
		return cardinalityQuery{}, fmt.Errorf("every matcher also matches the empty string, which Prometheus rejects: use %q instead of %q to match all metric names", ".+", metricRegex)
	}

	expression := fmt.Sprintf("count by (%s) ({ %s })", strings.Join(groupBy, ", "), strings.Join(selectors, ", "))
	return cardinalityQuery{
		expression: expression,
		groupBy:    groupBy,
		metricMode: len(groupBy) == 1 && groupBy[0] == "__name__",
	}, nil
}

func quotePromQLString(value string) string {
	return `"` + source.EscapePromQLString(value) + `"`
}

func normalizeGroupBy(raw []string) ([]string, error) {
	if len(raw) == 0 {
		return []string{"__name__"}, nil
	}

	seen := make(map[string]struct{}, len(raw))
	groupBy := make([]string, 0, len(raw))
	for _, value := range raw {
		for _, label := range strings.Split(value, ",") {
			label = strings.TrimSpace(label)
			if !labelNamePattern.MatchString(label) {
				return nil, fmt.Errorf("invalid group-by label %q", label)
			}
			if _, ok := seen[label]; ok {
				continue
			}
			seen[label] = struct{}{}
			groupBy = append(groupBy, label)
		}
	}
	if len(groupBy) == 0 {
		return nil, fmt.Errorf("--group-by must contain at least one label")
	}
	return groupBy, nil
}

// matchesEmptyLabel reports whether a label matcher also matches a series in
// which that label is absent or empty. Prometheus requires at least one matcher
// in a vector selector that does not, which is why __name__=~".*" is invalid
// while __name__=~".+" is not.
func matchesEmptyLabel(operator, value string) bool {
	switch operator {
	case "=":
		return value == ""
	case "!=":
		// A series without the label still satisfies "!=" against a non-empty
		// value, because the absent label is treated as empty.
		return value != ""
	case "=~":
		pattern, err := regexp.Compile(value)
		return err == nil && pattern.MatchString("")
	case "!~":
		pattern, err := regexp.Compile(value)
		return err == nil && !pattern.MatchString("")
	default:
		return true
	}
}

func parseMatcher(raw string) (string, string, string, error) {
	raw = strings.TrimSpace(raw)
	matches := matcherPattern.FindStringSubmatch(raw)
	if matches == nil {
		return "", "", "", fmt.Errorf("invalid --label-regex %q: expected label=~\\\"regex\\\", label=~\\\"value\\\", label!=value, or label!~regex", raw)
	}

	name, operator, encodedValue := matches[1], matches[2], strings.TrimSpace(matches[3])
	value, err := decodeMatcherValue(encodedValue)
	if err != nil {
		return "", "", "", fmt.Errorf("invalid --label-regex %q: %w", raw, err)
	}
	if operator == "=~" || operator == "!~" {
		if _, err := regexp.Compile(value); err != nil {
			return "", "", "", fmt.Errorf("invalid regex in --label-regex %q: %w", raw, err)
		}
	}
	return name, operator, value, nil
}

func decodeMatcherValue(raw string) (string, error) {
	if raw == "" {
		return "", nil
	}
	if raw[0] != '"' {
		if strings.Contains(raw, `"`) {
			return "", fmt.Errorf("unquoted value contains a quote")
		}
		return raw, nil
	}
	if len(raw) < 2 || raw[len(raw)-1] != '"' {
		return "", fmt.Errorf("quoted value is not terminated")
	}

	inner := raw[1 : len(raw)-1]
	var decoded strings.Builder
	escaped := false
	for _, r := range inner {
		if escaped {
			switch r {
			case '"', '\\':
				decoded.WriteRune(r)
			default:
				// Preserve unknown escapes so regular expressions such as
				// \. survive the CLI-to-PromQL string round trip.
				decoded.WriteRune('\\')
				decoded.WriteRune(r)
			}
			escaped = false
			continue
		}
		if r == '\\' {
			escaped = true
			continue
		}
		if r == '"' {
			return "", fmt.Errorf("unescaped quote in value")
		}
		decoded.WriteRune(r)
	}
	if escaped {
		return "", fmt.Errorf("value ends with an incomplete escape")
	}
	return decoded.String(), nil
}

func makeCardinalityResult(query cardinalityQuery, samples []source.VectorSample) (cardinalityResult, error) {
	result := cardinalityResult{
		Query:   query.expression,
		GroupBy: append([]string(nil), query.groupBy...),
		Rows:    make([]countRow, 0, len(samples)),
	}

	for i, sample := range samples {
		count, err := activeSeriesCount(sample.Value)
		if err != nil {
			return cardinalityResult{}, fmt.Errorf("result %d has invalid count: %w", i, err)
		}
		if result.TotalActiveSeries > math.MaxInt64-count {
			return cardinalityResult{}, fmt.Errorf("result count exceeds int64")
		}
		result.TotalActiveSeries += count

		row := countRow{ActiveSeries: count}
		if query.metricMode {
			row.Metric = sample.Metric["__name__"]
		} else {
			row.Labels = make(map[string]string, len(query.groupBy))
			for _, label := range query.groupBy {
				if label == "__name__" {
					row.Metric = sample.Metric[label]
					continue
				}
				row.Labels[label] = sample.Metric[label]
			}
		}
		result.Rows = append(result.Rows, row)
	}

	sort.Slice(result.Rows, func(i, j int) bool {
		if result.Rows[i].ActiveSeries != result.Rows[j].ActiveSeries {
			return result.Rows[i].ActiveSeries > result.Rows[j].ActiveSeries
		}
		return rowSortKey(result.Rows[i]) < rowSortKey(result.Rows[j])
	})
	return result, nil
}

func activeSeriesCount(value float64) (int64, error) {
	if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || math.Trunc(value) != value || value > float64(math.MaxInt64) {
		return 0, fmt.Errorf("expected a non-negative integer, got %v", value)
	}
	return int64(value), nil
}

func rowSortKey(row countRow) string {
	var key strings.Builder
	key.WriteString(row.Metric)
	labels := make([]string, 0, len(row.Labels))
	for label := range row.Labels {
		labels = append(labels, label)
	}
	sort.Strings(labels)
	for _, label := range labels {
		key.WriteByte(0)
		key.WriteString(label)
		key.WriteByte('=')
		key.WriteString(row.Labels[label])
	}
	return key.String()
}

func writeCardinalityResult(w io.Writer, format string, result cardinalityResult) error {
	if format == "json" {
		encoder := json.NewEncoder(w)
		encoder.SetIndent("", "  ")
		encoder.SetEscapeHTML(false)
		return encoder.Encode(result)
	}
	return writeCardinalityTable(w, result)
}

func writeCardinalityTable(w io.Writer, result cardinalityResult) error {
	if _, err := fmt.Fprintf(w, "Total active series: %d\n\n", result.TotalActiveSeries); err != nil {
		return err
	}

	writer := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	headers := make([]string, 0, len(result.GroupBy)+1)
	for _, label := range result.GroupBy {
		if label == "__name__" {
			headers = append(headers, "METRIC")
		} else {
			headers = append(headers, strings.ToUpper(label))
		}
	}
	headers = append(headers, "ACTIVE_SERIES")
	if _, err := fmt.Fprintln(writer, strings.Join(headers, "\t")); err != nil {
		return err
	}

	for _, row := range result.Rows {
		cells := make([]string, 0, len(result.GroupBy)+1)
		for _, label := range result.GroupBy {
			if label == "__name__" {
				cells = append(cells, row.Metric)
			} else {
				cells = append(cells, row.Labels[label])
			}
		}
		cells = append(cells, strconv.FormatInt(row.ActiveSeries, 10))
		if _, err := fmt.Fprintln(writer, strings.Join(cells, "\t")); err != nil {
			return err
		}
	}
	return writer.Flush()
}
