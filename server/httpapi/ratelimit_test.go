package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ansonnchan/tokenholic/server/ratelimit"
)

// recordingAllower grants every request and records the keys it was asked
// about; refuse makes it refuse one key with a fixed wait.
type recordingAllower struct {
	keys   []string
	refuse string
	wait   time.Duration
}

func (r *recordingAllower) Allow(key string) (bool, time.Duration) {
	r.keys = append(r.keys, key)
	if key == r.refuse {
		return false, r.wait
	}
	return true, 0
}

func limitedHandler(t *testing.T, limits RateLimits) (http.Handler, *int) {
	t.Helper()
	reached := 0
	handler, err := RateLimited(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		reached++
		response.WriteHeader(http.StatusNoContent)
	}), limits)
	if err != nil {
		t.Fatal(err)
	}
	return handler, &reached
}

func tokenBucket(t *testing.T, burst int, every time.Duration, now func() time.Time) *ratelimit.Limiter {
	t.Helper()
	limiter, err := ratelimit.New(ratelimit.Limit{Burst: burst, Every: every}, 0, now)
	if err != nil {
		t.Fatal(err)
	}
	return limiter
}

func TestRateLimitedReturns429WithRetryAfter(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	generous := func() *ratelimit.Limiter { return tokenBucket(t, 1000, time.Millisecond, clock) }
	tests := []struct {
		name   string
		limits RateLimits
		// send issues the request under test; it is repeated until refused.
		method, path, credential string
		wantGranted              int
		wantRetryAfter           string
	}{
		{
			name:   "per address",
			limits: RateLimits{PerAddress: tokenBucket(t, 2, 90*time.Second, clock), PerCredential: generous(), DeviceStart: generous()},
			method: http.MethodGet, path: "/v1/groups", credential: "device-credential",
			wantGranted: 2, wantRetryAfter: "90",
		},
		{
			name:   "per credential",
			limits: RateLimits{PerAddress: generous(), PerCredential: tokenBucket(t, 3, 1500*time.Millisecond, clock), DeviceStart: generous()},
			method: http.MethodPost, path: "/v2/sync/buckets", credential: "device-credential",
			// A wait of 1.5 seconds rounds up: retrying after 1 would fail again.
			wantGranted: 3, wantRetryAfter: "2",
		},
		{
			name:   "device login start",
			limits: RateLimits{PerAddress: generous(), PerCredential: generous(), DeviceStart: tokenBucket(t, 1, 6*time.Second, clock)},
			method: http.MethodPost, path: "/v1/auth/device/start",
			wantGranted: 1, wantRetryAfter: "6",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler, reached := limitedHandler(t, tt.limits)
			var refused *httptest.ResponseRecorder
			for attempt := 0; attempt <= tt.wantGranted; attempt++ {
				request := httptest.NewRequest(tt.method, tt.path, nil)
				if tt.credential != "" {
					request.Header.Set("Authorization", "Bearer "+tt.credential)
				}
				response := httptest.NewRecorder()
				handler.ServeHTTP(response, request)
				if attempt < tt.wantGranted && response.Code != http.StatusNoContent {
					t.Fatalf("request %d = %d, want it granted", attempt+1, response.Code)
				}
				refused = response
			}
			if refused.Code != http.StatusTooManyRequests || refused.Header().Get("Retry-After") != tt.wantRetryAfter {
				t.Fatalf("request over the limit = %d with Retry-After %q, want 429 with %q", refused.Code, refused.Header().Get("Retry-After"), tt.wantRetryAfter)
			}
			if *reached != tt.wantGranted {
				t.Fatalf("%d requests reached the route, want %d: a refused request must not", *reached, tt.wantGranted)
			}
			if body := refused.Body.String(); !strings.Contains(body, `"error":"rate_limited"`) || refused.Header().Get("Content-Type") != "application/json" ||
				refused.Header().Get("Cache-Control") != "no-store" || refused.Header().Get("X-Content-Type-Options") != "nosniff" {
				t.Fatalf("429 response = %q with headers %v", body, refused.Header())
			}
		})
	}
}

func TestRateLimitedKeysAndExemptions(t *testing.T) {
	t.Parallel()
	address, credential, start := &recordingAllower{}, &recordingAllower{}, &recordingAllower{}
	handler, reached := limitedHandler(t, RateLimits{PerAddress: address, PerCredential: credential, DeviceStart: start})
	send := func(method, path, bearer string) {
		request := httptest.NewRequest(method, path, nil)
		request.RemoteAddr = "203.0.113.7:4444"
		if bearer != "" {
			request.Header.Set("Authorization", "Bearer "+bearer)
		}
		handler.ServeHTTP(httptest.NewRecorder(), request)
	}
	send(http.MethodGet, "/healthz", "")
	if len(address.keys)+len(credential.keys)+len(start.keys) != 0 || *reached != 1 {
		t.Fatalf("the health check was limited: %v %v %v", address.keys, credential.keys, start.keys)
	}
	send(http.MethodGet, "/v1/groups", "secret-device-credential")
	send(http.MethodGet, "/v1/groups", "another-credential")
	send(http.MethodPost, "/v1/auth/device/start", "")
	send(http.MethodGet, "/v1/auth/device/start", "") // not the login route
	if got := strings.Join(address.keys, ","); got != "203.0.113.7,203.0.113.7,203.0.113.7,203.0.113.7" {
		t.Fatalf("address keys = %s", got)
	}
	if len(start.keys) != 1 || start.keys[0] != "203.0.113.7" {
		t.Fatalf("device-start keys = %v, want the one POST", start.keys)
	}
	if len(credential.keys) != 2 || credential.keys[0] == credential.keys[1] {
		t.Fatalf("credential keys = %v, want one distinct key per credential", credential.keys)
	}
	for _, key := range credential.keys {
		if len(key) != 64 || strings.Contains(key, "credential") {
			t.Fatalf("credential key %q is not a digest", key)
		}
	}

	// A refused credential does not stop other credentials from the same address.
	credential.refuse, credential.wait = credential.keys[0], time.Second
	before := *reached
	send(http.MethodGet, "/v1/groups", "secret-device-credential")
	send(http.MethodGet, "/v1/groups", "another-credential")
	if *reached != before+1 {
		t.Fatalf("%d requests reached the route after one credential was refused, want 1", *reached-before)
	}
}

func TestClientAddress(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		remote    string
		forwarded []string
		hops      int
		want      string
	}{
		{name: "direct connection", remote: "203.0.113.7:4444", want: "203.0.113.7"},
		{name: "forwarded header ignored without a trusted proxy", remote: "203.0.113.7:4444", forwarded: []string{"198.51.100.1"}, want: "203.0.113.7"},
		{name: "one trusted proxy", remote: "10.0.0.1:80", forwarded: []string{"198.51.100.1"}, hops: 1, want: "198.51.100.1"},
		{name: "client-supplied entries are to the left", remote: "10.0.0.1:80", forwarded: []string{"192.0.2.99, 198.51.100.1"}, hops: 1, want: "198.51.100.1"},
		{name: "two trusted proxies", remote: "10.0.0.1:80", forwarded: []string{"192.0.2.99, 198.51.100.1, 10.0.0.2"}, hops: 2, want: "198.51.100.1"},
		{name: "repeated headers form one list", remote: "10.0.0.1:80", forwarded: []string{"192.0.2.99", "198.51.100.1 , 10.0.0.2"}, hops: 2, want: "198.51.100.1"},
		{name: "header shorter than the trusted chain", remote: "10.0.0.1:80", forwarded: []string{"198.51.100.1"}, hops: 2, want: "10.0.0.1"},
		{name: "missing header", remote: "10.0.0.1:80", hops: 1, want: "10.0.0.1"},
		{name: "entry that is not an address", remote: "10.0.0.1:80", forwarded: []string{"unknown"}, hops: 1, want: "10.0.0.1"},
		{name: "IPv6 client is keyed by its /64", remote: "[2001:db8:1:2:aaaa:bbbb:cccc:dddd]:4444", want: "2001:db8:1:2::/64"},
		{name: "another host in the same /64", remote: "[2001:db8:1:2::1]:4444", want: "2001:db8:1:2::/64"},
		{name: "forwarded IPv6", remote: "10.0.0.1:80", forwarded: []string{"2001:db8:9:9::5"}, hops: 1, want: "2001:db8:9:9::/64"},
		{name: "IPv4 carried in IPv6", remote: "[::ffff:203.0.113.7]:4444", want: "203.0.113.7"},
		{name: "address without a port", remote: "203.0.113.7", want: "203.0.113.7"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/v1/groups", nil)
			request.RemoteAddr = tt.remote
			for _, value := range tt.forwarded {
				request.Header.Add("X-Forwarded-For", value)
			}
			if got := clientAddress(request, tt.hops); got != tt.want {
				t.Fatalf("clientAddress() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRateLimitedRequiresCompleteConfiguration(t *testing.T) {
	t.Parallel()
	allow := &recordingAllower{}
	next := http.NotFoundHandler()
	complete := RateLimits{PerAddress: allow, PerCredential: allow, DeviceStart: allow}
	if _, err := RateLimited(next, complete); err != nil {
		t.Fatal(err)
	}
	invalid := map[string]func() (http.Handler, RateLimits){
		"no handler":            func() (http.Handler, RateLimits) { return nil, complete },
		"no address limiter":    func() (http.Handler, RateLimits) { l := complete; l.PerAddress = nil; return next, l },
		"no credential limiter": func() (http.Handler, RateLimits) { l := complete; l.PerCredential = nil; return next, l },
		"no login limiter":      func() (http.Handler, RateLimits) { l := complete; l.DeviceStart = nil; return next, l },
		"negative proxy hops":   func() (http.Handler, RateLimits) { l := complete; l.TrustedProxyHops = -1; return next, l },
		"too many proxy hops": func() (http.Handler, RateLimits) {
			l := complete
			l.TrustedProxyHops = MaxTrustedProxyHops + 1
			return next, l
		},
	}
	for name, build := range invalid {
		handler, limits := build()
		if _, err := RateLimited(handler, limits); err == nil {
			t.Errorf("%s: RateLimited() error = nil", name)
		}
	}
}
