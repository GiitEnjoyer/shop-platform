package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/GiitEnjoyer/shop-platform/internal/chaos"
	"github.com/GiitEnjoyer/shop-platform/internal/config"
	"github.com/GiitEnjoyer/shop-platform/internal/httpx"
	"github.com/GiitEnjoyer/shop-platform/services/orders"
)

var version = "dev"

func newID() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return "ord_" + hex.EncodeToString(b)
}

func main() {
	migrate := flag.Bool("migrate", false, "apply database migrations and exit (Helm pre-upgrade hook)")
	flag.Parse()

	log := config.Logger("orders", version)

	// In Kubernetes DATABASE_URL_FILE / AMQP_URL_FILE point to files rendered by Vault Agent.
	store, err := orders.OpenPostgres(config.Secret("DATABASE_URL", "postgres://orders:orders@localhost:5432/orders?sslmode=disable"))
	if err != nil {
		log.Error("open db", "err", err)
		os.Exit(1)
	}
	defer store.Close()

	if *migrate {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		if err := store.Migrate(ctx); err != nil {
			log.Error("migration failed", "err", err)
			os.Exit(1)
		}
		log.Info("migrations applied")
		return
	}

	pub := orders.NewAMQPPublisher(config.Secret("AMQP_URL", "amqp://guest:guest@localhost:5672/"), "shop.events")
	defer pub.Close()

	srv := httpx.NewServer("orders", version, config.String("LISTEN_ADDR", ":8080"), log)
	srv.DrainDelay = config.Duration("DRAIN_DELAY", 10*time.Second)
	srv.AddCheck("postgres", store.Ping)
	srv.AddCheck("rabbitmq", pub.Ping)

	faults := chaos.Config{
		ErrorRate: config.Float("FAULT_RATE", 0),
		Latency:   config.Duration("FAULT_LATENCY", 0),
	}
	if faults.Enabled() {
		log.Warn("fault injection enabled", "error_rate", faults.ErrorRate, "latency", faults.Latency)
		srv.Wrap = func(m *http.ServeMux) http.Handler { return chaos.Middleware(faults, m) }
	}

	orders.NewService(store, pub, srv.Registry, newID).Register(srv.Mux)

	if err := srv.Run(); err != nil {
		log.Error("server", slog.Any("err", err))
		os.Exit(1)
	}
}
