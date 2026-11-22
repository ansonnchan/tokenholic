// Package sharepage serves the links people post. A share link answers a
// crawler with a small HTML page whose Open Graph tags name a PNG card, and
// sends a browser on to the matching page of the static website; crawlers do
// not run JavaScript, so the website cannot do this itself.
//
// The routes are public and read-only, and follow the rule of every public
// route: a private, deleted, or nonexistent record gets one identical
// response. The pages and cards show only what the public JSON routes
// already return, read through the same services.
package sharepage

import (
	"bytes"
	"context"
	"errors"
	"html/template"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	serveraccount "github.com/ansonnchan/tokenholic/server/account"
	servergroup "github.com/ansonnchan/tokenholic/server/group"
	serverleaderboard "github.com/ansonnchan/tokenholic/server/leaderboard"
	"github.com/ansonnchan/tokenholic/server/sharecard"
)

// Prefix is the path prefix of every share route.
const Prefix = "/s/"

// cardMaxAge is how long a shared cache may reuse a card. It is short on
// purpose: a card for a profile that has just gone private must stop being
// served soon, and the versioned address in each page already makes a
// crawler fetch a new card when the totals change.
const cardMaxAge = 300

// Profiles reads a public profile, or serveraccount.ErrProfileNotFound.
type Profiles interface {
	PublicProfile(ctx context.Context, login string) (serveraccount.PublicProfile, error)
}

// Boards reads a public group board, or servergroup.ErrNotFound.
type Boards interface {
	PublicGroup(ctx context.Context, slug string, period serverleaderboard.Period, metric serverleaderboard.Metric) (serverleaderboard.Board, error)
}

// Cards draws a card as a PNG.
type Cards interface {
	Render(sharecard.Card) ([]byte, error)
}

// Site builds addresses of the static website's pages.
type Site interface {
	Home() string
	Profile(login string) string
	Board(slug, period, metric string) string
}

// Handler serves share pages and cards under Prefix.
type Handler struct {
	profiles Profiles
	boards   Boards
	cards    Cards
	site     Site
	mux      *http.ServeMux
}

// New constructs the share routes.
func New(profiles Profiles, boards Boards, cards Cards, site Site) (*Handler, error) {
	if profiles == nil || boards == nil || cards == nil || site == nil {
		return nil, errors.New("initialize share pages: profiles, boards, a card renderer, and the website are required")
	}
	handler := &Handler{profiles: profiles, boards: boards, cards: cards, site: site, mux: http.NewServeMux()}
	handler.mux.HandleFunc("GET /s/u/{login}", handler.profilePage)
	handler.mux.HandleFunc("GET /s/u/{login}/card.png", handler.profileCard)
	handler.mux.HandleFunc("GET /s/b/{slug}", handler.boardPage)
	handler.mux.HandleFunc("GET /s/b/{slug}/card.png", handler.boardCard)
	// Anything else under the prefix is the same miss as an unknown record.
	handler.mux.HandleFunc("GET /s/", func(response http.ResponseWriter, _ *http.Request) { writeNotFound(response) })
	return handler, nil
}

// ServeHTTP routes a share request. Only GET and HEAD are served.
func (h *Handler) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	header := response.Header()
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("Referrer-Policy", "no-referrer")
	// A share page runs no script and loads only its own card.
	header.Set("Content-Security-Policy", "default-src 'none'; img-src 'self'; style-src 'unsafe-inline'; base-uri 'none'; form-action 'none'")
	h.mux.ServeHTTP(response, request)
}

// target is what one share link resolves to: the page's words and card, the
// website page a browser is sent on to, and the query that selects the same
// view of this share link again.
type target struct {
	view        view
	destination string
	query       string
}

func (h *Handler) profile(request *http.Request) (target, error) {
	profile, err := h.profiles.PublicProfile(request.Context(), request.PathValue("login"))
	if err != nil {
		return target{}, err
	}
	return target{view: profileView(profile), destination: h.site.Profile(profile.Login)}, nil
}

func (h *Handler) board(request *http.Request) (target, error) {
	period, metric := boardQuery(request)
	slug := request.PathValue("slug")
	board, err := h.boards.PublicGroup(request.Context(), slug, period, metric)
	if err != nil {
		return target{}, err
	}
	return target{
		view:        boardView(board),
		destination: h.site.Board(slug, string(period), string(metric)),
		query:       "period=" + string(period) + "&metric=" + string(metric),
	}, nil
}

// boardQuery reads a board's period and metric. A share link opens as the
// website's board does, on this week's estimate, and anything unrecognized
// falls back to that rather than failing a link someone already posted.
func boardQuery(request *http.Request) (serverleaderboard.Period, serverleaderboard.Metric) {
	period := serverleaderboard.Period(request.URL.Query().Get("period"))
	if !period.Valid() {
		period = serverleaderboard.PeriodWeek
	}
	metric := serverleaderboard.Metric(request.URL.Query().Get("metric"))
	if !metric.Valid() {
		metric = serverleaderboard.MetricAPIEstimate
	}
	return period, metric
}

func (h *Handler) profilePage(response http.ResponseWriter, request *http.Request) {
	shared, err := h.profile(request)
	h.writePage(response, request, shared, err)
}

func (h *Handler) boardPage(response http.ResponseWriter, request *http.Request) {
	shared, err := h.board(request)
	h.writePage(response, request, shared, err)
}

func (h *Handler) profileCard(response http.ResponseWriter, request *http.Request) {
	shared, err := h.profile(request)
	h.writeCard(response, request, shared.view, err)
}

func (h *Handler) boardCard(response http.ResponseWriter, request *http.Request) {
	shared, err := h.board(request)
	h.writeCard(response, request, shared.view, err)
}

// missing reports whether err is a public miss. Private, deleted, and
// nonexistent records all arrive as one of these errors, already
// indistinguishable; an invalid name is refused the same way.
func missing(err error) bool {
	return errors.Is(err, serveraccount.ErrProfileNotFound) || errors.Is(err, servergroup.ErrNotFound) || errors.Is(err, servergroup.ErrInvalidInput)
}

func (h *Handler) writePage(response http.ResponseWriter, request *http.Request, shared target, err error) {
	origin, ok := requestOrigin(request)
	switch {
	case err != nil && missing(err), err == nil && !ok:
		writeNotFound(response)
		return
	case err != nil:
		writeUnavailable(response)
		return
	}
	page := shared.view
	self := origin + request.URL.Path
	// The card's address carries its content hash, so a crawler that cached
	// an earlier card fetches the new one when the totals change.
	image := self + "/card.png?v=" + page.Card.Hash()
	if shared.query != "" {
		self += "?" + shared.query
		image += "&" + shared.query
	}
	var body bytes.Buffer
	if err := pageTemplate.Execute(&body, map[string]any{
		"Title": page.Title, "Description": page.Description, "Alt": page.Card.Alt(),
		"Self": self, "Image": image, "Destination": shared.destination,
		"Width": sharecard.Width, "Height": sharecard.Height,
	}); err != nil {
		writeUnavailable(response)
		return
	}
	header := response.Header()
	header.Set("Content-Type", "text/html; charset=utf-8")
	// Never cached: a page for a profile that goes private must stop at once.
	header.Set("Cache-Control", "no-store")
	response.WriteHeader(http.StatusOK)
	_, _ = response.Write(body.Bytes())
}

func (h *Handler) writeCard(response http.ResponseWriter, request *http.Request, page view, err error) {
	switch {
	case err != nil && missing(err):
		writeNotFound(response)
		return
	case err != nil:
		writeUnavailable(response)
		return
	}
	header := response.Header()
	tag := `"` + page.Card.Hash() + `"`
	header.Set("ETag", tag)
	header.Set("Cache-Control", "public, max-age="+strconv.Itoa(cardMaxAge))
	if request.Header.Get("If-None-Match") == tag {
		header.Del("Content-Type")
		response.WriteHeader(http.StatusNotModified)
		return
	}
	data, err := h.cards.Render(page.Card)
	if err != nil {
		header.Del("ETag")
		writeUnavailable(response)
		return
	}
	header.Set("Content-Type", "image/png")
	header.Set("Content-Length", strconv.Itoa(len(data)))
	response.WriteHeader(http.StatusOK)
	_, _ = response.Write(data)
}

var hostPattern = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9.-]{0,251}[A-Za-z0-9])?(?::[0-9]{1,5})?$`)

// requestOrigin is the address this API was reached at, for the absolute
// links Open Graph requires. It is HTTPS except on loopback, and a Host that
// is not a plain name is refused, so nothing a client sends can put markup or
// another scheme into a page.
func requestOrigin(request *http.Request) (string, bool) {
	host := request.Host
	if !hostPattern.MatchString(host) {
		return "", false
	}
	name, _, _ := strings.Cut(host, ":")
	if name == "localhost" || name == "127.0.0.1" {
		return "http://" + host, true
	}
	return "https://" + host, true
}

const notFoundBody = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="robots" content="noindex">
<title>Not found · tokenholic</title>
<style>html{background:#0B1120;color-scheme:dark}body{margin:0;padding:15vh 24px 24px;background:#0B1120;color:#E8EAF0;font:15px/1.6 ui-monospace,"SF Mono",Menlo,Consolas,monospace}main{max-width:560px;margin:0 auto}.mark{font-weight:700;letter-spacing:.14em}.mark span,h1{color:#D9805F}h1{margin:32px 0 8px;font-size:13px;letter-spacing:.14em}p{margin:0 0 12px}.muted{color:#8D929B}</style>
</head>
<body>
<main>
<p class="mark"><span>&gt;_</span> TOKENHOLIC</p>
<h1>NOT FOUND</h1>
<p>There is nothing to share at this address.</p>
<p class="muted">A profile or board can be shared only while its owner keeps it public.</p>
</main>
</body>
</html>
`

// writeNotFound is the one response for every share route that finds nothing
// to show: a private, deleted, or nonexistent record, a malformed name, or an
// unknown path. Status, headers, and body are identical in every case.
func writeNotFound(response http.ResponseWriter) {
	header := response.Header()
	header.Set("Content-Type", "text/html; charset=utf-8")
	header.Set("Cache-Control", "no-store")
	response.WriteHeader(http.StatusNotFound)
	_, _ = response.Write([]byte(notFoundBody))
}

// writeUnavailable answers a failure that is not a miss, without detail.
func writeUnavailable(response http.ResponseWriter) {
	header := response.Header()
	header.Set("Content-Type", "text/plain; charset=utf-8")
	header.Set("Cache-Control", "no-store")
	response.WriteHeader(http.StatusInternalServerError)
	_, _ = response.Write([]byte("temporarily unavailable\n"))
}

// pageTemplate is the share page. html/template escapes every value for the
// place it lands, so a login or group name cannot add markup. og:url names
// the share page itself: a crawler that follows it must land back on these
// tags, not on the static site, which has none per record.
var pageTemplate = template.Must(template.New("share").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="robots" content="noindex">
<title>{{.Title}}</title>
<meta name="description" content="{{.Description}}">
<meta property="og:type" content="website">
<meta property="og:site_name" content="tokenholic">
<meta property="og:title" content="{{.Title}}">
<meta property="og:description" content="{{.Description}}">
<meta property="og:url" content="{{.Self}}">
<meta property="og:image" content="{{.Image}}">
<meta property="og:image:type" content="image/png">
<meta property="og:image:width" content="{{.Width}}">
<meta property="og:image:height" content="{{.Height}}">
<meta property="og:image:alt" content="{{.Alt}}">
<meta name="twitter:card" content="summary_large_image">
<meta name="twitter:title" content="{{.Title}}">
<meta name="twitter:description" content="{{.Description}}">
<meta name="twitter:image" content="{{.Image}}">
<meta name="twitter:image:alt" content="{{.Alt}}">
<meta http-equiv="refresh" content="0; url={{.Destination}}">
<style>html{background:#0B1120;color-scheme:dark}body{margin:0;padding:8vh 24px 24px;background:#0B1120;color:#E8EAF0;font:15px/1.6 ui-monospace,"SF Mono",Menlo,Consolas,monospace}main{max-width:640px;margin:0 auto}.mark{font-weight:700;letter-spacing:.14em}.mark span{color:#D9805F}h1{margin:28px 0 8px;font-size:18px}p{margin:0 0 16px}.muted{color:#8D929B}a{color:#D9805F;text-underline-offset:3px}a:focus-visible{outline:2px solid #D9805F;outline-offset:3px}img{display:block;width:100%;height:auto;margin-top:24px;border:1px solid #50555E;border-radius:6px}</style>
</head>
<body>
<main>
<p class="mark"><span>&gt;_</span> TOKENHOLIC</p>
<h1>{{.Title}}</h1>
<p class="muted">{{.Description}}</p>
<p><a href="{{.Destination}}">Continue to the page</a></p>
<img src="{{.Image}}" width="{{.Width}}" height="{{.Height}}" alt="{{.Alt}}">
</main>
</body>
</html>
`))
