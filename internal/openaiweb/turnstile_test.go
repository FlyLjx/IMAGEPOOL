package openaiweb

import (
	"encoding/base64"
	"encoding/json"
	"testing"
)

func TestSolveTurnstileToken(t *testing.T) {
	const legacyToken = "legacy-requirements-token"
	program := []any{
		[]any{2, 130, "hello"},
		[]any{1, 130, 16},
		[]any{1, 130, 16},
		[]any{2, 120, "window"},
		[]any{2, 121, "Reflect"},
		[]any{24, 122, 120, 121},
		[]any{2, 123, "set"},
		[]any{24, 124, 122, 123},
		[]any{2, 125, "window.Object.create"},
		[]any{17, 126, 125},
		[]any{2, 127, "token"},
		[]any{2, 128, "value"},
		[]any{7, 124, 126, 127, 128},
		[]any{15, 129, 126},
		[]any{20, 129, 129, 3, 129},
	}

	token, err := solveTurnstileToken(turnstileDXForTest(t, legacyToken, program), legacyToken)
	if err != nil {
		t.Fatal(err)
	}
	want := base64.StdEncoding.EncodeToString([]byte(`{"token": "value"}`))
	if token != want {
		t.Fatalf("token=%q want=%q", token, want)
	}
}

func TestSolveTurnstileTokenWithStringMapKeys(t *testing.T) {
	const legacyToken = "legacy-requirements-token"
	program := []any{
		[]any{2, "payload", "hello"},
		[]any{19, "payload"},
		[]any{18, "payload"},
		[]any{20, "payload", "payload", 3, "payload"},
	}
	token, err := solveTurnstileToken(turnstileDXForTest(t, legacyToken, program), legacyToken)
	if err != nil {
		t.Fatal(err)
	}
	if want := base64.StdEncoding.EncodeToString([]byte("hello")); token != want {
		t.Fatalf("token=%q want=%q", token, want)
	}
}

func TestTurnstileHeaders(t *testing.T) {
	client := &Client{}
	requirements := chatRequirements{TurnstileToken: "turnstile-proof"}
	if got := client.imageHeaders(requirements, "", "application/json")["OpenAI-Sentinel-Turnstile-Token"]; got != "turnstile-proof" {
		t.Fatalf("image header=%q", got)
	}
	if got := client.conversationHeaders(requirements)["OpenAI-Sentinel-Turnstile-Token"]; got != "turnstile-proof" {
		t.Fatalf("conversation header=%q", got)
	}
}

func TestParseTurnstileVMOutput(t *testing.T) {
	token, err := parseTurnstileVMOutput([]byte(`{"ok":true,"turnstile":"proof-token"}`))
	if err != nil || token != "proof-token" {
		t.Fatalf("token=%q err=%v", token, err)
	}
	if _, err := parseTurnstileVMOutput([]byte(`{"ok":false,"turnstile_result_decoded_if_error":"unsupported opcode"}`)); err == nil {
		t.Fatal("expected VM failure")
	}
}

func turnstileDXForTest(t *testing.T, p string, program []any) string {
	t.Helper()
	raw, err := json.Marshal(program)
	if err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString([]byte(xorTurnstileString(string(raw), p)))
}
