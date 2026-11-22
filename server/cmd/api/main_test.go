package main

import (
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	serveraccount "github.com/ansonnchan/tokenholic/server/account"
	servergroup "github.com/ansonnchan/tokenholic/server/group"
	serverleaderboard "github.com/ansonnchan/tokenholic/server/leaderboard"
	"github.com/ansonnchan/tokenholic/server/website"
)

func TestListenAddress(t *testing.T) {
	tests := []struct {
		name    string
		values  map[string]string
		want    string
		wantErr string
	}{
		{name: "local default", want: "127.0.0.1:8080"},
		{name: "platform port", values: map[string]string{"PORT": "10000"}, want: "0.0.0.0:10000"},
		{name: "explicit address wins", values: map[string]string{"TOKENHOLIC_LISTEN_ADDR": "127.0.0.1:9090", "PORT": "10000"}, want: "127.0.0.1:9090"},
		{name: "trims values", values: map[string]string{"PORT": " 8081 "}, want: "0.0.0.0:8081"},
		{name: "zero port", values: map[string]string{"PORT": "0"}, wantErr: "between 1 and 65535"},
		{name: "large port", values: map[string]string{"PORT": "65536"}, wantErr: "between 1 and 65535"},
		{name: "invalid port", values: map[string]string{"PORT": "http"}, wantErr: "between 1 and 65535"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			getenv := func(key string) string { return test.values[key] }
			got, err := listenAddress(getenv)
			if test.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("listenAddress() error = %v, want containing %q", err, test.wantErr)
				}
				return
			}
			if err != nil || got != test.want {
				t.Fatalf("listenAddress() = %q, %v, want %q", got, err, test.want)
			}
		})
	}
}

func TestDecodeStateKey(t *testing.T) {
	raw := []byte("01234567890123456789012345678901")
	for _, encoded := range []string{
		base64.StdEncoding.EncodeToString(raw),
		base64.RawStdEncoding.EncodeToString(raw),
	} {
		key, err := decodeStateKey(encoded)
		if err != nil || string(key) != string(raw) {
			t.Fatalf("decodeStateKey() = %q, %v", key, err)
		}
	}
	if _, err := decodeStateKey("short"); err == nil {
		t.Fatal("decodeStateKey() accepted an invalid key")
	}
}

func TestTrustedProxyHops(t *testing.T) {
	tests := []struct {
		value   string
		want    int
		wantErr bool
	}{
		{value: "", want: 0},
		{value: " 2 ", want: 2},
		{value: "0", want: 0},
		{value: "8", want: 8},
		{value: "9", wantErr: true},
		{value: "-1", wantErr: true},
		{value: "one", wantErr: true},
	}
	for _, test := range tests {
		got, err := trustedProxyHops(func(string) string { return test.value })
		if (err != nil) != test.wantErr || got != test.want {
			t.Errorf("trustedProxyHops(%q) = %d, %v; want %d, error %t", test.value, got, err, test.want, test.wantErr)
		}
	}
}

// The configured limits must refuse a client that keeps starting device
// logins, and must leave the health check alone.
func TestRateLimitedAppliesTheConfiguredLimits(t *testing.T) {
	reached := 0
	handler, err := rateLimited(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		reached++
		response.WriteHeader(http.StatusNoContent)
	}), func(string) string { return "" }, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	var last *httptest.ResponseRecorder
	for attempt := 0; attempt <= deviceStartLimit.Burst; attempt++ {
		last = httptest.NewRecorder()
		handler.ServeHTTP(last, httptest.NewRequest(http.MethodPost, "/v1/auth/device/start", nil))
	}
	if last.Code != http.StatusTooManyRequests || last.Header().Get("Retry-After") == "" || reached != deviceStartLimit.Burst {
		t.Fatalf("login start %d = %d with Retry-After %q after %d reached the route", deviceStartLimit.Burst+1, last.Code, last.Header().Get("Retry-After"), reached)
	}
	for attempt := 0; attempt < perAddressLimit.Burst+10; attempt++ {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/healthz", nil))
		if response.Code != http.StatusNoContent {
			t.Fatalf("health check %d = %d, want it never limited", attempt+1, response.Code)
		}
	}
	if _, err := rateLimited(http.NotFoundHandler(), func(string) string { return "many" }, io.Discard); err == nil {
		t.Fatal("rateLimited() accepted an invalid proxy setting")
	}
}

// The operator reads the proxy chain's length from the log: one line per
// distinct count, and never an address.
func TestForwardingIsReportedOncePerCountWithoutAddresses(t *testing.T) {
	var log strings.Builder
	handler, err := rateLimited(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusNoContent)
	}), func(name string) string {
		if name == "TOKENHOLIC_TRUSTED_PROXY_HOPS" {
			return "1"
		}
		return ""
	}, &log)
	if err != nil {
		t.Fatal(err)
	}
	send := func(path string, forwarded ...string) {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		if len(forwarded) > 0 {
			request.Header.Set("X-Forwarded-For", strings.Join(forwarded, ", "))
		}
		handler.ServeHTTP(httptest.NewRecorder(), request)
	}
	send("/healthz")
	if log.Len() != 0 {
		t.Fatalf("a health probe was reported: %q", log.String())
	}
	for range 3 {
		send("/v1/public/users/synthetic", "203.0.113.7", "198.51.100.9")
	}
	send("/v1/public/users/synthetic", "203.0.113.7")
	long := make([]string, 40)
	for index := range long {
		long[index] = "203.0.113.7"
	}
	send("/v1/public/users/synthetic", long...)
	send("/v1/public/users/synthetic", append(long, "203.0.113.8")...)

	lines := strings.Split(strings.TrimSpace(log.String()), "\n")
	want := []string{
		"a request arrived with 2 X-Forwarded-For entries; TOKENHOLIC_TRUSTED_PROXY_HOPS is 1",
		"a request arrived with 1 X-Forwarded-For entries; TOKENHOLIC_TRUSTED_PROXY_HOPS is 1",
		"a request arrived with more than 8 X-Forwarded-For entries; TOKENHOLIC_TRUSTED_PROXY_HOPS is 1",
	}
	if len(lines) != len(want) {
		t.Fatalf("log = %q, want %d lines", log.String(), len(want))
	}
	for index, line := range lines {
		if !strings.HasSuffix(line, want[index]) {
			t.Errorf("line %d = %q, want it to end %q", index, line, want[index])
		}
	}
	if strings.Contains(log.String(), "203.0.113") || strings.Contains(log.String(), "198.51.100") {
		t.Fatalf("the log holds an address: %q", log.String())
	}
}

func TestWebSiteConfiguration(t *testing.T) {
	site, err := webSite(func(string) string { return "" })
	if err != nil || site.Configured() {
		t.Fatalf("webSite(unset) = %+v, %v; want no site", site, err)
	}
	site, err = webSite(func(string) string { return " https://pages.example/tokenholic/ " })
	if err != nil || site.Origin() != "https://pages.example" || site.Invite("code") != "https://pages.example/tokenholic/invite/#code" {
		t.Fatalf("webSite(project page) = %+v, %v", site, err)
	}
	for _, value := range []string{"http://pages.example", "pages.example", "*", "https://pages.example/?x=1"} {
		if _, err := webSite(func(string) string { return value }); err == nil || !strings.Contains(err.Error(), "TOKENHOLIC_WEB_ORIGIN") {
			t.Errorf("webSite(%q) error = %v, want one naming the variable", value, err)
		}
	}
}

// Only the configured website may read public routes from a browser, and
// with no website nothing may.
func TestReadableByAllowsOnlyTheConfiguredWebsite(t *testing.T) {
	next := http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) { response.WriteHeader(http.StatusNoContent) })
	request := httptest.NewRequest(http.MethodGet, "/v1/public/users/someone", nil)
	request.Header.Set("Origin", "https://pages.example")

	closed, err := readableBy(website.Site{}, next)
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	closed.ServeHTTP(response, request)
	if got := response.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("without a website the API allowed origin %q", got)
	}

	site, err := website.Parse("https://pages.example/tokenholic")
	if err != nil {
		t.Fatal(err)
	}
	open, err := readableBy(site, next)
	if err != nil {
		t.Fatal(err)
	}
	response = httptest.NewRecorder()
	open.ServeHTTP(response, request)
	if got := response.Header().Get("Access-Control-Allow-Origin"); got != "https://pages.example" {
		t.Fatalf("Access-Control-Allow-Origin = %q, want the website's origin without its path", got)
	}
}

type noProfiles struct{}

func (noProfiles) PublicProfile(context.Context, string) (serveraccount.PublicProfile, error) {
	return serveraccount.PublicProfile{}, serveraccount.ErrProfileNotFound
}

type noBoards struct{}

func (noBoards) PublicGroup(context.Context, string, serverleaderboard.Period, serverleaderboard.Metric) (serverleaderboard.Board, error) {
	return serverleaderboard.Board{}, servergroup.ErrNotFound
}

// Share routes exist only when there is a website to send browsers on to,
// and never take over the JSON API's own paths.
func TestWithSharePagesMountsOnlyUnderItsPrefix(t *testing.T) {
	api := http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) { response.WriteHeader(http.StatusTeapot) })
	get := func(handler http.Handler, path string) *httptest.ResponseRecorder {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		return response
	}

	without, err := withSharePages(api, website.Site{}, noProfiles{}, noBoards{})
	if err != nil {
		t.Fatal(err)
	}
	if code := get(without, "/s/u/someone").Code; code != http.StatusTeapot {
		t.Fatalf("without a website /s/ = %d, want it left to the API", code)
	}

	site, err := website.Parse("https://pages.example/tokenholic")
	if err != nil {
		t.Fatal(err)
	}
	with, err := withSharePages(api, site, noProfiles{}, noBoards{})
	if err != nil {
		t.Fatal(err)
	}
	if response := get(with, "/s/u/someone"); response.Code != http.StatusNotFound || !strings.HasPrefix(response.Header().Get("Content-Type"), "text/html") {
		t.Fatalf("share miss = %d %q", response.Code, response.Header().Get("Content-Type"))
	}
	for _, path := range []string{"/healthz", "/v1/public/users/someone", "/v2/sync/buckets", "/", "/sx"} {
		if code := get(with, path).Code; code != http.StatusTeapot {
			t.Errorf("GET %s = %d, want it routed to the API", path, code)
		}
	}
}
