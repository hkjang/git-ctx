package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"git-ctx/internal/app"
	"git-ctx/internal/config"
	runtimelogging "git-ctx/internal/logging"
	"git-ctx/internal/recovery"
	"git-ctx/internal/version"
)

func main() {
	// -version 은 DB 없이도 답해야 합니다. 배포 스크립트와 운영자가 "이 바이너리가
	// 무슨 빌드인지" 를 확인하는 가장 짧은 경로입니다.
	if len(os.Args) > 1 && (os.Args[1] == "-version" || os.Args[1] == "--version" || os.Args[1] == "version") {
		fmt.Println(version.Full())
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "recovery-token" {
		if err := runRecoveryToken(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "recovery-token:", err)
			os.Exit(2)
		}
		return
	}
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
		// 업그레이드 후 "정말 새 빌드가 도는가" 를 로그 한 줄로 확인할 수 있어야 합니다.
		slog.Info("git-ctx listening", "address", httpConfig.ListenAddress, "version", version.Version, "build", version.Full())
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

func runRecoveryToken(args []string) error {
	flags := flag.NewFlagSet("recovery-token", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	ttl := flags.Duration("ttl", 15*time.Minute, "one-time token validity (1m..1h)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	cfg, err := config.FromEnv()
	if err != nil {
		return err
	}
	token, expires, err := recovery.Generate(cfg.RecoveryKey, *ttl, time.Now().UTC())
	if err != nil {
		return err
	}
	fmt.Fprintln(os.Stdout, token)
	fmt.Fprintf(os.Stderr, "One-time recovery token expires at %s. Open /admin?recovery=1 and enter it once.\n", expires.Format(time.RFC3339))
	return nil
}
