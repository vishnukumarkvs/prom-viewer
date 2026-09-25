# prom-viewer

`prom-viewer` is a read-only web UI and companion CLI for inspecting a running Prometheus instance. The web UI brings TSDB, WAL, cardinality, resource, runtime, and recording-rule information into one server-rendered page; `promviewerctl` provides scriptable cardinality queries for automation.

The application talks to Prometheus through its HTTP APIs. It does not open the TSDB directory, write to Prometheus, or require a separate database. Templates, CSS, and a vendored copy of [htmx](https://htmx.org/) are embedded into the Go binary, so the deployment has no frontend build step or Node.js runtime.

## Features

- **TSDB head** — series, chunk, label-pair, time-range, and block-count summaries.
- **WAL status** — storage size and current segment, with replay progress while Prometheus is recovering.
- **Resource trends** — one-hour CPU, resident-memory, and head-series graphs queried from Prometheus.
- **Metric cardinality** — top metric names, searchable across the metric names returned by Prometheus, with an exact-name detail view for series count, metadata, and per-label distinct-value counts.
- **Recording rules** — filterable rule groups, evaluation timing, health, source file, and individual recording-rule detail.
- **Runtime information** — build, host, retention, Go runtime, corruption count, and CLI flags.
- **Progressive enhancement** — htmx updates individual sections, while every form also works as a normal `GET` navigation with JavaScript disabled.
- **Command-line cardinality** — `promviewerctl` counts active series per metric or label group and emits table or JSON.

The page is intentionally operational rather than a general Prometheus query UI. Its PromQL expressions are fixed and read-only; there is currently no endpoint for entering arbitrary PromQL.

## Requirements

- Go 1.25 or newer when building from source.
- A Prometheus server reachable from the prom-viewer or promviewerctl process.
- Permission for the Prometheus HTTP endpoints listed in [Prometheus access](#prometheus-access).

The web binary uses the Go standard library. The CLI uses Cobra for command parsing; all Prometheus HTTP and PromQL construction remains in the project. The only non-Go asset is the vendored htmx file in `internal/web/static/`.

## Quick start

### Run from source

```sh
git clone https://github.com/vishnukumarkvs/prom-viewer.git
cd prom-viewer
go run ./cmd/prom-viewer --prometheus.url=http://localhost:9090
```

Open <http://localhost:9099> in a browser. The process writes logs to standard error and exits if the web server cannot start.

To build a reusable binary:

```sh
go build -trimpath -o ./bin/prom-viewer ./cmd/prom-viewer
./bin/prom-viewer --prometheus.url=http://localhost:9090
```

Build the optional CLI alongside it:

```sh
go build -trimpath -o ./bin/promviewerctl ./cmd/promviewerctl
./bin/promviewerctl --prometheus-url http://localhost:9090 \
  cardinality --metric-regex '^envoy_.*'
```

### Run with Docker

Build locally. The default `Dockerfile` is the GoReleaser release image and expects pre-built binaries, so use `Dockerfile.example` for a standalone build:

```sh
docker build -f Dockerfile.example -t prom-viewer:local .
docker run --rm -p 9099:9099 prom-viewer:local \
  --prometheus.url=http://host.docker.internal:9090
```

On Linux, add `--add-host=host.docker.internal:host-gateway` when the Prometheus server is running on the host. If Prometheus is another container, put both containers on the same Docker network and use its service name instead.

A multi-platform image for `linux/amd64` and `linux/arm64` is published to GitHub Container Registry by the release workflow:

```sh
docker pull ghcr.io/vishnukumarkvs/prom-viewer:0.2.2
docker run --rm -p 9099:9099 ghcr.io/vishnukumarkvs/prom-viewer:0.2.2 \
  --prometheus.url=http://prometheus:9090
```

The image uses a minimal Alpine runtime and runs as a non-root user. It includes BusyBox `/bin/sh` for diagnostics, but does not include `bash`. An image built from this checkout contains both `/prom-viewer` (the default entry point) and `/promviewerctl`. Pass application configuration as container arguments or environment variables as described below.

Run the CLI from a locally built image by overriding the entry point:

```sh
docker run --rm --entrypoint /promviewerctl \
  prom-viewer:local \
  --prometheus-url http://prometheus:9090 \
  cardinality --metric-regex '^envoy_.*' --format json
```

### Run beside Prometheus in a Kubernetes pod

When both processes share a pod network namespace, prom-viewer can use `localhost`:

```yaml
containers:
  - name: prom-viewer
    image: ghcr.io/vishnukumarkvs/prom-viewer:0.2.2
    args:
      - --prometheus.url=http://localhost:9090
      - --web.listen-address=:9099
    ports:
      - name: prom-viewer
        containerPort: 9099
```

## Configuration

| Flag | Environment variable | Default | Description |
|---|---|---|---|
| `--prometheus.url` | — | `http://localhost:9090` | Base URL of the Prometheus server. Do not include `/api/v1`. |
| `--web.listen-address` | `PROM_VIEWER_WEB_LISTEN_ADDRESS` | `:9099` | Address for the prom-viewer web server. |

Command-line flags take precedence over the environment variable, which takes precedence over the default. For example:

```sh
PROM_VIEWER_WEB_LISTEN_ADDRESS=127.0.0.1:9099 \
  go run ./cmd/prom-viewer --prometheus.url=http://prometheus:9090
```

The default `:9099` listens on all available interfaces. Use `127.0.0.1:9099` when prom-viewer should only be reachable from the local host. The application has no built-in authentication, authorization, TLS, or reverse-proxy configuration; place it behind an appropriately secured proxy or bind it to a trusted interface when exposing operational data.

## promviewerctl

`promviewerctl` is a small Cobra CLI for running read-only instant queries. The current `cardinality` command builds a Prometheus `count by (...)` query, so “active” means a series visible to Prometheus at the query evaluation time—not every historical series in TSDB storage.

### Count active series per metric

```sh
promviewerctl \
  --prometheus-url http://localhost:9090 \
  cardinality \
  --metric-regex '^envoy_.*'
```

With no `--group-by`, the command groups by `__name__` and prints one row per matching metric:

```text
Total active series: 12345

METRIC                         ACTIVE_SERIES
envoy_cluster_upstream_rq_total 12000
envoy_server_hot_restart      345
```

The metric-name regex is a Prometheus/RE2 regular expression and is fully anchored by Prometheus, so use `^envoy_.*` when you want a prefix match.

### Count by an Envoy label value

Repeat `--label-regex` for additional Prometheus matchers. The flag accepts `=`, `!=`, `=~`, and `!~` operators:

```sh
promviewerctl \
  --prometheus-url http://localhost:9090 \
  cardinality \
  --metric-regex '^envoy_.*' \
  --label-regex 'envoy_cluster_name=~"cluster-a|cluster-b"' \
  --group-by envoy_cluster_name
```

To retain the metric name as well as the label, group by both:

```sh
promviewerctl cardinality \
  --metric-regex '^envoy_.*' \
  --group-by __name__,envoy_cluster_name \
  --label-regex 'envoy_cluster_name=~"cluster-a|cluster-b"'
```

If `--group-by` is omitted, the CLI groups by metric name; label matchers filter those per-metric counts. Use `--group-by <label>` for one row per label value. Multiple group labels can be repeated or comma-separated.

### JSON output and point-in-time queries

Use `--format json` for automation. Successful output is a JSON object containing the generated query, grouping labels, total active-series count, and sorted rows:

```sh
promviewerctl cardinality \
  --prometheus-url http://localhost:9090 \
  --timeout 30s \
  --metric-regex '^envoy_.*' \
  --group-by __name__,envoy_cluster_name \
  --format json
```

The result has this shape:

```json
{
  "query": "count by (__name__, envoy_cluster_name) ({ __name__=~\"^envoy_.*\" })",
  "group_by": ["__name__", "envoy_cluster_name"],
  "total_active_series": 42,
  "rows": [
    {
      "metric": "envoy_cluster_upstream_rq_total",
      "labels": {"envoy_cluster_name": "cluster-a"},
      "active_series": 40
    }
  ]
}
```

Pass `--time 2026-01-02T15:04:05Z` to evaluate the same query at a specific RFC3339 timestamp. The CLI does not currently expose arbitrary PromQL; it is intentionally focused on safe, repeatable cardinality helpers.

| CLI flag | Environment variable | Default | Description |
|---|---|---|---|
| `--prometheus-url` | `PROMVIEWERCTL_PROMETHEUS_URL` | `http://localhost:9090` | Prometheus base URL. |
| `--timeout` | — | `30s` | Context deadline for the instant query; the remote client also has a 30-second ceiling. |

## Prometheus access

The remote source makes only `GET` requests and currently uses these Prometheus endpoints:

```text
/api/v1/status/tsdb
/api/v1/status/tsdb/blocks
/api/v1/status/runtimeinfo
/api/v1/status/buildinfo
/api/v1/status/flags
/api/v1/status/walreplay
/api/v1/query
/api/v1/query_range
/api/v1/metadata
/api/v1/labels
/api/v1/label/{name}/values
/api/v1/label/__name__/values
/api/v1/rules
/metrics
```

The resource and WAL views also depend on these Prometheus self-metrics:

```text
prometheus_tsdb_wal_storage_size_bytes
prometheus_tsdb_wal_segment_current
process_cpu_seconds_total
process_resident_memory_bytes
prometheus_tsdb_head_series
```

Prometheus must be reachable at the configured base URL and must permit the status and query APIs. Sections are fetched independently, so an unavailable or restricted endpoint can leave the rest of the page usable. No authentication headers are generated by prom-viewer or promviewerctl; use a trusted network or an HTTP proxy that adds the required credentials.

A few operations can be expensive on large Prometheus installations:

- The TSDB status request walks the metric-name index. The UI asks for up to 10,000 names for search and filters those results locally.
- The metric count and label-value lookups are performed only for the requested metric, but may involve several API calls.
- The overview issues several independent requests and refreshes all data on each full page load.
- A CLI `count by (...)` query scans the series selected by the metric and label regexes; keep broad patterns bounded on large installations.

## How it works

```text
                         ┌──────────────────────────────┐
Prometheus HTTP API ────►│ internal/source.RemoteSource │
                         └──────────────┬───────────────┘
                                        │ typed data
                         ┌──────────────┴───────────────┐
                         ▼                              ▼
                 internal/web.Handler          internal/cli
                 (html/template + htmx)         (Cobra + table/JSON)
                         │                              │
                         ▼                              ▼
                       browser                       terminal/pipe
```

The source layer is intentionally small: `Source` defines the web data contract, and `RemoteSource` adapts Prometheus HTTP responses to that contract. The web layer owns request parsing, timeouts, query parameters, data assembly, and HTML rendering. The CLI uses the same remote adapter for instant vector queries, while Cobra and its table/JSON output code remain independent of the web templates. This separation leaves room for a future local TSDB source without coupling either frontend to a particular backend.

The overview is rendered at `/`. Four partial endpoints update sections in place when htmx is available:

| Route | Section |
|---|---|
| `/partials/cardinality` | Metric-name top-N and search |
| `/partials/metric-detail` | Exact metric cardinality and metadata |
| `/partials/rules` | Recording-rule groups |
| `/partials/rule-detail` | Recording-rule group detail |

The full-page and partial routes use the same optional query parameters:

| Parameter | Purpose |
|---|---|
| `limit` | Number of top metric-name rows to request |
| `metric` | Case-insensitive metric-name search |
| `detail_metric` | Exact metric name to inspect |
| `rule_search` | Case-insensitive rule-group filter |
| `rule_group` | Rule-group name for the detail view |

All templates and static assets are embedded at compile time. A change to a template, stylesheet, or static JavaScript file therefore requires rebuilding the binary or image.

## Releases

Releases are automated with [GoReleaser](https://goreleaser.com/). Pushing a `v*` tag runs `.github/workflows/release.yml`, which:

- runs the test suite;
- cross-compiles `prom-viewer` and `promviewerctl` for `linux/amd64` and `linux/arm64` with `CGO_ENABLED=0` and `-trimpath`;
- builds a multi-architecture image from `Dockerfile` and pushes it to `ghcr.io/vishnukumarkvs/prom-viewer` as `<version>` (for example `0.2.2`) and `latest`;
- creates a GitHub Release with the per-architecture archives and `checksums.txt`.

The workflow authenticates to GHCR with the automatic `GITHUB_TOKEN`, so no registry secret is needed. A new package is created private, so make it public in the package settings once if the image should be pullable without credentials.

There are two Dockerfiles on purpose:

| File | Used by | Purpose |
|---|---|---|
| `Dockerfile` | GoReleaser | Copies the binaries GoReleaser has already cross-compiled. It cannot be built on its own. |
| `Dockerfile.example` | `docker build -f Dockerfile.example` | Self-contained multi-stage build for local, offline, or non-GoReleaser builds. |

To rehearse a release locally without contacting a registry:

```sh
goreleaser check
goreleaser release --snapshot --clean
```

A snapshot build compiles both binaries, builds a host-architecture image, and writes the artifacts to `dist/`.

## Development

The repository includes focused tests for the Prometheus adapter and the CLI using `httptest` and in-memory vector results. Add or extend tests when changing parsing, request routing, or error handling.

```sh
# Format changed Go files
gofmt -w <changed .go files>

# Compile and test all packages
go test ./...

# Run static analysis
go vet ./...

# Compile the web and CLI packages
go build ./...

# Start the local development server
go run ./cmd/prom-viewer --prometheus.url=http://localhost:9090
```

When changing the application, keep these constraints in mind:

- Keep the data-source interface and the remote adapter separate from the web layer.
- Preserve request contexts and the existing timeouts for upstream calls.
- Use `html/template` and keep user-controlled values in the template context; do not introduce raw HTML rendering.
- Preserve `GET` form fallbacks alongside htmx attributes.
- Do not add write operations or direct TSDB mutation paths.
- Update this README when flags, routes, supported Prometheus endpoints, or operational limitations change.

## Project layout

```text
cmd/prom-viewer/       Web process entry point and flag parsing
cmd/promviewerctl/     CLI process entry point
internal/cli/          Cobra commands, query construction, and output
internal/source/       Source interface, data types, Prometheus HTTP adapter
internal/web/          HTTP handlers, request data, template funcs, and rendering
internal/web/templates/ Server-rendered HTML templates
internal/web/static/   CSS and vendored htmx
Dockerfile             GoReleaser release image; copies pre-built binaries
Dockerfile.example     Self-contained multi-platform build for standalone use
.goreleaser.yaml       Binary, archive, and container release pipeline
.github/workflows/     Tag-driven release workflow
```

## Current limitations

- The shipped backend is remote-only. There is no local `Source` implementation or direct on-disk TSDB access.
- Prometheus's remote `/status/tsdb` response supports the `__name__` breakdown; a global breakdown by arbitrary label name is not available through the current remote API.
- prom-viewer does not provide authentication, authorization, TLS, caching, or a persistent database.
- API availability and response shapes depend on the Prometheus version and its enabled status endpoints.
- `promviewerctl cardinality` reports instant-query results; it does not count historical series outside Prometheus's active lookback window.
- The web UI and CLI are intended for trusted operational networks because they expose Prometheus runtime and TSDB metadata.

## License

[MIT](LICENSE)
