package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"git-ctx/internal/app"
	"git-ctx/internal/config"
	runtimelogging "git-ctx/internal/logging"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: &runtimelogging.Level})))
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

	httpConfig := a.HTTPServerConfig(context.Background())
	srv := &http.Server{
		Addr:              httpConfig.ListenAddress,
		Handler:           a.Handler(),
		ReadHeaderTimeout: httpConfig.ReadHeaderTimeout,
		ReadTimeout:       httpConfig.ReadTimeout,
		WriteTimeout:      httpConfig.WriteTimeout,
		IdleTimeout:       httpConfig.IdleTimeout,
	}
	go func() {
		slog.Info("git-ctx listening", "address", httpConfig.ListenAddress)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("server stopped", "error", err)
			os.Exit(1)
		}
	}()
	stop, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	<-stop.Done()
	ctx, done := context.WithTimeout(context.Background(), httpConfig.ShutdownTimeout)
	defer done()
	_ = srv.Shutdown(ctx)
}
