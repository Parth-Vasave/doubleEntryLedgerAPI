// Package config loads service configuration from environment variables.
package config

import (
	"fmt"
	"time"

	"github.com/caarlos0/env/v11"
)

// Config holds all runtime settings for the api and worker binaries.
type Config struct {
	DatabaseURL     string        `env:"DATABASE_URL,required"`
	DBMaxConns      int32         `env:"DB_MAX_CONNS" envDefault:"20"`
	HTTPAddr        string        `env:"HTTP_ADDR" envDefault:":8080"`
	LogLevel        string        `env:"LOG_LEVEL" envDefault:"info"`
	AdminAPIKey     string        `env:"ADMIN_API_KEY,required"`
	ShutdownTimeout time.Duration `env:"SHUTDOWN_TIMEOUT" envDefault:"15s"`
	MigrateOnStart  bool          `env:"MIGRATE_ON_START" envDefault:"true"`

	// Rate limiting (per API key, token bucket).
	RateLimitRPS   float64 `env:"RATE_LIMIT_RPS" envDefault:"500"`
	RateLimitBurst int     `env:"RATE_LIMIT_BURST" envDefault:"1000"`

	// Observability.
	OTLPEndpoint string `env:"OTEL_EXPORTER_OTLP_ENDPOINT"`
	ServiceName  string `env:"OTEL_SERVICE_NAME" envDefault:"ledger"`

	// Worker settings.
	WorkerPollInterval time.Duration `env:"WORKER_POLL_INTERVAL" envDefault:"1s"`
	ReconcileInterval  time.Duration `env:"RECONCILE_INTERVAL" envDefault:"1m"`
	IdempotencyKeyTTL  time.Duration `env:"IDEMPOTENCY_KEY_TTL" envDefault:"24h"`
	WebhookTimeout     time.Duration `env:"WEBHOOK_TIMEOUT" envDefault:"5s"`
	WebhookMaxAttempts int           `env:"WEBHOOK_MAX_ATTEMPTS" envDefault:"10"`
	WorkerMetricsAddr  string        `env:"WORKER_METRICS_ADDR" envDefault:":9091"`
	// WebhookAllowPrivate permits delivery to private/loopback addresses.
	// Only for local development; production webhooks must be public.
	WebhookAllowPrivate bool `env:"WEBHOOK_ALLOW_PRIVATE_NETWORKS" envDefault:"false"`
}

// Load parses the environment into a Config.
func Load() (Config, error) {
	var c Config
	if err := env.Parse(&c); err != nil {
		return c, fmt.Errorf("config: %w", err)
	}
	return c, nil
}
