package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"mpanel/internal/appconfig"
	"mpanel/internal/auth"
	configmanager "mpanel/internal/config"
	"mpanel/internal/mihomo"
	"mpanel/internal/server"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	cfg, err := appconfig.Load()
	if err != nil {
		logger.Error("configuration error", "error", err)
		os.Exit(1)
	}
	client := mihomo.New(cfg.MihomoAPIURL, cfg.MihomoAPISecret, 5*time.Second)
	manager := &configmanager.Manager{Path: cfg.MihomoConfigPath, Validator: configmanager.BinaryValidator{Binary: cfg.MihomoBinary, ConfigDir: filepath.Dir(cfg.MihomoConfigPath), Timeout: cfg.CommandTimeout}, Reloader: client}
	handler := server.New(auth.New(cfg.Username, cfg.Password, cfg.SessionSecret), manager, logger).Handler()
	httpServer := &http.Server{Addr: cfg.ListenAddr, Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 0, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 1 << 20}
	go func() {
		logger.Info("MPanel listening", "address", cfg.ListenAddr)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("HTTP server failed", "error", err)
			os.Exit(1)
		}
	}()
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(ctx); err != nil {
		logger.Error("graceful shutdown failed", "error", err)
	}
}
