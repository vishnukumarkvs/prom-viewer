# prom-viewer

`prom-viewer` is a read-only web UI for inspecting a running Prometheus instance. It brings TSDB, WAL, cardinality, resource, runtime, and recording-rule information into one server-rendered page.

The application talks to Prometheus through its HTTP APIs. It does not open the TSDB directory, write to Prometheus, or require a separate database. Templates, CSS, and a vendored copy of [htmx](https://htmx.org/) are embedded into the Go binary, so the deployment has no frontend build step or Node.js runtime.

## Features

- **TSDB head** — series, chunk, label-pair, time-range, and block-count summaries.
- **WAL status** — storage size and current segment, with replay progress while Prometheus is recovering.
- **Resource trends** — one-hour CPU, resident-memory, and head-series graphs queried from Prometheus.
- **Metric cardinality** — top metric names, searchable across the metric names returned by Prometheus, with an exact-name detail view for series count, metadata, and per-label distinct-value counts.
- **Recording rules** — filterable rule groups, evaluation timing, health, source file, and individual recording-rule detail.
- **Runtime information** — build, host, retention, Go runtime, corruption count, and CLI flags.
- **Progressive enhancement** — htmx updates individual sections, while every form also works as a normal `GET` navigation with JavaScript disabled.

The page is intentionally operational rather than a general Prometheus query UI. Its PromQL expressions are fixed and read-only; there is currently no endpoint for entering arbitrary PromQL.

## Requirements

- Go 1.25 or newer when building from source.
- A Prometheus server reachable from the prom-viewer process.
- Permission for the Prometheus HTTP endpoints listed in [Prometheus access](#prometheus-access).

There are no third-party Go dependencies at present. The only non-Go asset is the vendored htmx file in `internal/web/static/`.

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

### Run with Docker

Build locally:

```sh
docker build -t prom-viewer:local .
docker run --rm -p 9099:9099 prom-viewer:local \
  --prometheus.url=http://host.docker.internal:9090
```

On Linux, add `--add-host=host.docker.internal:host-gateway` when the Prometheus server is running on the host. If Prometheus is another container, put both containers on the same Docker network and use its service name instead.

A multi-platform image is published under `vishnukumarkvs/prom-viewer` for `linux/amd64` and `linux/arm64`:

```sh
docker pull vishnukumarkvs/prom-viewer:0.1.0
docker run --rm -p 9099:9099 vishnukumarkvs/prom-viewer:0.1.0 \
  --prometheus.url=http://prometheus:9090
```

The image uses a non-root distroless runtime and has no shell. Pass application configuration as container arguments or environment variables as described below.

### Run beside Prometheus in a Kubernetes pod

When both processes share a pod network namespace, prom-viewer can use `localhost`:

```yaml
containers:
  - name: prom-viewer
    image: docker.io/vishnukumarkvs/prom-viewer:0.1.0
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

Prometheus must be reachable at the configured base URL and must permit the status and query APIs. Sections are fetched independently, so an unavailable or restricted endpoint can leave the rest of the page usable. No authentication headers are generated by prom-viewer; use a trusted network or an HTTP proxy that adds the required credentials.

A few operations can be expensive on large Prometheus installations:

- The TSDB status request walks the metric-name index. The UI asks for up to 10,000 names for search and filters those results locally.
- The metric count and label-value lookups are performed only for the requested metric, but may involve several API calls.
- The overview issues several independent requests and refreshes all data on each full page load.

## How it works

```text
Prometheus HTTP API
        │
        ▼
internal/source.RemoteSource
        │  typed operational data
        ▼
internal/web.Handler
        │  html/template + htmx partials
        ▼
browser
```

The source layer is intentionally small: `Source` defines the data contract, and `RemoteSource` adapts Prometheus HTTP responses to that contract. The web layer owns request parsing, timeouts, query parameters, data assembly, and HTML rendering. This separation leaves room for a future local TSDB source without coupling the templates to a particular backend.

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

## Development

The repository has no test files yet, so `go test ./...` currently performs a compile check. Add focused tests when changing parsing, request routing, or error handling.

```sh
# Format changed Go files
gofmt -w <changed .go files>

# Compile and test all packages
go test ./...

# Run static analysis
go vet ./...

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
cmd/prom-viewer/       CLI entry point and flag parsing
internal/source/       Source interface, data types, Prometheus HTTP adapter
internal/web/          HTTP handlers, request data, template funcs, and rendering
internal/web/templates/ Server-rendered HTML templates
internal/web/static/   CSS and vendored htmx
Dockerfile             Multi-stage, multi-platform image build
```

## Current limitations

- The shipped backend is remote-only. There is no local `Source` implementation or direct on-disk TSDB access.
- Prometheus's remote `/status/tsdb` response supports the `__name__` breakdown; a global breakdown by arbitrary label name is not available through the current remote API.
- prom-viewer does not provide authentication, authorization, TLS, caching, or a persistent database.
- API availability and response shapes depend on the Prometheus version and its enabled status endpoints.
- The application is intended for trusted operational networks because it exposes Prometheus runtime and TSDB metadata.

## License

[MIT](LICENSE)
