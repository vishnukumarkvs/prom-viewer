package cli

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/kvsvishnukumar/prom-viewer/internal/source"
	"github.com/spf13/cobra"
)

type rootOptions struct {
	PrometheusURL string
	Timeout       time.Duration
}

// NewRootCommand builds the promviewerctl command tree.
func NewRootCommand() *cobra.Command {
	opts := &rootOptions{}

	cmd := &cobra.Command{
		Use:           "promviewerctl",
		Short:         "Inspect Prometheus time-series cardinality",
		Long:          "promviewerctl runs read-only Prometheus instant queries and reports active time-series counts.",
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	cmd.PersistentFlags().StringVar(&opts.PrometheusURL, "prometheus-url", envDefault("PROMVIEWERCTL_PROMETHEUS_URL", "http://localhost:9090"), "Base URL of the Prometheus server.")
	cmd.PersistentFlags().DurationVar(&opts.Timeout, "timeout", 30*time.Second, "Context deadline for the Prometheus query; the remote client also has a 30-second ceiling.")

	cmd.AddCommand(newCardinalityCommand(opts))
	return cmd
}

func envDefault(key, fallback string) string {
	if value, ok := os.LookupEnv(key); ok {
		return value
	}
	return fallback
}

func (o *rootOptions) validate() error {
	if strings.TrimSpace(o.PrometheusURL) == "" {
		return fmt.Errorf("prometheus URL must not be empty")
	}
	if o.Timeout <= 0 {
		return fmt.Errorf("timeout must be greater than zero")
	}
	return nil
}

func (o *rootOptions) remoteSource() *source.RemoteSource {
	// RemoteSource appends /api/v1 itself. Trim a trailing slash to make the
	// CLI forgiving of URLs copied from a browser or deployment manifest.
	return source.NewRemoteSource(strings.TrimRight(o.PrometheusURL, "/"))
}
