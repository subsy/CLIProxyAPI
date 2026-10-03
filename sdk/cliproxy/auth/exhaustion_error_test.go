package auth

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestModelCooldownErrorSaysWhenNextCredentialIsBack(t *testing.T) {
	err := newModelCooldownError("claude-opus-5-5", "claude", 4*time.Minute)
	var body struct {
		Error struct {
			Message     string `json:"message"`
			AvailableAt string `json:"available_at"`
		} `json:"error"`
	}
	if errDecode := json.Unmarshal([]byte(err.Error()), &body); errDecode != nil {
		t.Fatalf("decode %q: %v", err.Error(), errDecode)
	}
	if !strings.Contains(body.Error.Message, "the next one is available in 4m0s (at ") || !strings.HasSuffix(body.Error.Message, "UTC)") {
		t.Fatalf("message = %q", body.Error.Message)
	}
	if _, errParse := time.Parse(time.RFC3339, body.Error.AvailableAt); errParse != nil {
		t.Fatalf("available_at = %q: %v", body.Error.AvailableAt, errParse)
	}
}

func TestAuthUnavailableErrorSaysWhenNextCredentialIsBack(t *testing.T) {
	now := time.Now()
	err := newAuthUnavailableError(now.Add(90*time.Second), now)
	if !strings.Contains(err.Error(), "the next one is back in 1m30s") {
		t.Fatalf("message = %q", err.Error())
	}
	if plain := newAuthUnavailableError(time.Time{}, now); !strings.HasSuffix(plain.Error(), "no auth available") {
		t.Fatalf("message without a recovery time = %q", plain.Error())
	}
}
