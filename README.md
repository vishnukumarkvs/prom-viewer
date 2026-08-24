# prom-viewer

### Much Better TSDB page

A richer operational view of a running Prometheus than its built-in `/tsdb-status`
page — cardinality, WAL, and TSDB internals, queried live over Prometheus's own
HTTP API. Ships as a single static Go binary: no database, no build step, no
Node — the UI is plain CSS plus a vendored [htmx](https://htmx.org) for
partial-page updates, embedded straight into the binary.

## Features

- **TSDB head snapshot** — series/chunk/label-pair counts, head time range,
  persisted block count.
- **WAL status** — current size and segment, plus a live replay progress bar
  on startup (hidden once replay is done, instead of a table of zeroes).
- **Cardinality by metric name** — top-N series count by metric, adjustable
  limit, with live-as-you-type search across up to 10,000 metric names (the
  same cost as fetching 10 — Prometheus's postings-index walk scans
  everything regardless of the limit requested).
- **Cardinality of a single metric** — look up any metric by exact name to
  see its series count, type/help/unit metadata, which jobs expose it, and a
  per-label breakdown of distinct values (i.e. which label is driving the
  cardinality).
- **Runtime health** — build info, runtime info, and CLI flags in one place,
  instead of scattered across separate pages.

Everything is read-only against Prometheus's public `/api/v1/*` HTTP API —
prom-viewer never touches the on-disk TSDB directly, so it works against any
remote Prometheus (colocated or not) and carries none of the API-stability
risk of importing Prometheus's internal `tsdb` Go package.

## Usage

```
go run ./cmd/prom-viewer --prometheus.url=http://localhost:9090
```

Then open http://localhost:9099.

### Flags / environment variables

| Flag                  | Env var                          | Default                 | Description                          |
|------------------------|-----------------------------------|--------------------------|---------------------------------------|
| `--prometheus.url`     | —                                  | `http://localhost:9090` | Base URL of the Prometheus instance to inspect. |
| `--web.listen-address` | `PROM_VIEWER_WEB_LISTEN_ADDRESS` | `:9099`                  | Address prom-viewer's own UI listens on. |

An explicit `--web.listen-address` flag always wins over the env var. The
default port is `9099` — deliberately outside Prometheus's own [reserved
port range](https://github.com/prometheus/prometheus/wiki/Default-port-allocations)
(`9090` Prometheus, `9091` Pushgateway, `9093` Alertmanager, `9100` Node
Exporter, etc.) to avoid colliding with anything else in a typical
Prometheus deployment.

## Docker

```
docker build -t prom-viewer .
docker run -p 9099:9099 prom-viewer --prometheus.url=http://prometheus:9090
```

Multi-arch image (`linux/amd64`, `linux/arm64`) is published as `vishnukumarkvs/prom-viewer:0.1.0` and `latest` — `docker pull` fetches only your platform (~4–5 MiB compressed; registry stores ~8–9 MiB total). Built with cross-compilation to avoid QEMU (`golang:1.25` segfault):

```
docker buildx build --platform linux/amd64,linux/arm64 -t vishnukumarkvs/prom-viewer:0.1.0 --push .
```

## Running as a sidecar

Since prom-viewer talks to Prometheus over `localhost`, it can run as a
second container in the same pod as your Prometheus server, sharing its
network namespace — no separate Service or network hop needed:

```yaml
- name: prom-viewer
  image: docker.io/vishnukumarkvs/prom-viewer:latest
  args:
    - --prometheus.url=http://localhost:9090
    - --web.listen-address=:9099
  ports:
    - name: prom-viewer
      containerPort: 9099
```

## Architecture

- `internal/source` — the `Source` interface and its `RemoteSource`
  implementation (HTTP client over `/api/v1/*`). A future `LocalSource`
  (direct, read-only `tsdb.OpenDBReadOnly` access) is planned for
  block/chunk-level detail the HTTP API doesn't expose.
- `internal/web` — server-rendered HTML (`html/template`) plus two
  htmx-swappable partials (metric-name search, single-metric cardinality
  lookup) that update in place without a full page reload. Every form still
  has a plain `method="get"` fallback, so the page is fully functional with
  JavaScript disabled.
- `cmd/prom-viewer` — CLI entry point.


## License

[MIT](LICENSE)
