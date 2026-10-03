package cmd

import (
	"bytes"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func TestDoClientConfig(t *testing.T) {
	cfg := &config.Config{Port: 9000}
	cfg.APIKeys = []string{"key-1", "key-2"}
	cfg.TLS.Enable = true

	var codex bytes.Buffer
	if code := DoClientConfig(&codex, cfg, "codex", "box.tailnet.ts.net"); code != 0 {
		t.Fatalf("codex exit = %d", code)
	}
	for _, want := range []string{`base_url = "https://box.tailnet.ts.net:9000/v1"`, `wire_api = "responses"`, "supports_websockets = true", `CLIPROXY_API_KEY="key-1"`, "first of 2 api-keys"} {
		if !strings.Contains(codex.String(), want) {
			t.Errorf("codex output missing %q:\n%s", want, codex.String())
		}
	}

	var claude bytes.Buffer
	if code := DoClientConfig(&claude, &config.Config{}, "claude", ""); code != 0 {
		t.Fatalf("claude exit = %d", code)
	}
	for _, want := range []string{`ANTHROPIC_BASE_URL="http://localhost:8317"`, "<one of your api-keys>", "no api-keys are configured", "-client-host"} {
		if !strings.Contains(claude.String(), want) {
			t.Errorf("claude output missing %q:\n%s", want, claude.String())
		}
	}

	if code := DoClientConfig(&bytes.Buffer{}, cfg, "cursor", ""); code != 2 {
		t.Fatalf("unknown client exit = %d, want 2", code)
	}
}
