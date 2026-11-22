package httpapi

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"
)

const testWebOrigin = "https://tokenholic.example"

func publicAPI(t *testing.T) http.Handler {
	t.Helper()
	api, err := New(&fakeAuthService{}, &fakeUsageSyncService{}, &fakeAccountUsageService{}, &fakeGroupService{}, &fakeLeaderboardService{})
	if err != nil {
		t.Fatal(err)
	}
	handler, err := AllowWebOrigin(api, testWebOrigin)
	if err != nil {
		t.Fatal(err)
	}
	return handler
}

func getFrom(handler http.Handler, path, origin string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodGet, path, nil)
	if origin != "" {
		request.Header.Set("Origin", origin)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func TestAllowWebOriginNamesOnlyTheWebsiteOnPublicRoutes(t *testing.T) {
	t.Parallel()
	handler := publicAPI(t)
	tests := []struct {
		name, path, origin string
		allowed            bool
	}{
		{"the website reads a profile", "/v1/public/users/public-member", testWebOrigin, true},
		{"the website reads a board", "/v1/public/groups/g-publicab/leaderboard", testWebOrigin, true},
		{"the website previews an invite", "/v1/public/invites/ovt_valid", testWebOrigin, true},
		{"the website reads a miss", "/v1/public/users/nobody", testWebOrigin, true},
		{"another site", "/v1/public/users/public-member", "https://elsewhere.example", false},
		{"a lookalike host", "/v1/public/users/public-member", testWebOrigin + ".evil.example", false},
		{"plain HTTP on the same host", "/v1/public/users/public-member", "http://tokenholic.example", false},
		{"the opaque origin", "/v1/public/users/public-member", "null", false},
		{"no origin at all", "/v1/public/users/public-member", "", false},
		{"an authenticated route", "/v1/account/usage", testWebOrigin, false},
		{"the global board", "/v1/global/leaderboard", testWebOrigin, false},
		{"the health check", "/healthz", testWebOrigin, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			header := getFrom(handler, test.path, test.origin).Header()
			allow := header.Get("Access-Control-Allow-Origin")
			if test.allowed && allow != testWebOrigin || !test.allowed && allow != "" {
				t.Fatalf("Access-Control-Allow-Origin = %q, allowed = %t", allow, test.allowed)
			}
			if allow == "*" || header.Get("Access-Control-Allow-Credentials") != "" {
				t.Fatalf("response opens the route to any origin or to credentials: %v", header)
			}
			public := len(test.path) > len(publicPrefix) && test.path[:len(publicPrefix)] == publicPrefix
			if varies := header.Get("Vary") == "Origin"; varies != public {
				t.Fatalf("Vary = %q on a public route = %t", header.Get("Vary"), public)
			}
		})
	}
}

// The allowlist must not give a private record a different answer from a
// missing one, for the website or for anyone else.
func TestAllowWebOriginKeepsEveryMissIdentical(t *testing.T) {
	t.Parallel()
	handler := publicAPI(t)
	misses := []string{
		"/v1/public/users/private-member", "/v1/public/users/nobody",
		"/v1/public/groups/g-privatea/leaderboard", "/v1/public/groups/g-nothere/leaderboard",
		"/v1/public/invites/ovt_expired", "/v1/public/invites/unknown",
	}
	for _, origin := range []string{testWebOrigin, "https://elsewhere.example", ""} {
		reference := getFrom(handler, misses[0], origin)
		if reference.Code != http.StatusNotFound {
			t.Fatalf("miss from %q = %d", origin, reference.Code)
		}
		for _, path := range misses[1:] {
			response := getFrom(handler, path, origin)
			if response.Code != reference.Code || response.Body.String() != reference.Body.String() || !reflect.DeepEqual(response.Header(), reference.Header()) {
				t.Fatalf("GET %s from %q = %d %q %v, want exactly the reference miss %v", path, origin, response.Code, response.Body.String(), response.Header(), reference.Header())
			}
		}
		// A hit carries the same cross-origin headers as a miss, so the
		// headers say nothing about whether a record is public.
		hit := getFrom(handler, "/v1/public/users/public-member", origin)
		for _, name := range []string{"Access-Control-Allow-Origin", "Access-Control-Expose-Headers", "Vary"} {
			if !reflect.DeepEqual(hit.Header().Values(name), reference.Header().Values(name)) {
				t.Fatalf("%s differs between a hit and a miss from %q: %v, %v", name, origin, hit.Header().Values(name), reference.Header().Values(name))
			}
		}
	}
}

// A refused request must still be readable by the website, with the wait.
func TestAllowWebOriginCoversRateLimitedResponses(t *testing.T) {
	t.Parallel()
	limited, _ := limitedHandler(t, RateLimits{
		PerAddress:    &recordingAllower{refuse: "192.0.2.1", wait: 12 * time.Second},
		PerCredential: &recordingAllower{}, DeviceStart: &recordingAllower{},
	})
	handler, err := AllowWebOrigin(limited, testWebOrigin)
	if err != nil {
		t.Fatal(err)
	}
	response := getFrom(handler, "/v1/public/users/public-member", testWebOrigin)
	if response.Code != http.StatusTooManyRequests || response.Header().Get("Access-Control-Allow-Origin") != testWebOrigin ||
		response.Header().Get("Access-Control-Expose-Headers") != "Retry-After" || response.Header().Get("Retry-After") != "12" {
		t.Fatalf("rate-limited response = %d %v", response.Code, response.Header())
	}
}

func TestAllowWebOriginRejectsAnythingButOneHTTPSOrigin(t *testing.T) {
	t.Parallel()
	for _, origin := range []string{
		"", "*", "https://*", "https://*.tokenholic.example", "http://tokenholic.example", "tokenholic.example",
		"https://tokenholic.example/", "https://tokenholic.example/site", "https://tokenholic.example?x=1", "null",
	} {
		if _, err := AllowWebOrigin(http.NotFoundHandler(), origin); err == nil {
			t.Errorf("AllowWebOrigin(%q) error = nil", origin)
		}
	}
	if _, err := AllowWebOrigin(nil, testWebOrigin); err == nil {
		t.Error("AllowWebOrigin(nil handler) error = nil")
	}
}
