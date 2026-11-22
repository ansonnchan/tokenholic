// Package website describes where the static website is served and how to
// link into it. It is the one place the API knows the site's address shapes:
// the origin browsers may read public routes from, and the page addresses
// used in invitation links and share-page redirects. The site itself is the
// static files under web/; nothing here serves them.
package website

import (
	"errors"
	"net/url"
	"strings"
)

// Site is the website's address. The zero value means no site is configured.
type Site struct {
	origin string
	base   string
}

// Parse reads the website's HTTPS address. A path is allowed, because a
// GitHub Pages project site lives under /<repository>/; a query, fragment,
// or user is not. Anything else is rejected so a typo cannot widen the
// origin allowlist or send links somewhere unexpected.
func Parse(address string) (Site, error) {
	parsed, err := url.Parse(strings.TrimSpace(address))
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil ||
		parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || parsed.Opaque != "" {
		return Site{}, errors.New("website address must be an HTTPS URL without a query, fragment, or user")
	}
	if !validHost(parsed.Hostname()) || strings.HasSuffix(parsed.Host, ":") {
		return Site{}, errors.New("website address needs a plain host name")
	}
	path := strings.TrimRight(parsed.Path, "/")
	for _, segment := range strings.Split(strings.TrimPrefix(path, "/"), "/") {
		if path != "" && !validSegment(segment) {
			return Site{}, errors.New("website address path may hold only letters, digits, '.', '_', and '-'")
		}
	}
	origin := "https://" + strings.ToLower(parsed.Host)
	return Site{origin: origin, base: origin + path}, nil
}

// validHost accepts a DNS name: no wildcard, address literal, or empty label.
func validHost(host string) bool {
	for _, label := range strings.Split(host, ".") {
		if label == "" || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
			return false
		}
		for _, character := range label {
			alphanumeric := character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9'
			if !alphanumeric && character != '-' {
				return false
			}
		}
	}
	return true
}

func validSegment(segment string) bool {
	if segment == "" || segment == "." || segment == ".." {
		return false
	}
	for _, character := range segment {
		alphanumeric := character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9'
		if !alphanumeric && character != '.' && character != '_' && character != '-' {
			return false
		}
	}
	return true
}

// Configured reports whether a site address was set.
func (s Site) Configured() bool { return s.origin != "" }

// Origin is the scheme and host a browser sends in its Origin header for
// pages of the site, with no path.
func (s Site) Origin() string { return s.origin }

// Home is the landing page.
func (s Site) Home() string { return s.base + "/" }

// Invite is the invite page for a code. The code sits in the fragment, which
// browsers never send to the server hosting the site.
func (s Site) Invite(code string) string {
	return s.base + "/invite/#" + url.PathEscape(code)
}

// Profile is the public profile page for a GitHub login.
func (s Site) Profile(login string) string {
	return s.base + "/u/?login=" + url.QueryEscape(login)
}

// Board is the public board page for a group's public slug. period and
// metric select what it opens on; empty values leave the page's own default.
func (s Site) Board(slug, period, metric string) string {
	query := url.Values{"slug": []string{slug}}
	if period != "" {
		query.Set("period", period)
	}
	if metric != "" {
		query.Set("metric", metric)
	}
	// Encode sorts keys; the page reads them by name.
	return s.base + "/b/?" + query.Encode()
}
