package cliproxy

import (
	"testing"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestResetSoonestRoutingSelector(t *testing.T) {
	for _, input := range []string{"reset-soonest", "ResetSoonest", "rs"} {
		state := normalizedRoutingRuntimeState(&internalconfig.Config{
			Routing: internalconfig.RoutingConfig{Strategy: input},
		})
		if state.strategy != "reset-soonest" {
			t.Fatalf("strategy(%q) = %q, want reset-soonest", input, state.strategy)
		}
		if _, ok := newRoutingSelector(state).(*coreauth.ResetSoonestSelector); !ok {
			t.Fatalf("selector type = %T, want *auth.ResetSoonestSelector", newRoutingSelector(state))
		}
	}
}
