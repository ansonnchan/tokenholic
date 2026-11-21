package auth

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestGitHubClientDeviceFlowAndIdentity(t *testing.T) {
	var tokenPolls int
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		status := http.StatusOK
		body := ""
		switch request.URL.Path {
		case "/device":
			if request.FormValue("client_id") != "client-id" {
				t.Fatalf("device client ID = %q", request.FormValue("client_id"))
			}
			body = `{"device_code":"device-secret","user_code":"ABCD","verification_uri":"https://github.com/login/device","expires_in":900,"interval":5}`
		case "/token":
			tokenPolls++
			if tokenPolls == 1 {
				body = `{"error":"authorization_pending"}`
				break
			}
			body = `{"access_token":"provider-token","token_type":"bearer"}`
		case "/user":
			if request.Header.Get("Authorization") != "Bearer provider-token" || request.Header.Get("X-GitHub-Api-Version") == "" {
				t.Fatalf("user headers = %#v", request.Header)
			}
			body = `{"id":101,"login":"synthetic-user","name":"Synthetic","avatar_url":"https://avatars.githubusercontent.com/u/101?v=4"}`
		default:
			status = http.StatusNotFound
		}
		return &http.Response{
			StatusCode: status, Header: make(http.Header),
			Body: io.NopCloser(strings.NewReader(body)), Request: request,
		}, nil
	})
	client, err := NewGitHubClient(&http.Client{Transport: transport}, "client-id")
	if err != nil {
		t.Fatal(err)
	}
	client.deviceURL, client.tokenURL, client.userURL = "https://github.test/device", "https://github.test/token", "https://github.test/user"
	client.now = func() time.Time { return time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC) }
	started, err := client.Start(t.Context())
	if err != nil || started.DeviceCode != "device-secret" || started.Interval != 5*time.Second {
		t.Fatalf("Start() = %#v, %v", started, err)
	}
	pending, err := client.Poll(t.Context(), started.DeviceCode)
	if err != nil || pending.Status != FlowPending {
		t.Fatalf("pending Poll() = %#v, %v", pending, err)
	}
	approved, err := client.Poll(t.Context(), started.DeviceCode)
	if err != nil || approved.Status != FlowApproved {
		t.Fatalf("approved Poll() = %#v, %v", approved, err)
	}
	user, err := client.User(t.Context(), approved.AccessToken)
	if err != nil || user.ID != 101 || user.Login != "synthetic-user" || user.AvatarURL != "https://avatars.githubusercontent.com/u/101?v=4" {
		t.Fatalf("User() = %#v, %v", user, err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func TestAvatarURLAcceptsOnlyGitHubsAvatarHost(t *testing.T) {
	tests := []struct {
		name  string
		value string
		keep  bool
	}{
		{name: "GitHub avatar", value: "https://avatars.githubusercontent.com/u/1?v=4", keep: true},
		{name: "empty", value: ""},
		{name: "plain HTTP", value: "http://avatars.githubusercontent.com/u/1"},
		{name: "another host", value: "https://images.example.test/u/1.png"},
		{name: "look-alike host", value: "https://avatars.githubusercontent.com.example.test/u/1"},
		{name: "host in the path", value: "https://example.test/avatars.githubusercontent.com/u/1"},
		{name: "credentials in the URL", value: "https://user@avatars.githubusercontent.com/u/1"},
		{name: "script URL", value: "javascript:alert(1)"},
		{name: "data URL", value: "data:image/png;base64,AAAA"},
		{name: "fragment", value: "https://avatars.githubusercontent.com/u/1#x"},
		{name: "too long", value: "https://avatars.githubusercontent.com/u/" + strings.Repeat("1", 500)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := AvatarURL(test.value)
			if test.keep && got != test.value || !test.keep && got != "" {
				t.Fatalf("AvatarURL(%q) = %q, keep %t", test.value, got, test.keep)
			}
		})
	}
}
