package main

import (
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/GiitEnjoyer/shop-platform/internal/chaos"
	"github.com/GiitEnjoyer/shop-platform/internal/config"
	"github.com/GiitEnjoyer/shop-platform/internal/httpx"
	"github.com/GiitEnjoyer/shop-platform/services/catalog"
)

// version is injected at build time: -ldflags "-X main.version=$CI_COMMIT_SHORT_SHA"
var version = "dev"

func main() {
	log := config.Logger("catalog", version)

	store := catalog.NewStore()
	path := config.String("CATALOG_FILE", "/data/catalog.json")
	f, err := os.Open(path)
	if err != nil {
		log.Error("open catalog", "path", path, "err", err)
		os.Exit(1)
	}
	if err := store.Load(f); err != nil {
		log.Error("load catalog", "err", err)
		os.Exit(1)
	}
	f.Close()
	log.Info("catalog loaded", "products", store.Len())

	srv := httpx.NewServer("catalog", version, config.String("LISTEN_ADDR", ":8080"), log)
	srv.DrainDelay = config.Duration("DRAIN_DELAY", 10*time.Second)

	faults := chaos.Config{
		ErrorRate: config.Float("FAULT_RATE", 0),
		Latency:   config.Duration("FAULT_LATENCY", 0),
	}
	if faults.Enabled() {
		log.Warn("fault injection enabled", "error_rate", faults.ErrorRate, "latency", faults.Latency)
		srv.Wrap = func(m *http.ServeMux) http.Handler { return chaos.Middleware(faults, m) }
	}

	catalog.Register(srv.Mux, store)

	if err := srv.Run(); err != nil {
		log.Error("server", slog.Any("err", err))
		os.Exit(1)
	}
}
