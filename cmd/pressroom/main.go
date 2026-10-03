// Command pressroom serves the HTML-to-PDF API.
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

	"github.com/tommitoan/pressroom/internal/api"
	"github.com/tommitoan/pressroom/internal/config"
	"github.com/tommitoan/pressroom/internal/render"
)

func main() {
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		slog.New(slog.NewJSONHandler(os.Stderr, nil)).Error("invalid configuration", "err", err.Error())
		os.Exit(1)
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))
	slog.SetDefault(logger)

	renderer := render.NewChromium(render.ChromiumConfig{
		ExecPath:    cfg.ChromePath,
		NoSandbox:   cfg.NoSandbox,
		Concurrency: cfg.Concurrency,
		Queue:       cfg.QueueSize,
		Logger:      logger,
	})

	srv := newServer(":"+cfg.Port, api.New(api.Deps{
		Renderer:      renderer,
		Token:         cfg.Token,
		MaxBodyBytes:  cfg.MaxBodyBytes,
		RenderTimeout: cfg.RenderTimeout,
		Logger:        logger,
	}), cfg.RenderTimeout)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	ln, err := net.Listen("tcp", srv.Addr)
	if err != nil {
		logger.Error("listen failed", "err", err.Error())
		os.Exit(1)
	}

	logger.Info("listening", "port", cfg.Port)
	// The browser is closed only after in-flight renders have drained: the
	// drain may last as long as one render plus a margin.
	err = serve(ctx, srv, ln, cfg.RenderTimeout+5*time.Second)
	renderer.Close()
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Error("server stopped", "err", err.Error())
		os.Exit(1)
	}
}
