package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"time"
)

// MaxTrustedProxyHops bounds the proxy chain the API can be told to trust.
const MaxTrustedProxyHops = 8

// Allower grants or refuses one request for a key and, when it refuses, says
// how long until the next request would be granted.
type Allower interface {
	Allow(key string) (allowed bool, retryAfter time.Duration)
}

// RateLimits configures the limits applied before any route runs.
//
// PerAddress limits every request by client address. PerCredential limits
// requests that present a bearer credential, whether or not it is valid, so a
// leaked or looping credential is contained wherever it is used from.
// DeviceStart is a stricter per-address limit on starting a device login,
// the one unauthenticated route that calls GitHub on the caller's behalf.
//
// TrustedProxyHops is how many reverse proxies in front of the API append to
// X-Forwarded-For. Zero trusts none and uses the connection's own address.
// A wrong value either lets a client choose its own address (too high) or
// puts every client behind the proxy's address (too low), so it must match
// the deployment.
type RateLimits struct {
	PerAddress       Allower
	PerCredential    Allower
	DeviceStart      Allower
	TrustedProxyHops int
}

// RateLimited wraps the API with request rate limiting. A request over a
// limit gets 429 Too Many Requests with a Retry-After header in seconds and
// reaches no route. The health check is exempt so a platform's probes cannot
// be throttled into reporting the service down.
func RateLimited(next http.Handler, limits RateLimits) (http.Handler, error) {
	if next == nil || limits.PerAddress == nil || limits.PerCredential == nil || limits.DeviceStart == nil {
		return nil, errors.New("initialize rate limiting: a handler and all three limiters are required")
	}
	if limits.TrustedProxyHops < 0 || limits.TrustedProxyHops > MaxTrustedProxyHops {
		return nil, errors.New("initialize rate limiting: trusted proxy hops are out of range")
	}
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodGet && request.URL.Path == "/healthz" {
			next.ServeHTTP(response, request)
			return
		}
		address := clientAddress(request, limits.TrustedProxyHops)
		checks := []struct {
			limiter Allower
			key     string
		}{{limits.PerAddress, address}}
		if request.Method == http.MethodPost && request.URL.Path == "/v1/auth/device/start" {
			checks = append(checks, struct {
				limiter Allower
				key     string
			}{limits.DeviceStart, address})
		}
		if credential, presented := bearerCredential(request); presented {
			// Only a digest is used as the key, so limiter state never holds
			// a credential.
			digest := sha256.Sum256([]byte(credential))
			checks = append(checks, struct {
				limiter Allower
				key     string
			}{limits.PerCredential, hex.EncodeToString(digest[:])})
		}
		for _, check := range checks {
			if allowed, retryAfter := check.limiter.Allow(check.key); !allowed {
				writeRateLimited(response, retryAfter)
				return
			}
		}
		next.ServeHTTP(response, request)
	}), nil
}

func writeRateLimited(response http.ResponseWriter, retryAfter time.Duration) {
	response.Header().Set("Cache-Control", "no-store")
	response.Header().Set("Content-Type", "application/json")
	response.Header().Set("X-Content-Type-Options", "nosniff")
	response.Header().Set("Retry-After", strconv.FormatInt(durationSeconds(retryAfter), 10))
	writeError(response, http.StatusTooManyRequests, "rate_limited")
}

// clientAddress returns the limiter key for the request's origin.
//
// Each trusted proxy appends the address it received the request from to
// X-Forwarded-For, so with n trusted proxies the client is the nth entry from
// the right. Entries further left were supplied by the client and are never
// read. A header that is missing, too short, or not an address falls back to
// the connection's address, which limits the proxy as a whole rather than
// trusting unverifiable input.
//
// An IPv6 client is keyed by its /64, the smallest block routinely assigned
// to one subscriber; keying by full address would hand it 2^64 allowances.
func clientAddress(request *http.Request, trustedHops int) string {
	host, _, err := net.SplitHostPort(request.RemoteAddr)
	if err != nil {
		host = request.RemoteAddr
	}
	if trustedHops > 0 {
		var forwarded []string
		for _, header := range request.Header.Values("X-Forwarded-For") {
			for _, entry := range strings.Split(header, ",") {
				forwarded = append(forwarded, strings.TrimSpace(entry))
			}
		}
		if len(forwarded) >= trustedHops {
			if address, err := netip.ParseAddr(forwarded[len(forwarded)-trustedHops]); err == nil {
				return addressKey(address)
			}
		}
	}
	address, err := netip.ParseAddr(host)
	if err != nil {
		return host
	}
	return addressKey(address)
}

func addressKey(address netip.Addr) string {
	address = address.Unmap().WithZone("")
	if address.Is6() {
		if prefix, err := address.Prefix(64); err == nil {
			return prefix.String()
		}
	}
	return address.String()
}
