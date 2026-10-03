package auth

import "testing"

func TestWebsocketsEnabledDefaults(t *testing.T) {
	oauth := map[string]string{AttributeAuthKind: AuthKindOAuth}
	cases := []struct {
		name string
		auth *Auth
		want bool
	}{
		{"nil", nil, false},
		{"codex oauth default", &Auth{Provider: "codex", Attributes: oauth}, true},
		{"codex oauth explicit attribute false", &Auth{Provider: "codex", Attributes: map[string]string{AttributeAuthKind: AuthKindOAuth, "websockets": "false"}}, false},
		{"codex oauth explicit metadata false", &Auth{Provider: "codex", Attributes: oauth, Metadata: map[string]any{"websockets": false}}, false},
		{"codex api key default", &Auth{Provider: "codex", Attributes: map[string]string{AttributeAuthKind: AuthKindAPIKey}}, false},
		{"codex unclassified default", &Auth{Provider: "codex"}, false},
		{"claude oauth default", &Auth{Provider: "claude", Attributes: oauth}, false},
		{"api key explicit true", &Auth{Provider: "codex", Attributes: map[string]string{AttributeAuthKind: AuthKindAPIKey, "websockets": "true"}}, true},
	}
	for _, tc := range cases {
		if got := WebsocketsEnabled(tc.auth); got != tc.want {
			t.Errorf("%s: WebsocketsEnabled() = %v, want %v", tc.name, got, tc.want)
		}
	}
}
