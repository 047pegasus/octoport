// Command server runs the OctoPort control plane: REST API, agent WebSocket
// ingress, the public HTTP/TCP proxy, and the idle-expiry sweeper.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"octoport/control-plane/internal/api"
	"octoport/control-plane/internal/auth"
	"octoport/control-plane/internal/cache"
	"octoport/control-plane/internal/config"
	"octoport/control-plane/internal/db"
	"octoport/control-plane/internal/proxy"
	"octoport/control-plane/internal/tunnel"
)

func main() {
	_ = config.LoadDotEnv(".env")

	cfg, err := config.Load()
	if err != nil {
		slog.Error("config", "err", err)
		os.Exit(1)
	}
	logger := newLogger(cfg.LogLevel)
	slog.SetDefault(logger)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// ---- data plane ----
	database, err := db.Connect(ctx, cfg.DatabaseURL, cfg.DBPoolMax, cfg.DBShards)
	if err != nil {
		logger.Error("database", "err", err)
		os.Exit(1)
	}
	defer database.Close()
	logger.Info("database connected", "pool_max", cfg.DBPoolMax, "shards", cfg.DBShards)

	cacheClient, err := cache.New(ctx, cache.Options{
		Addrs:   cfg.ValkeyAddrs,
		TLS:     cfg.ValkeyTLS,
		IdleTTL: cfg.TunnelIdleTimeout,
	})
	if err != nil {
		logger.Error("cache", "err", err)
		os.Exit(1)
	}
	defer cacheClient.Close()
	logger.Info("cache connected", "addrs", cfg.ValkeyAddrs,
		"eviction_policy", cfg.ValkeyPolicy, "idle_ttl", cfg.TunnelIdleTimeout.String())

	// ---- control plane ----
	authManager := auth.NewManager(cfg.JWTSecret, cfg.JWTTTL, "octoport")
	hub := tunnel.NewHub()

	go hub.Sweep(ctx, cacheClient, database, cfg.TunnelIdleTimeout)

	apiServer := api.New(cfg, database, authManager, cacheClient, hub, logger)
	publicProxy := proxy.New(cfg, cacheClient, hub, logger)

	// SSE: push live stats + lifecycle events to connected GUIs instead of
	// letting clients poll. One shared broadcaster serves all subscribers.
	go apiServer.RunStatsBroadcast(ctx)

	// ---- listeners ----
	var servers []*http.Server
	start := func(name, addr string, h http.Handler) {
		srv := &http.Server{
			Addr:              addr,
			Handler:           h,
			ReadHeaderTimeout: 10 * time.Second,
		}
		servers = append(servers, srv)
		logger.Info("listening", "service", name, "addr", addr)
		go func() {
			if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				logger.Error("server failed", "service", name, "err", err)
			}
		}()
	}

	start("api", cfg.APIAddr, apiServer.Routes())
	start("agents", cfg.AgentWSAddr, apiServer.AgentMux())
	start("public-http", cfg.PublicAddr, publicProxy.Handler())

	var tcpLn net.Listener
	if ln, err := net.Listen("tcp", cfg.PublicTCPAddr); err == nil {
		tcpLn = ln
		logger.Info("listening", "service", "public-tcp", "addr", cfg.PublicTCPAddr)
		go func() {
			if err := publicProxy.ServeTCP(ln); err != nil && !errors.Is(err, net.ErrClosed) {
				logger.Error("tcp proxy failed", "err", err)
			}
		}()
	} else {
		logger.Warn("public tcp proxy disabled", "err", err)
	}

	// ---- graceful shutdown ----
	<-ctx.Done()
	logger.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, srv := range servers {
		_ = srv.Shutdown(shutdownCtx)
	}
	if tcpLn != nil {
		_ = tcpLn.Close()
	}
}

func newLogger(level string) *slog.Logger {
	var lvl slog.Level
	switch level {
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
