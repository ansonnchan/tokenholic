package fixturecheck

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCommittedFixturesAreSanitized(t *testing.T) {
	t.Parallel()

	if err := checkDir(filepath.Join("..", "..", "fixtures")); err != nil {
		t.Fatal(err)
	}
}

func TestCheckDirRejectsPrivateMaterial(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		data string
	}{
		{name: "prohibited key", data: `{"prompt":"synthetic"}`},
		{name: "camel case private key", data: `{"toolInput":"synthetic"}`},
		{name: "content alias", data: `{"transcript":"synthetic"}`},
		{name: "unknown key", data: `{"unexpectedMetadata":"synthetic"}`},
		{name: "error text beside the error flag", data: `{"isApiErrorMessage":true,"error":"synthetic"}`},
		{name: "unlisted cache lifetime", data: `{"cache_creation":{"ephemeral_24h_input_tokens":1}}`},
		{name: "working directory beside the turn identifier", data: `{"turn_id":"synthetic","cwd":"synthetic"}`},
		{name: "session instructions beside the session identifier", data: `{"session_id":"synthetic","base_instructions":"synthetic"}`},
		{name: "working directory beside the service tier", data: `{"thread_settings":{"service_tier":"synthetic","cwd":"synthetic"}}`},
		{name: "workspace settings beside the service tier", data: `{"thread_settings":{"service_tier":"synthetic","approval_policy":"synthetic"}}`},
		{name: "home path", data: `{"value":"/Users/example/work"}`},
		{name: "root path", data: `{"value":"/root/work"}`},
		{name: "tilde path", data: `{"value":"~/work"}`},
		{name: "email", data: `{"value":"person@example.com"}`},
		{name: "repository remote", data: `{"value":"https://github.com/example/private"}`},
		{name: "SSH repository remote", data: `{"value":"git@example.com:private/repository"}`},
		{name: "credential", data: `{"value":"sk-syntheticsecret"}`},
		{name: "AWS credential", data: `{"value":"AKIA1234567890TEST"}`},
		{name: "JWT credential", data: `{"value":"eyJabcdefgh.abcdefgh.abcdefgh"}`},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, "fixture.json"), []byte(tt.data), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := checkDir(root); err == nil {
				t.Fatal("CheckDir() error = nil, want privacy validation error")
			}
		})
	}
}

func TestCheckDirScansNonJSONFiles(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "fixture.txt"), []byte("git@example.com:private/repository"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := checkDir(root); err == nil || !strings.Contains(err.Error(), "prohibited") {
		t.Fatalf("CheckDir() error = %v, want prohibited material error", err)
	}
}

func TestMalformedFixtureCannotHidePrivateKey(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	data := []byte(`{"prompt":"synthetic"`)
	if err := os.WriteFile(filepath.Join(root, "malformed.jsonl"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := checkDir(root); err == nil {
		t.Fatal("CheckDir() error = nil, want prohibited key error")
	}
}
