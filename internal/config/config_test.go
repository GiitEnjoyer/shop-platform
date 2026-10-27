package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSecretPrefersFile(t *testing.T) {
	f := filepath.Join(t.TempDir(), "db")
	if err := os.WriteFile(f, []byte("postgres://from-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DATABASE_URL", "postgres://from-env")
	t.Setenv("DATABASE_URL_FILE", f)
	if got := Secret("DATABASE_URL", "def"); got != "postgres://from-file" {
		t.Fatalf("got %q", got)
	}
}

func TestSecretFallsBackToEnvAndDefault(t *testing.T) {
	t.Setenv("AMQP_URL", "amqp://env")
	if got := Secret("AMQP_URL", "def"); got != "amqp://env" {
		t.Fatalf("got %q", got)
	}
	if got := Secret("MISSING_KEY", "def"); got != "def" {
		t.Fatalf("got %q", got)
	}
}
