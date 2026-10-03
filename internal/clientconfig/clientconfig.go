// Package clientconfig renders the settings that point Claude Code or Codex at this proxy.
package clientconfig

import (
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

const clientConfigKeyPlaceholder = "<one of your api-keys>"

// ErrInvalid marks a client or host that cannot be rendered.
var ErrInvalid = errors.New("invalid client config request")

// Render writes the settings for client ("claude" or "codex") reaching the proxy at host.
func Render(out io.Writer, cfg *config.Config, client, host string) error {
	if !validClientHost(host) {
		return fmt.Errorf("%w: client host %q must be a host name, IPv4 address or [IPv6] address", ErrInvalid, host)
	}
	baseURL, apiKey, notes := clientConfigEndpoint(cfg, host)
	switch strings.ToLower(strings.TrimSpace(client)) {
	case "claude", "claude-code":
		writeClaudeClientConfig(out, baseURL, apiKey)
	case "codex":
		writeCodexClientConfig(out, baseURL, apiKey)
	default:
		return fmt.Errorf("%w: unknown client %q, use claude or codex", ErrInvalid, client)
	}
	for _, note := range notes {
		_, _ = fmt.Fprintln(out, "# Note: "+note)
	}
	return nil
}

// validClientHost keeps the host safe to paste into TOML and shell output.
func validClientHost(host string) bool {
	for _, char := range strings.TrimSpace(host) {
		switch {
		case char >= 'a' && char <= 'z', char >= 'A' && char <= 'Z', char >= '0' && char <= '9':
		case char == '.', char == '-', char == ':', char == '[', char == ']':
		default:
			return false
		}
	}
	return true
}

func clientConfigEndpoint(cfg *config.Config, host string) (string, string, []string) {
	var notes []string
	scheme, port := "http", 8317
	apiKey := clientConfigKeyPlaceholder
	if cfg != nil {
		if cfg.TLS.Enable {
			scheme = "https"
		}
		if cfg.Port > 0 {
			port = cfg.Port
		}
		if len(cfg.APIKeys) > 0 {
			apiKey = cfg.APIKeys[0]
			if len(cfg.APIKeys) > 1 {
				notes = append(notes, fmt.Sprintf("using the first of %d api-keys; any of them works", len(cfg.APIKeys)))
			}
		} else {
			notes = append(notes, "no api-keys are configured; add one to the proxy config and use it here")
		}
	}
	host = strings.TrimSpace(host)
	if host == "" {
		host = "localhost"
	}
	if host == "localhost" || host == "127.0.0.1" || host == "::1" {
		notes = append(notes, "for another machine (for example over Tailscale), use this machine's address as the host (-client-host on the command line, ?host= over HTTP)")
	}
	host = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
	return fmt.Sprintf("%s://%s", scheme, net.JoinHostPort(host, strconv.Itoa(port))), apiKey, notes
}

func writeClaudeClientConfig(out io.Writer, baseURL, apiKey string) {
	_, _ = fmt.Fprintf(out, `# Claude Code -> CLIProxyAPI
# Either export these in your shell profile:
export ANTHROPIC_BASE_URL=%q
export ANTHROPIC_AUTH_TOKEN=%q

# ...or add them to ~/.claude/settings.json:
{
  "env": {
    "ANTHROPIC_BASE_URL": %q,
    "ANTHROPIC_AUTH_TOKEN": %q
  }
}
`, baseURL, apiKey, baseURL, apiKey)
}

func writeCodexClientConfig(out io.Writer, baseURL, apiKey string) {
	_, _ = fmt.Fprintf(out, `# Codex -> CLIProxyAPI
# Add to ~/.codex/config.toml (top-level keys must come before any [table]):
model_provider = "cliproxy"

[model_providers.cliproxy]
name = "CLIProxyAPI"
base_url = "%s/v1"
wire_api = "responses"
env_key = "CLIPROXY_API_KEY"
# Lets Codex keep one websocket per session; the proxy forwards it upstream.
supports_websockets = true

# Then export the key in your shell profile:
export CLIPROXY_API_KEY=%q
`, baseURL, apiKey)
}
