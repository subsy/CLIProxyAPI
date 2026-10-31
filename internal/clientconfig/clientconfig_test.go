package clientconfig

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func TestRender(t *testing.T) {
	cfg := &config.Config{Port: 9000}
	cfg.APIKeys = []string{"key-1", "key-2"}
	cfg.TLS.Enable = true

	var codex bytes.Buffer
	if err := Render(&codex, cfg, "codex", "box.tailnet.ts.net"); err != nil {
		t.Fatalf("codex: %v", err)
	}
	for _, want := range []string{`base_url = "https://box.tailnet.ts.net:9000/v1"`, `wire_api = "responses"`, "supports_websockets = true", `CLIPROXY_API_KEY="key-1"`, "first of 2 api-keys"} {
		if !strings.Contains(codex.String(), want) {
			t.Errorf("codex output missing %q:\n%s", want, codex.String())
		}
	}

	var claude bytes.Buffer
	if err := Render(&claude, &config.Config{}, "claude", ""); err != nil {
		t.Fatalf("claude: %v", err)
	}
	for _, want := range []string{`ANTHROPIC_BASE_URL="http://localhost:8317"`, "<one of your api-keys>", "no api-keys are configured", "-client-host"} {
		if !strings.Contains(claude.String(), want) {
			t.Errorf("claude output missing %q:\n%s", want, claude.String())
		}
	}

	if err := Render(&bytes.Buffer{}, cfg, "cursor", ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown client error = %v", err)
	}
	if err := Render(&bytes.Buffer{}, cfg, "codex", "evil\"\nmodel = \"x"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("host with quotes and newlines error = %v", err)
	}
	for _, host := range []string{"[fd7a::1]", "fd7a::1"} {
		var ipv6 bytes.Buffer
		if err := Render(&ipv6, cfg, "codex", host); err != nil || !strings.Contains(ipv6.String(), `base_url = "https://[fd7a::1]:9000/v1"`) {
			t.Fatalf("IPv6 host %q: %v, output:\n%s", host, err, ipv6.String())
		}
	}
}

func TestRenderPicksKeyByPolicy(t *testing.T) {
	cfg := &config.Config{}
	cfg.APIKeys = []string{"claude-key", "codex-key"}
	cfg.APIKeyPolicies = []config.APIKeyPolicy{{APIKey: "codex-key", AllowedProviders: []string{"codex"}}}

	var codex, claude bytes.Buffer
	if err := Render(&codex, cfg, "codex", "box"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(codex.String(), `CLIPROXY_API_KEY="codex-key"`) {
		t.Errorf("codex should get the key restricted to codex:\n%s", codex.String())
	}
	if err := Render(&claude, cfg, "claude", "box"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(claude.String(), "claude-key") || strings.Contains(claude.String(), "codex-key") {
		t.Errorf("claude must get the unrestricted key, never the codex-only one:\n%s", claude.String())
	}

	cfg.APIKeys = []string{"codex-key"}
	var none bytes.Buffer
	if err := Render(&none, cfg, "claude", "box"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(none.String(), "codex-key") || !strings.Contains(none.String(), "no api-key may use the claude provider") {
		t.Errorf("with only a codex-only key, claude must get the placeholder and a note:\n%s", none.String())
	}
}
