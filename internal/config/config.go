// Package config reads 12-factor configuration from the environment.
// Secrets arrive as files rendered by Vault Agent (/vault/secrets/*), see Secret.
package config

import (
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"
)

func String(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}

// Secret reads KEY from the file named by KEY_FILE (Vault Agent renders
// secrets to /vault/secrets/*; images are distroless, so there is no shell
// to `source` an env file), falling back to the KEY variable, then def.
func Secret(key, def string) string {
	if path := os.Getenv(key + "_FILE"); path != "" {
		if b, err := os.ReadFile(path); err == nil {
			return strings.TrimSpace(string(b))
		}
	}
	return String(key, def)
}

func Float(key string, def float64) float64 {
	if v, ok := os.LookupEnv(key); ok {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return def
}

func Duration(key string, def time.Duration) time.Duration {
	if v, ok := os.LookupEnv(key); ok {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}

// Logger returns a JSON logger; LOG_LEVEL=debug|info|warn|error.
func Logger(service, version string) *slog.Logger {
	var level slog.Level
	_ = level.UnmarshalText([]byte(String("LOG_LEVEL", "info")))
	h := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level})
	return slog.New(h).With("service", service, "version", version, "track", String("TRACK", "stable"))
}
