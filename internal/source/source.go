// Package source defines the data-source abstraction that both the remote
// (HTTP API) and local (on-disk TSDB) backends implement, so the web/api
// layer can stay agnostic of where the data actually comes from.
package source

import (
	"context"
	"time"
)

// Stat is a single named counter, e.g. a metric name and its series count.
type Stat struct {
	Name  string
	Value uint64
}

// HeadStats mirrors Prometheus's own TSDB head snapshot.
type HeadStats struct {
	NumSeries     uint64
	NumLabelPairs int
	ChunkCount    int64
	MinTime       int64
	MaxTime       int64
}

// Cardinality holds the four "top N" breakdowns Prometheus's /status/tsdb
// exposes, plus the label name they were computed against.
type Cardinality struct {
	LabelName                   string
	SeriesCountByMetricName     []Stat
	LabelValueCountByLabelName  []Stat
	MemoryInBytesByLabelName    []Stat
	SeriesCountByLabelValuePair []Stat
}

// BlockCompaction describes a block's position in the compaction lineage.
type BlockCompaction struct {
	Level   int
	Sources []string
	Parents []string
	Failed  bool
}

// Block is a single on-disk TSDB block's metadata.
type Block struct {
	ULID       string
	MinTime    int64
	MaxTime    int64
	NumSeries  uint64
	NumSamples uint64
	NumChunks  uint64
	SizeBytes  int64
	Compaction BlockCompaction
}

// RuntimeInfo mirrors Prometheus's /status/runtimeinfo response.
type RuntimeInfo struct {
	StartTime           string
	CWD                 string
	Hostname            string
	ServerTime          string
	LastConfigTime      string
	ReloadConfigSuccess bool
	CorruptionCount     int64
	GoroutineCount      int
	GOMAXPROCS          int
	GOMEMLIMIT          int64
	GOGC                string
	StorageRetention    string
}

// BuildInfo mirrors Prometheus's /status/buildinfo response.
type BuildInfo struct {
	Version   string
	Revision  string
	Branch    string
	GoVersion string
}

// WALReplayStatus mirrors Prometheus's /status/walreplay response.
type WALReplayStatus struct {
	Min     int
	Max     int
	Current int
}

// WALInfo holds the current write-ahead-log size and segment position.
// Prometheus doesn't expose these via its JSON status API, only as
// self-metrics (prometheus_tsdb_wal_storage_size_bytes,
// prometheus_tsdb_wal_segment_current).
type WALInfo struct {
	SizeBytes      int64
	CurrentSegment int
}

// MetricMetadata holds a metric's type/help/unit, as reported by the scrape
// targets exposing it (Prometheus deduplicates identical metadata across
// targets, but different targets can disagree, hence a slice on MetricDetail).
type MetricMetadata struct {
	Type string
	Help string
	Unit string
}

// LabelCardinality is a label name and how many distinct values it takes
// across a specific metric's series (not the whole database).
type LabelCardinality struct {
	Name  string
	Count int
}

// MetricDetail holds cardinality and metadata details for a single metric
// name, e.g. "how many series does this metric have, and which labels are
// driving that count".
type MetricDetail struct {
	Name        string
	SeriesCount int64
	Metadata    []MetricMetadata
	// Labels excludes __name__ (trivially constant for a single metric) and
	// is sorted descending by Count.
	Labels []LabelCardinality
}

// SamplePoint is a single timestamp/value pair from a Prometheus range query.
type SamplePoint struct {
	Timestamp time.Time
	Value     float64
}

// VectorSample is a single result from a Prometheus instant vector query.
type VectorSample struct {
	Metric    map[string]string
	Value     float64
	Timestamp time.Time
}

// RecordingRule is a single recording rule within a group.
type RecordingRule struct {
	Name           string
	Query          string
	Labels         map[string]string
	Health         string
	EvaluationTime float64
	LastEvaluation time.Time
}

// RuleGroup is a Prometheus rule group, filtered to recording rules for UI.
type RuleGroup struct {
	Name           string
	File           string
	Interval       float64
	EvaluationTime float64
	LastEvaluation time.Time
	RecordingRules []RecordingRule
	TotalRules     int // all rules in group (for reference)
}

// Source is implemented by each backend (remote HTTP API, local TSDB read)
// that prom-viewer can pull operational data from.
type Source interface {
	// HeadStats returns the current in-memory head snapshot.
	HeadStats(ctx context.Context) (HeadStats, error)
	// CardinalityBy returns top-N cardinality breakdowns for the given
	// label name (Prometheus's own API hardcodes this to "__name__").
	CardinalityBy(ctx context.Context, labelName string, limit int) (Cardinality, error)
	// Blocks lists on-disk TSDB blocks.
	Blocks(ctx context.Context) ([]Block, error)
	// RuntimeInfo returns process/runtime status.
	RuntimeInfo(ctx context.Context) (RuntimeInfo, error)
	// BuildInfo returns version/build metadata.
	BuildInfo(ctx context.Context) (BuildInfo, error)
	// Flags returns the running server's CLI flags.
	Flags(ctx context.Context) (map[string]string, error)
	// WALReplayStatus returns WAL replay progress (zero value once replay
	// has completed).
	WALReplayStatus(ctx context.Context) (WALReplayStatus, error)
	// WALInfo returns the current WAL size and segment position.
	WALInfo(ctx context.Context) (WALInfo, error)
	// Features returns the enabled feature-flag map, keyed by category
	// (e.g. Features(ctx)["api"]["admin"] reports admin-API availability).
	Features(ctx context.Context) (map[string]map[string]bool, error)
	// MetricDetail returns cardinality and metadata details for a single
	// named metric. A metric with no matching series returns a zero-value
	// (empty) MetricDetail rather than an error.
	MetricDetail(ctx context.Context, metricName string) (MetricDetail, error)
	// QueryRange runs a PromQL range query and returns the first series' samples.
	QueryRange(ctx context.Context, query string, start, end time.Time, step time.Duration) ([]SamplePoint, error)
	// UniqueMetricCount returns the number of distinct metric names.
	UniqueMetricCount(ctx context.Context) (int, error)
	// RuleGroups returns recording-rule groups (only groups containing at least one recording rule).
	RuleGroups(ctx context.Context) ([]RuleGroup, error)
}
