package management

import "testing"

func TestNormalizeRoutingStrategyResetSoonest(t *testing.T) {
	for _, input := range []string{"reset-soonest", "resetsoonest", "rs"} {
		got, ok := normalizeRoutingStrategy(input)
		if !ok || got != "reset-soonest" {
			t.Fatalf("normalizeRoutingStrategy(%q) = %q, %v; want reset-soonest, true", input, got, ok)
		}
	}
}
