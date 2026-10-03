// Command pressroom serves the HTML-to-PDF API.
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
	defer renderer.Close()

	srv := &http.Server{
		Addr: ":" + cfg.Port,
		Handler: api.New(api.Deps{
			Renderer:      renderer,
			Token:         cfg.Token,
			MaxBodyBytes:  cfg.MaxBodyBytes,
			RenderTimeout: cfg.RenderTimeout,
			Logger:        logger,
		}),
		MaxHeaderBytes:    16 << 10,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      cfg.RenderTimeout + 10*time.Second,
		IdleTimeout:       60 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.RenderTimeout+5*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			logger.Error("shutdown", "err", err.Error())
		}
	}()

	logger.Info("listening", "port", cfg.Port)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Error("server stopped", "err", err.Error())
		renderer.Close()
		os.Exit(1)
	}
	// ListenAndServe returns as soon as shutdown starts; wait until in-flight
	// renders have finished before the browser is closed.
	<-drained
}
