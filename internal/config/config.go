// Package config reads the service configuration from the environment and
// fails fast when it is unusable.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"
)

// MinTokenLength is the shortest accepted bearer token.
const MinTokenLength = 16

// Config is the validated configuration.
type Config struct {
	Port          string
	Token         string
	LogLevel      slog.Level
	MaxBodyBytes  int64
	RenderTimeout time.Duration
}

// Load reads the configuration through getenv (os.Getenv in production).
func Load(getenv func(string) string) (Config, error) {
	cfg := Config{
		Port:          valueOr(getenv("PORT"), "8080"),
		Token:         getenv("PRESSROOM_TOKEN"),
		MaxBodyBytes:  2 << 20,
		RenderTimeout: 20 * time.Second,
	}

	if cfg.Token == "" {
		return Config{}, errors.New("PRESSROOM_TOKEN is required")
	}
	if len(cfg.Token) < MinTokenLength {
		return Config{}, fmt.Errorf("PRESSROOM_TOKEN must be at least %d characters", MinTokenLength)
	}
	if p, err := strconv.Atoi(cfg.Port); err != nil || p < 1 || p > 65535 {
		return Config{}, fmt.Errorf("PORT %q is not a valid port", cfg.Port)
	}

	level, err := parseLevel(valueOr(getenv("LOG_LEVEL"), "info"))
	if err != nil {
		return Config{}, err
	}
	cfg.LogLevel = level

	if v := getenv("MAX_BODY_BYTES"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 1024 || n > 64<<20 {
			return Config{}, fmt.Errorf("MAX_BODY_BYTES %q must be a whole number from 1024 to 67108864", v)
		}
		cfg.MaxBodyBytes = n
	}
	if v := getenv("RENDER_TIMEOUT"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d < time.Second || d > 2*time.Minute {
			return Config{}, fmt.Errorf("RENDER_TIMEOUT %q must be a duration from 1s to 2m", v)
		}
		cfg.RenderTimeout = d
	}
	return cfg, nil
}

func valueOr(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}

func parseLevel(s string) (slog.Level, error) {
	switch strings.ToLower(s) {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	}
	return 0, fmt.Errorf("LOG_LEVEL %q must be debug, info, warn or error", s)
}
