# AGENTS.md

This file contains repository-specific guidance for coding agents and contributors working in `prom-viewer`. It applies to the entire repository unless a more specific `AGENTS.md` is added in a subdirectory.

## Project profile

- **Purpose:** a small, read-only web UI and companion CLI for inspecting a running Prometheus server.
- **Language:** Go 1.25 or newer.
- **Module:** `github.com/kvsvishnukumar/prom-viewer`.
- **Entry points:** `cmd/prom-viewer` for the web process and `cmd/promviewerctl` for the CLI.
- **Data source:** Prometheus HTTP API through `internal/source.RemoteSource`.
- **Web layer:** `net/http` handlers, `html/template`, htmx partials, and embedded CSS/JavaScript.
- **CLI layer:** Cobra command tree, PromQL cardinality query construction, and table/JSON output.
- **Dependencies:** the server uses the Go standard library; the CLI uses Cobra. Keep the dependency footprint small unless a change has a clear justification.
- **Deployment model:** static web/CLI binaries or a small non-root Docker image. There is no database or frontend build toolchain.

Read `README.md` for the user-facing behavior, supported endpoints, configuration, and current limitations before making a change.

## Repository layout

```text
cmd/prom-viewer/main.go       Web process entry point, flags, logger, and server startup
cmd/promviewerctl/main.go     CLI process entry point
internal/cli/                 Cobra commands, query construction, parsing, and output
internal/source/source.go     Source interface and internal data types
internal/source/remote.go     Prometheus HTTP API adapter
internal/web/web.go           Routes, request parsing, timeouts, and data assembly
internal/web/templates/       Server-rendered HTML templates
internal/web/static/          CSS and vendored htmx
Dockerfile                    GoReleaser release image; copies pre-built binaries
Dockerfile.example            Self-contained multi-platform build for standalone use
.goreleaser.yaml              Binary, archive, and container release pipeline
.github/workflows/release.yml Tag-driven release workflow
```

The main dependency directions are:

```text
cmd/prom-viewer  -> internal/web    -> internal/source
cmd/promviewerctl -> internal/cli   -> internal/source
```

`internal/web` must not import Prometheus implementation packages or reach into a TSDB directory. Keep backend-specific HTTP details in `internal/source`.

## Working agreements

1. Keep changes focused. Do not rewrite unrelated code, generated/vendor content, or user changes found in the working tree.
2. Read the surrounding code and templates before changing a contract or UI section.
3. Prefer the existing standard-library patterns over introducing a framework; the CLI's Cobra dependency is an explicit exception for command parsing.
4. Update documentation when changing flags, environment variables, routes, Prometheus endpoints, or user-visible behavior.
5. Do not commit, delete, or overwrite files merely to make a diff look cleaner. If a change requires a migration, call it out explicitly.

## Go conventions

- Run `gofmt` on every changed Go file.
- Use clear, receiver-appropriate names and keep comments useful for exported Go identifiers.
- Pass `context.Context` through source calls and preserve cancellation. Do not replace request contexts with `context.Background()` inside handlers or adapters.
- Wrap upstream errors with enough context to identify the Prometheus request, while preserving the original error with `%w` when callers may need to inspect it.
- Return typed data from the source layer; do not pass raw Prometheus JSON or HTTP response objects into templates.
- Keep time conversions explicit. Prometheus timestamps are handled as Unix milliseconds or float seconds in different APIs; preserve UTC behavior used by the UI.
- Keep maps and other user-visible data in a deterministic order. For example, CLI flags are sorted before rendering.
- If a new method is added to `Source`, update every implementation, the compile-time interface assertion, callers, and focused tests. Currently `RemoteSource` is the only implementation.
- Do not change the module path as incidental cleanup. The Git remote and `go.mod` module path currently differ; changing either is a repository-identity migration and requires coordinated import and documentation updates.

## Source and API contracts

The current remote backend deliberately has these properties:

- `--prometheus.url` is the base server URL, not a URL ending in `/api/v1`. `RemoteSource` appends `/api/v1` itself, so avoid double prefixes.
- Requests are `GET` requests and the application is read-only. Do not add mutating Prometheus operations or direct TSDB writes.
- The standard Prometheus response envelope must be checked for both HTTP success and `status: "success"`.
- The web UI's remote cardinality breakdown by arbitrary label names is not supported: Prometheus's remote `/status/tsdb` breakdown is treated as `__name__`-only. The CLI's PromQL-based grouping is a separate capability; preserve the web limitation unless the API and UI contract are deliberately extended.
- Metric names are passed into PromQL selectors. Escape PromQL string literals before interpolating them, and use `url.Values` for query parameters.
- The source currently uses a 30-second `http.Client` timeout. Handler-level deadlines are intentionally shorter for overview and resource requests. Do not remove or weaken them without understanding the fan-out of upstream calls.
- Resource graphs are fixed one-hour PromQL queries with a 30-second step and currently render the first returned series. Do not present them as arbitrary query support.
- `RemoteSource.Query` and `QueryAt` execute instant queries and return typed vector samples. The CLI's `cardinality` command uses `count by (...)`; keep its “active” semantics tied to the instant-query evaluation time.
- A source method returning an empty slice or zero value for “no data” should be kept distinct from a transport/API error where the interface documents that distinction.

When adding a Prometheus API call:

1. Prefer the existing `get` helper when the response is a standard JSON envelope.
2. Handle non-200 responses, decode failures, and `status != "success"` distinctly enough for useful logs.
3. Bound or deliberately document expensive requests such as metric-name enumeration.
4. Add tests with `httptest.Server`; unit tests should not require a live Prometheus instance.

## HTTP and web-layer contracts

The current routes are:

```text
GET /                         Full overview page
GET /static/                  Embedded CSS and JavaScript
GET /partials/cardinality     Top metric names and metric search
GET /partials/metric-detail   Exact metric cardinality and metadata
GET /partials/rules           Recording-rule groups and filter
GET /partials/rule-detail     Recording-rule group detail
```

Supported query parameters are `limit`, `metric`, `detail_metric`, `rule_search`, and `rule_group`. Preserve their meaning and URL behavior when changing handlers or templates.

- Keep the full `GET /` fallback working when JavaScript is disabled.
- htmx endpoints may render fragments, but the corresponding form must continue to target `/` for a normal navigation.
- Keep `HX-Push-Url` behavior and bookmarkable query strings unless the UX is intentionally changing.
- Set appropriate content types and use `http.Error` for handler-level failures.
- Prefer small, testable fetch functions over embedding upstream API details directly in templates.
- Preserve the existing per-request timeouts and per-section error isolation where practical. A failed optional section should not take down unrelated sections.
- Do not add authentication assumptions silently. The current server has no auth, authorization, or TLS layer; any such feature needs explicit configuration, documentation, and security review.

## CLI conventions

`cmd/promviewerctl` is a separate binary built with Cobra. Keep the entry point thin and put command behavior in `internal/cli` so it can be tested without a live Prometheus.

- The `cardinality` command is intentionally an instant-query helper, not an arbitrary PromQL shell.
- `--metric-regex` matches `__name__`; `--label-regex` is repeatable and accepts `=`, `!=`, `=~`, and `!~` matchers; `--group-by` controls aggregation labels.
- Prometheus requires a vector selector to have at least one matcher that cannot match the empty string, so `.*` is invalid as a sole matcher. `buildCardinalityQuery` enforces this locally via `matchesEmptyLabel` and points the user at `.+`. Keep that check in step with `buildCardinalityQuery`'s selector construction.
- Escape user-provided metric names, regexes, and label values before building PromQL. Validate regexes and label names locally when possible.
- Treat “active” as the result of a Prometheus instant query. Use `--time` only when a reproducible RFC3339 evaluation time is needed.
- Keep table output human-readable and JSON output stable, sorted, and machine-friendly. Preserve the total and row-level count fields when extending the schema.
- Keep runtime errors on stderr and successful output on stdout. Do not print progress or debug text to stdout because JSON must remain pipeable.

## Template and asset guidance

- Use `html/template`, not `text/template`, for HTML output.
- Keep all user-controlled values in template data and let `html/template` perform contextual escaping. Avoid `template.HTML` or hand-built markup for input-derived content.
- Keep semantic HTML, keyboard-accessible controls, and the existing no-JavaScript behavior.
- Keep the CSS and templates free of a Node/npm build step.
- Treat `internal/web/static/htmx.min.js` as vendored third-party code. Do not hand-edit it as part of an application change; update it deliberately and retain its licensing/attribution requirements.
- Template and static files are embedded with `go:embed`. Changes to them are not visible to a running binary until it is rebuilt.
- When adding a partial, define the same template name and data shape expected by both the full-page template and the handler, then verify the fragment and no-JavaScript fallback.

## Development and validation commands

Use these commands from the repository root:

```sh
# Compile and run all package tests
go test ./...

# Run static analysis
go vet ./...

# Build without leaving a binary in the repository
go build ./...

# Run against a local Prometheus
go run ./cmd/prom-viewer --prometheus.url=http://localhost:9090

# Build the container image without GoReleaser
docker build -f Dockerfile.example -t prom-viewer:local .

# Validate the release configuration and rehearse a release
goreleaser check
goreleaser release --snapshot --clean
```

The CLI and source packages have focused unit tests using `httptest` and in-memory vectors; these tests do not require a live Prometheus. A change that affects parsing, request routing, template behavior, or source compatibility should extend those tests rather than relying only on compilation.

Useful test boundaries:

- Use an `httptest.Server` to test `RemoteSource` response decoding, API errors, and malformed responses.
- Use a fake `Source` implementation to test web handlers and query-parameter behavior without a Prometheus dependency.
- Test template construction through `web.NewHandler` so template parse errors are caught.
- Test empty metric/rule data separately from upstream failures.
- Run `gofmt`, `go vet ./...`, and `go test ./...` after implementation changes.

## Release and Docker guidance

Releases are tag-driven through GoReleaser. `.github/workflows/release.yml` runs `goreleaser release` when a `v*` tag is pushed. It authenticates to GHCR with the automatic `GITHUB_TOKEN`, so the workflow needs no registry secret. Preserve:

- `CGO_ENABLED=0` and `-trimpath` in both `builds` entries;
- the minimal non-root Alpine runtime image;
- both `/prom-viewer` and `/promviewerctl` in the image;
- the web binary as the default entry point and the CLI available via `--entrypoint`;
- the `linux/amd64` and `linux/arm64` platforms.

The runtime stage runs `apk add`, so the arm64 image build needs QEMU. Keep the `docker/setup-qemu-action` step in the release workflow unless the runtime stage is made `RUN`-free.

There are two Dockerfiles and the distinction is load-bearing:

- `Dockerfile` is the GoReleaser image. It copies binaries that GoReleaser already cross-compiled from `$TARGETPLATFORM`, and cannot be built standalone. Do not add a builder stage or a compiler to it.
- `Dockerfile.example` is the self-contained two-stage `CGO_ENABLED=0` build that cross-compiles via `GOOS`/`GOARCH`. It is what `docker build -f Dockerfile.example .` uses and what anyone building without GoReleaser should use.

If you change runtime contents in `Dockerfile`, mirror the change in `Dockerfile.example` so both images stay equivalent. Verify with `goreleaser release --snapshot --clean`, and confirm the image still works for both `linux/amd64` and `linux/arm64`.

## Definition of done

Before handing off a change, verify that:

- the behavior matches the README and the relevant template/source contracts;
- changed Go files are formatted;
- `go vet ./...` and `go test ./...` pass, or any failure is explained;
- the application builds with the Go version in `go.mod`;
- `goreleaser check` passes, and `goreleaser release --snapshot --clean` succeeds if `Dockerfile`, `.goreleaser.yaml`, or the binaries changed;
- embedded assets and partial routes were checked together;
- no write path, secret, or unsafe template rendering was introduced;
- documentation is updated for user-visible changes;
- unrelated files and the user's existing work remain untouched.
