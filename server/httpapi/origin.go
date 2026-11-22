package httpapi

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
)

// publicPrefix is the path prefix of the unauthenticated, read-only routes.
const publicPrefix = "/v1/public/"

// AllowWebOrigin lets pages served from the website read the public routes
// from a browser. It answers a request for a public route whose Origin header
// is exactly origin with Access-Control-Allow-Origin naming it; every other
// origin gets no such header, and the browser withholds the response from
// that page. No other route is ever readable cross-origin, credentials are
// never allowed, and there is no wildcard.
//
// The headers depend only on the request's path prefix and Origin header,
// never on the record asked for, so a private record still answers exactly
// as a nonexistent one. Wrap the rate limiter with this rather than the
// other way round: a refused request then also reaches the page, which can
// say "try again" instead of reporting an unreachable API.
//
// Public routes are plain GETs with no custom headers, which browsers send
// without a preflight, so OPTIONS is deliberately not answered.
func AllowWebOrigin(next http.Handler, origin string) (http.Handler, error) {
	parsed, err := url.Parse(origin)
	if next == nil || err != nil || parsed.Scheme != "https" || parsed.Host == "" || strings.Contains(parsed.Host, "*") ||
		origin != "https://"+parsed.Host {
		return nil, errors.New("initialize origin allowlist: a handler and an HTTPS origin without a path are required")
	}
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if strings.HasPrefix(request.URL.Path, publicPrefix) {
			// The response differs by Origin, so a shared cache must key on it.
			response.Header().Add("Vary", "Origin")
			if request.Header.Get("Origin") == origin {
				response.Header().Set("Access-Control-Allow-Origin", origin)
				// Retry-After is not readable cross-origin unless exposed.
				response.Header().Set("Access-Control-Expose-Headers", "Retry-After")
			}
		}
		next.ServeHTTP(response, request)
	}), nil
}
