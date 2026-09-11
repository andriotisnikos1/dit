// Command dit-server is the dit backend: it owns the SQLite database, the
// registry credentials, the check scheduler and notification delivery.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/andriotisnikos1/dit"
	"github.com/andriotisnikos1/dit/internal/apitypes"
	"github.com/andriotisnikos1/dit/internal/check"
	"github.com/andriotisnikos1/dit/internal/config"
	"github.com/andriotisnikos1/dit/internal/crypto"
	"github.com/andriotisnikos1/dit/internal/notify"
	"github.com/andriotisnikos1/dit/internal/registry"
	"github.com/andriotisnikos1/dit/internal/server"
	"github.com/andriotisnikos1/dit/internal/store"
)

// Version is stamped at build time with -ldflags.
var Version = "dev"

func main() {
	os.Exit(run())
}

func run() int {
	var (
		configPath  string
		listen      string
		dataDir     string
		logLevel    string
		showVersion bool
		migrateOnly bool
		healthCheck bool
	)

	flag.StringVar(&configPath, "config", "", "path to the YAML config file (env DIT_CONFIG)")
	flag.StringVar(&listen, "listen", "", "address to listen on, e.g. :8080 (env DIT_LISTEN)")
	flag.StringVar(&dataDir, "data-dir", "", "directory for the database and master key (env DIT_DATA_DIR)")
	flag.StringVar(&logLevel, "log-level", "", "log level: debug, info, warn, error (env DIT_LOG_LEVEL)")
	flag.BoolVar(&showVersion, "version", false, "print the version and exit")
	flag.BoolVar(&migrateOnly, "migrate", false, "apply migrations and exit")
	flag.BoolVar(&healthCheck, "healthcheck", false,
		"probe the local /healthz endpoint and exit 0 when healthy (for container health checks)")
	flag.Parse()

	if showVersion {
		fmt.Println("dit-server", Version)
		return 0
	}

	// A container health check runs this same binary because the runtime image
	// is distroless: there is no shell, curl or wget to call.
	if healthCheck {
		return runHealthCheck(listen, configPath)
	}

	cfg, err := config.LoadServer(configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "dit-server: %v\n", err)
		return 1
	}
	// Flags override both the file and the environment.
	if listen != "" {
		cfg.Listen = listen
	}
	if dataDir != "" {
		cfg.DataDir = dataDir
	}
	if logLevel != "" {
		cfg.LogLevel = strings.ToLower(logLevel)
	}

	logger := newLogger(cfg.LogLevel)
	slog.SetDefault(logger)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Master key: environment, then file, then generate one.
	keys, err := crypto.ResolveMasterKey(cfg.MasterKey, cfg.MasterKeyFile, cfg.DataDir)
	if err != nil {
		logger.Error("resolve master key", "error", err)
		return 1
	}
	if keys.Generated {
		logger.Warn("GENERATED A NEW MASTER KEY — existing secrets cannot be decrypted with it",
			"path", keys.Path,
			"advice", "set DIT_MASTER_KEY explicitly and back it up; losing this key makes stored registry credentials and channel secrets unrecoverable")
	}
	logger.Info("master key resolved", "source", keys.Source, "path", keys.Path)

	db, err := store.Open(ctx, store.Options{
		Path:       cfg.DBPath,
		Sealer:     keys.Sealer,
		Migrations: dit.Migrations,
	})
	if err != nil {
		logger.Error("open database", "path", cfg.DBPath, "error", err)
		return 1
	}
	defer db.Close()
	logger.Info("database ready", "path", db.Path())

	if migrateOnly {
		logger.Info("migrations applied; exiting because --migrate was given")
		return 0
	}

	engine, err := check.New(check.Options{
		Store:          db,
		Registry:       registry.NewRemote(),
		Notifier:       notify.DefaultBuilder{},
		Logger:         logger,
		Interval:       cfg.CheckInterval.Std(),
		Concurrency:    cfg.CheckConcurrency,
		Timeout:        cfg.CheckTimeout.Std(),
		NotifyAttempts: cfg.NotifyAttempts,
		NotifyBackoff:  cfg.NotifyBackoff.Std(),
	})
	if err != nil {
		logger.Error("build check engine", "error", err)
		return 1
	}

	srv, err := server.New(server.Options{
		Config:  cfg,
		Store:   db,
		Engine:  engine,
		Logger:  logger,
		Version: Version,
	})
	if err != nil {
		logger.Error("build server", "error", err)
		return 1
	}

	httpServer := &http.Server{
		Addr:              cfg.Listen,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	engineErr := make(chan error, 1)
	go func() {
		engineErr <- engine.Run(ctx)
	}()

	serveErr := make(chan error, 1)
	go func() {
		logger.Info("dit-server listening", "addr", cfg.Listen, "version", Version)
		logger.Info("configuration", "effective", cfg.Redacted())
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
			return
		}
		serveErr <- nil
	}()

	select {
	case err := <-serveErr:
		if err != nil {
			logger.Error("http server stopped", "error", err)
			return 1
		}
	case err := <-engineErr:
		if err != nil && !errors.Is(err, context.Canceled) {
			logger.Error("check engine stopped", "error", err)
			return 1
		}
	case <-ctx.Done():
		logger.Info("shutdown signal received")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		logger.Warn("graceful shutdown failed", "error", err)
	}
	logger.Info("dit-server stopped")
	return 0
}

// runHealthCheck probes the server's own /healthz endpoint. It is what the
// container health check runs, because the distroless runtime image has no
// shell, curl or wget.
func runHealthCheck(listenFlag, configPath string) int {
	// Resolve the address the same way the server does, without requiring a
	// full configuration (the API token may not be readable from here).
	addr := strings.TrimSpace(listenFlag)
	if addr == "" {
		addr = strings.TrimSpace(os.Getenv(config.EnvListen))
	}
	if addr == "" {
		addr = config.DefaultListen
	}

	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "dit-server: cannot parse listen address %q: %v\n", addr, err)
		return 1
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	url := fmt.Sprintf("http://%s/healthz", net.JoinHostPort(host, port))

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		fmt.Fprintf(os.Stderr, "dit-server: health check against %s failed: %v\n", url, err)
		return 1
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "dit-server: health check returned %s\n", resp.Status)
		return 1
	}

	var health apitypes.Health
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&health); err != nil {
		fmt.Fprintf(os.Stderr, "dit-server: health check response was not JSON: %v\n", err)
		return 1
	}
	if health.Status != "ok" {
		fmt.Fprintf(os.Stderr, "dit-server: health status is %q, want ok\n", health.Status)
		return 1
	}
	fmt.Printf("ok (version %s)\n", health.Version)
	return 0
}

func newLogger(level string) *slog.Logger {
	var lvl slog.Level
	switch strings.ToLower(level) {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: lvl}))
}
