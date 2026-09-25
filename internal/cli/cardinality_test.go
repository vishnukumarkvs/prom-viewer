package cli

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kvsvishnukumar/prom-viewer/internal/source"
)

func TestBuildCardinalityQuery(t *testing.T) {
	query, err := buildCardinalityQuery(
		"^envoy_.*",
		[]string{`envoy_cluster_name=~"cluster-a|cluster-b"`},
		[]string{"__name__,envoy_cluster_name"},
	)
	if err != nil {
		t.Fatalf("buildCardinalityQuery() error = %v", err)
	}

	want := `count by (__name__, envoy_cluster_name) ({ __name__=~"^envoy_.*", envoy_cluster_name=~"cluster-a|cluster-b" })`
	if query.expression != want {
		t.Errorf("expression = %q, want %q", query.expression, want)
	}
	if query.metricMode {
		t.Error("metricMode = true, want false for metric-plus-label grouping")
	}
}

func TestBuildCardinalityQueryRejectsInvalidMatcher(t *testing.T) {
	if _, err := buildCardinalityQuery("^envoy_", []string{"envoy_cluster_name"}, nil); err == nil {
		t.Fatal("buildCardinalityQuery() error = nil, want invalid matcher error")
	}
}

// Prometheus rejects a vector selector whose matchers all match the empty
// string, so ".*" must fail locally rather than becoming an upstream 400.
func TestBuildCardinalityQueryRejectsEmptyMatchingRegex(t *testing.T) {
	for _, metricRegex := range []string{".*", "", "^$", ".{0,3}"} {
		if _, err := buildCardinalityQuery(metricRegex, nil, nil); err == nil && metricRegex != "" {
			t.Errorf("buildCardinalityQuery(%q) error = nil, want empty-matcher error", metricRegex)
		}
	}
}

func TestBuildCardinalityQueryAllowsNonEmptyRegex(t *testing.T) {
	query, err := buildCardinalityQuery(".+", nil, []string{"actual_destination"})
	if err != nil {
		t.Fatalf("buildCardinalityQuery() error = %v", err)
	}
	want := `count by (actual_destination) ({ __name__=~".+" })`
	if query.expression != want {
		t.Errorf("expression = %q, want %q", query.expression, want)
	}
}

// A label matcher that cannot match the empty string satisfies the Prometheus
// rule on its own, so ".*" stays acceptable alongside it.
func TestBuildCardinalityQueryEmptyRegexWithNonEmptyLabelMatcher(t *testing.T) {
	query, err := buildCardinalityQuery(".*", []string{`job="api"`}, nil)
	if err != nil {
		t.Fatalf("buildCardinalityQuery() error = %v", err)
	}
	want := `count by (__name__) ({ __name__=~".*", job="api" })`
	if query.expression != want {
		t.Errorf("expression = %q, want %q", query.expression, want)
	}
}

func TestMatchesEmptyLabel(t *testing.T) {
	tests := []struct {
		operator string
		value    string
		want     bool
	}{
		{"=", "api", false},
		{"=", "", true},
		{"!=", "api", true},
		{"!=", "", false},
		{"=~", ".+", false},
		{"=~", ".*", true},
		{"!~", ".+", true},
		{"!~", ".*", false},
	}
	for _, test := range tests {
		if got := matchesEmptyLabel(test.operator, test.value); got != test.want {
			t.Errorf("matchesEmptyLabel(%q, %q) = %v, want %v", test.operator, test.value, got, test.want)
		}
	}
}

func TestMakeCardinalityResultSortsAndTotals(t *testing.T) {
	query := cardinalityQuery{
		expression: "count by (__name__) ({ __name__=~\".*\" })",
		groupBy:    []string{"__name__"},
		metricMode: true,
	}
	samples := []source.VectorSample{
		{Metric: map[string]string{"__name__": "b"}, Value: 2},
		{Metric: map[string]string{"__name__": "a"}, Value: 2},
		{Metric: map[string]string{"__name__": "c"}, Value: 5},
	}

	result, err := makeCardinalityResult(query, samples)
	if err != nil {
		t.Fatalf("makeCardinalityResult() error = %v", err)
	}
	if result.TotalActiveSeries != 9 {
		t.Errorf("total = %d, want 9", result.TotalActiveSeries)
	}
	if got := []string{result.Rows[0].Metric, result.Rows[1].Metric, result.Rows[2].Metric}; strings.Join(got, ",") != "c,a,b" {
		t.Errorf("metric order = %v, want [c a b]", got)
	}
}

func TestCardinalityTableOutput(t *testing.T) {
	result := cardinalityResult{
		GroupBy:           []string{"envoy_cluster_name"},
		TotalActiveSeries: 4,
		Rows: []countRow{{
			Labels:       map[string]string{"envoy_cluster_name": "cluster-a"},
			ActiveSeries: 4,
		}},
	}

	var output bytes.Buffer
	if err := writeCardinalityResult(&output, "table", result); err != nil {
		t.Fatalf("writeCardinalityResult() error = %v", err)
	}
	for _, want := range []string{"Total active series: 4", "ENVOY_CLUSTER_NAME", "cluster-a", "4"} {
		if !strings.Contains(output.String(), want) {
			t.Errorf("table output does not contain %q:\n%s", want, output.String())
		}
	}
}

func TestRootCommandCardinalityJSON(t *testing.T) {
	var gotQuery string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/query" {
			t.Errorf("path = %q, want /api/v1/query", r.URL.Path)
		}
		gotQuery = r.URL.Query().Get("query")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[{"metric":{"__name__":"envoy_up"},"value":[1700000000,"7"]}]}}`))
	}))
	defer server.Close()

	root := NewRootCommand()
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs([]string{
		"--prometheus-url", server.URL,
		"--timeout", "2s",
		"cardinality",
		"--metric-regex", "^envoy_",
		"--format", "json",
	})
	if err := root.Execute(); err != nil {
		t.Fatalf("Execute() error = %v (stderr: %s)", err, stderr.String())
	}

	if !strings.Contains(gotQuery, `count by (__name__)`) {
		t.Errorf("query = %q, want metric-name count query", gotQuery)
	}
	var result cardinalityResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("JSON output is invalid: %v\n%s", err, stdout.String())
	}
	if result.TotalActiveSeries != 7 {
		t.Errorf("total_active_series = %d, want 7", result.TotalActiveSeries)
	}
	if len(result.Rows) != 1 || result.Rows[0].Metric != "envoy_up" || result.Rows[0].ActiveSeries != 7 {
		t.Errorf("rows = %+v, want one envoy_up row with count 7", result.Rows)
	}
}
