package cmd

import (
	"fmt"
	"io"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

const clientConfigKeyPlaceholder = "<one of your api-keys>"

// DoClientConfig prints the settings that point a coding agent at this proxy.
// It returns a process exit code.
func DoClientConfig(out io.Writer, cfg *config.Config, client, host string) int {
	baseURL, apiKey, notes := clientConfigEndpoint(cfg, host)
	switch strings.ToLower(strings.TrimSpace(client)) {
	case "claude", "claude-code":
		writeClaudeClientConfig(out, baseURL, apiKey)
	case "codex":
		writeCodexClientConfig(out, baseURL, apiKey)
	default:
		_, _ = fmt.Fprintf(out, "unknown client %q: use claude or codex\n", client)
		return 2
	}
	for _, note := range notes {
		_, _ = fmt.Fprintln(out, "# Note: "+note)
	}
	return 0
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
	if host == "localhost" || host == "127.0.0.1" {
		notes = append(notes, "for another machine (for example over Tailscale), rerun with -client-host <this machine's address>")
	}
	return fmt.Sprintf("%s://%s:%d", scheme, host, port), apiKey, notes
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
