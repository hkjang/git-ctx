package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"git-ctx/internal/app"
	"git-ctx/internal/config"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})))
	cfg, err := config.FromEnv()
	if err != nil {
		slog.Error("invalid bootstrap configuration", "error", err)
		os.Exit(2)
	}
	a, err := app.New(context.Background(), cfg)
	if err != nil {
		slog.Error("startup failed", "error", err)
		os.Exit(1)
	}
	defer a.Close()

	srv := &http.Server{
		Addr:              cfg.ListenAddress,
		Handler:           a.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       90 * time.Second,
	}
	go func() {
		slog.Info("git-ctx listening", "address", cfg.ListenAddress)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("server stopped", "error", err)
			os.Exit(1)
		}
	}()
	stop, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	<-stop.Done()
	ctx, done := context.WithTimeout(context.Background(), 15*time.Second)
	defer done()
	_ = srv.Shutdown(ctx)
}
