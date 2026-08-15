// Command prom-viewer serves a richer operational view (cardinality, TSDB
// internals, runtime health) of a running Prometheus than its built-in UI.
package main

import (
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"

	"github.com/kvsvishnukumar/prom-viewer/internal/source"
	"github.com/kvsvishnukumar/prom-viewer/internal/web"
)

// envDefault returns the value of the named environment variable, or
// fallback if it's unset. Used so flags can be overridden by env var
// without losing the flag as the higher-precedence source (an explicit
// -web.listen-address on the command line still wins).
func envDefault(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return fallback
}

func main() {
	var (
		prometheusURL = flag.String("prometheus.url", "http://localhost:9090", "Base URL of the Prometheus instance to inspect.")
		listenAddr    = flag.String("web.listen-address", envDefault("PROM_VIEWER_WEB_LISTEN_ADDRESS", ":9099"), "Address to listen on for the prom-viewer UI. Overrides PROM_VIEWER_WEB_LISTEN_ADDRESS if both are set.")
	)
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

	src := source.NewRemoteSource(*prometheusURL)

	handler, err := web.NewHandler(src, logger)
	if err != nil {
		logger.Error("build web handler", "err", err)
		os.Exit(1)
	}

	mux := http.NewServeMux()
	handler.Routes(mux)

	logger.Info("prom-viewer starting", "listen", *listenAddr, "prometheus.url", *prometheusURL)
	if err := http.ListenAndServe(*listenAddr, mux); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
