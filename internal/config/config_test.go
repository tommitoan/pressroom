package config

import (
	"log/slog"
	"strings"
	"testing"
	"time"
)

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

const goodToken = "0123456789abcdef"

func TestLoadDefaults(t *testing.T) {
	cfg, err := Load(env(map[string]string{"PRESSROOM_TOKEN": goodToken}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Port != "8080" || cfg.Token != goodToken || cfg.LogLevel != slog.LevelInfo ||
		cfg.MaxBodyBytes != 2<<20 || cfg.RenderTimeout != 20*time.Second ||
		cfg.ChromePath != "" || cfg.NoSandbox || cfg.Concurrency != 2 || cfg.QueueSize != 4 {
		t.Errorf("unexpected defaults: %+v", cfg)
	}
}

func TestLoadOverrides(t *testing.T) {
	cfg, err := Load(env(map[string]string{
		"PRESSROOM_TOKEN": goodToken, "PORT": "9000", "LOG_LEVEL": "DEBUG",
		"MAX_BODY_BYTES": "4096", "RENDER_TIMEOUT": "5s",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Port != "9000" || cfg.LogLevel != slog.LevelDebug || cfg.MaxBodyBytes != 4096 || cfg.RenderTimeout != 5*time.Second {
		t.Errorf("unexpected config: %+v", cfg)
	}
}

func TestLoadEngineSettings(t *testing.T) {
	cfg, err := Load(env(map[string]string{
		"PRESSROOM_TOKEN": goodToken, "CHROME_PATH": "/usr/bin/chromium",
		"CHROMIUM_NO_SANDBOX": "true", "RENDER_CONCURRENCY": "3", "RENDER_QUEUE": "0",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ChromePath != "/usr/bin/chromium" || !cfg.NoSandbox || cfg.Concurrency != 3 || cfg.QueueSize != 0 {
		t.Errorf("unexpected engine settings: %+v", cfg)
	}
}

func TestLoadRejectsBadValues(t *testing.T) {
	for name, tc := range map[string]struct {
		env  map[string]string
		want string
	}{
		"missing token":    {map[string]string{}, "PRESSROOM_TOKEN is required"},
		"short token":      {map[string]string{"PRESSROOM_TOKEN": "short"}, "at least 16"},
		"port not number":  {map[string]string{"PRESSROOM_TOKEN": goodToken, "PORT": "abc"}, "PORT"},
		"port too large":   {map[string]string{"PRESSROOM_TOKEN": goodToken, "PORT": "70000"}, "PORT"},
		"bad log level":    {map[string]string{"PRESSROOM_TOKEN": goodToken, "LOG_LEVEL": "loud"}, "LOG_LEVEL"},
		"body too small":   {map[string]string{"PRESSROOM_TOKEN": goodToken, "MAX_BODY_BYTES": "10"}, "MAX_BODY_BYTES"},
		"body not number":  {map[string]string{"PRESSROOM_TOKEN": goodToken, "MAX_BODY_BYTES": "lots"}, "MAX_BODY_BYTES"},
		"timeout too low":  {map[string]string{"PRESSROOM_TOKEN": goodToken, "RENDER_TIMEOUT": "10ms"}, "RENDER_TIMEOUT"},
		"sandbox invalid":  {map[string]string{"PRESSROOM_TOKEN": goodToken, "CHROMIUM_NO_SANDBOX": "maybe"}, "CHROMIUM_NO_SANDBOX"},
		"concurrency 0":    {map[string]string{"PRESSROOM_TOKEN": goodToken, "RENDER_CONCURRENCY": "0"}, "RENDER_CONCURRENCY"},
		"concurrency big":  {map[string]string{"PRESSROOM_TOKEN": goodToken, "RENDER_CONCURRENCY": "99"}, "RENDER_CONCURRENCY"},
		"queue negative":   {map[string]string{"PRESSROOM_TOKEN": goodToken, "RENDER_QUEUE": "-1"}, "RENDER_QUEUE"},
		"queue not number": {map[string]string{"PRESSROOM_TOKEN": goodToken, "RENDER_QUEUE": "many"}, "RENDER_QUEUE"},
		"timeout invalid":  {map[string]string{"PRESSROOM_TOKEN": goodToken, "RENDER_TIMEOUT": "soon"}, "RENDER_TIMEOUT"},
	} {
		_, err := Load(env(tc.env))
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: error = %v, want it to contain %q", name, err, tc.want)
		}
	}
}

func TestErrorDoesNotEchoTheToken(t *testing.T) {
	_, err := Load(env(map[string]string{"PRESSROOM_TOKEN": "tooshort-secret"}))
	if err == nil || strings.Contains(err.Error(), "tooshort-secret") {
		t.Errorf("error = %v; it must not contain the token", err)
	}
}
