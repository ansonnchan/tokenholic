package multiplayer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const maxResponseBytes = 1 << 20

var (
	// ErrUnauthorized means the stored device session is not accepted.
	ErrUnauthorized = errors.New("multiplayer session is unauthorized")
	// ErrNotFound hides a group or invite that is not visible to the caller.
	ErrNotFound = errors.New("multiplayer resource was not found")
	// ErrConflict means the requested membership transition is not allowed.
	ErrConflict = errors.New("multiplayer operation conflicts with current state")
)

// Period is a leaderboard calendar window.
type Period string

const (
	PeriodToday      Period = "today"
	PeriodWeek       Period = "week"
	PeriodLast30Days Period = "last_30_days"
	PeriodAllTime    Period = "all_time"
)

// Metric is the value a leaderboard ranks by: total tokens or the
// API-equivalent estimate.
type Metric string

const (
	MetricTotalTokens Metric = "total_tokens"
	MetricAPIEstimate Metric = "api_estimate"
)

// Group is one group visible to the authenticated account. PublicSlug names
// its public board only while Visibility is "public".
type Group struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Timezone   string `json:"timezone"`
	Role       string `json:"role"`
	Visibility string `json:"visibility"`
	PublicSlug string `json:"public_slug"`
}

// Invite is plaintext invitation material returned only at creation. Link is
// the same code as a website URL, when the server knows the website.
type Invite struct {
	Code      string    `json:"code"`
	Link      string    `json:"link"`
	ExpiresAt time.Time `json:"expires_at"`
	MaxUses   int       `json:"max_uses"`
}

// Profile is the signed-in account's identity and visibility choices.
type Profile struct {
	Login       string `json:"login"`
	AvatarURL   string `json:"avatar_url"`
	Visibility  string `json:"visibility"`
	GlobalOptIn bool   `json:"global_opt_in"`
}

// Availability preserves complete, partial, unavailable, and empty coverage.
type Availability string

const (
	AvailabilityEmpty       Availability = "empty"
	AvailabilityComplete    Availability = "complete"
	AvailabilityPartial     Availability = "partial"
	AvailabilityUnavailable Availability = "unavailable"
)

// Value is an arbitrary-precision decimal aggregate plus coverage.
type Value struct {
	Value *string      `json:"value"`
	State Availability `json:"state"`
}

// SourceMix is one source's token contribution.
type SourceMix struct {
	Source      string `json:"source"`
	TotalTokens Value  `json:"total_tokens"`
}

// ModelUsage is the most-used model with explicit resolution quality.
type ModelUsage struct {
	ModelID     string `json:"model_id"`
	Resolution  string `json:"resolution"`
	TotalTokens Value  `json:"total_tokens"`
}

// Limitations exposes incomplete model and estimate coverage.
type Limitations struct {
	UnknownModelBuckets           int64 `json:"unknown_model_buckets"`
	InferredModelBuckets          int64 `json:"inferred_model_buckets"`
	APIEstimateUnavailableBuckets int64 `json:"api_estimate_unavailable_buckets"`
}

// Coverage says how much of a member's usage in the window has a price.
// PricedBasisPoints is hundredths of a percent, nil when there are no tokens.
type Coverage struct {
	PricedTokens      string `json:"priced_tokens"`
	UnpricedTokens    string `json:"unpriced_tokens"`
	PricedBasisPoints *int   `json:"priced_basis_points"`
}

// UnrankedReason says why a row has no rank.
type UnrankedReason string

const (
	UnrankedNoUsage          UnrankedReason = "no_usage"
	UnrankedValueUnavailable UnrankedReason = "value_unavailable"
	UnrankedCostCoverage     UnrankedReason = "cost_coverage_below_threshold"
)

// Trend compares a row with the board's comparison window. RankChange is
// positive for a climb and nil when either window has no rank. New marks a
// member with usage now and none in the comparison window.
type Trend struct {
	PreviousValue Value `json:"previous_value"`
	PreviousRank  *int  `json:"previous_rank"`
	RankChange    *int  `json:"rank_change"`
	New           bool  `json:"new"`
}

// Plausibility is the server's advisory note on self-reported usage.
type Plausibility struct {
	DailyTokensAboveLimit bool `json:"daily_tokens_above_limit"`
}

// Row is one active group member's leaderboard result.
type Row struct {
	Rank           *int           `json:"rank"`
	UnrankedReason UnrankedReason `json:"unranked_reason"`
	UserID         string         `json:"user_id"`
	DisplayName    string         `json:"display_name"`
	CurrentUser    bool           `json:"current_user"`
	Value          Value          `json:"value"`
	Coverage       Coverage       `json:"cost_coverage"`
	Trend          Trend          `json:"trend"`
	Plausibility   Plausibility   `json:"plausibility"`
	LastSyncedAt   *time.Time     `json:"last_synced_at"`
	SourceMix      []SourceMix    `json:"source_mix"`
	MostUsedModel  *ModelUsage    `json:"most_used_model"`
	Limitations    Limitations    `json:"limitations"`
}

// ComparisonKind names what a board's trends are measured against.
type ComparisonKind string

const (
	// ComparisonPreviousPeriod is the preceding period at the same elapsed time.
	ComparisonPreviousPeriod ComparisonKind = "previous_period"
	// ComparisonDayEarlier is the all-time board as it stood 24 hours before.
	ComparisonDayEarlier ComparisonKind = "24h_earlier"
)

// Comparison is the window every row's trend is measured against.
type Comparison struct {
	Kind   ComparisonKind `json:"kind"`
	Window Window         `json:"window"`
}

// Window is the UTC range represented by a leaderboard.
type Window struct {
	Start *time.Time `json:"start"`
	End   time.Time  `json:"end"`
}

// Board is one group leaderboard response.
type Board struct {
	GroupID     string     `json:"group_id"`
	GroupName   string     `json:"group_name"`
	Timezone    string     `json:"timezone"`
	Period      Period     `json:"period"`
	Metric      Metric     `json:"metric"`
	Window      Window     `json:"window"`
	Comparison  Comparison `json:"comparison"`
	GeneratedAt time.Time  `json:"generated_at"`
	// CostRankMinPricedBasisPoints is the priced share an estimate needs to
	// be ranked, as the server applied it.
	CostRankMinPricedBasisPoints int   `json:"cost_rank_min_priced_basis_points"`
	Rows                         []Row `json:"rows"`
}

// Client calls the hosted private-group API.
type Client struct {
	baseURL    *url.URL
	httpClient *http.Client
}

// NewClient validates the API origin. Plain HTTP is accepted only for a
// loopback development server.
func NewClient(baseURL string, httpClient *http.Client) (*Client, error) {
	parsed, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("invalid tokenholic API URL")
	}
	if parsed.Scheme != "https" && !(parsed.Scheme == "http" && isLoopbackHost(parsed.Hostname())) {
		return nil, errors.New("tokenholic API URL must use HTTPS or loopback HTTP")
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 15 * time.Second}
	}
	clientCopy := *httpClient
	clientCopy.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	return &Client{baseURL: parsed, httpClient: &clientCopy}, nil
}

// List returns the caller's active group memberships.
func (c *Client) List(ctx context.Context, credential string) ([]Group, error) {
	var response struct {
		Groups []Group `json:"groups"`
	}
	if err := c.request(ctx, http.MethodGet, "/v1/groups", credential, nil, &response); err != nil {
		return nil, err
	}
	if response.Groups == nil {
		response.Groups = make([]Group, 0)
	}
	for _, group := range response.Groups {
		if !group.valid() {
			return nil, errors.New("multiplayer server returned an invalid group")
		}
	}
	return response.Groups, nil
}

// Create creates a private group in the supplied IANA timezone.
func (c *Client) Create(ctx context.Context, credential, name, timezone string) (Group, error) {
	var group Group
	err := c.request(ctx, http.MethodPost, "/v1/groups", credential, map[string]string{
		"name": name, "timezone": timezone,
	}, &group)
	if err == nil && !group.valid() {
		err = errors.New("multiplayer server returned an invalid group")
	}
	return group, err
}

// Join consumes one invite code.
func (c *Client) Join(ctx context.Context, credential, code string) (Group, error) {
	var group Group
	err := c.request(ctx, http.MethodPost, "/v1/group-invites/join", credential, map[string]string{"code": code}, &group)
	if err == nil && !group.valid() {
		err = errors.New("multiplayer server returned an invalid group")
	}
	return group, err
}

// Invite creates a limited-use invitation for a group.
func (c *Client) Invite(ctx context.Context, credential, groupID string, lifetime time.Duration, maxUses int) (Invite, error) {
	var invite Invite
	err := c.request(ctx, http.MethodPost, "/v1/groups/"+url.PathEscape(groupID)+"/invites", credential, map[string]any{
		"expires_in_seconds": int64(lifetime / time.Second), "max_uses": maxUses,
	}, &invite)
	if err == nil && (invite.Code == "" || invite.ExpiresAt.IsZero() || invite.MaxUses < 1) {
		err = errors.New("multiplayer server returned an invalid invite")
	}
	return invite, err
}

// Leave revokes the caller's member role.
func (c *Client) Leave(ctx context.Context, credential, groupID string) error {
	return c.request(ctx, http.MethodDelete, "/v1/groups/"+url.PathEscape(groupID)+"/membership", credential, nil, nil)
}

// Leaderboard returns one period and metric for a private group.
func (c *Client) Leaderboard(ctx context.Context, credential, groupID string, period Period, metric Metric) (Board, error) {
	query := url.Values{"period": []string{string(period)}, "metric": []string{string(metric)}}
	var board Board
	err := c.request(ctx, http.MethodGet, "/v1/groups/"+url.PathEscape(groupID)+"/leaderboard?"+query.Encode(), credential, nil, &board)
	if err == nil {
		windowInvalid := board.Window.End.IsZero() || (period == PeriodAllTime && board.Window.Start != nil) || (period != PeriodAllTime && board.Window.Start == nil)
		if board.GroupID != groupID || board.GroupName == "" || board.Timezone == "" || board.Period != period || board.Metric != metric || board.GeneratedAt.IsZero() || windowInvalid {
			err = errors.New("multiplayer server returned an invalid leaderboard")
		} else if board.Rows == nil {
			board.Rows = make([]Row, 0)
		}
	}
	return board, err
}

// Profile returns the caller's own profile and visibility choices.
func (c *Client) Profile(ctx context.Context, credential string) (Profile, error) {
	var profile Profile
	err := c.request(ctx, http.MethodGet, "/v1/account/profile", credential, nil, &profile)
	if err == nil && (profile.Login == "" || profile.Visibility != "public" && profile.Visibility != "private") {
		err = errors.New("multiplayer server returned an invalid profile")
	}
	return profile, err
}

// SetProfileVisibility makes the caller's profile public or private.
func (c *Client) SetProfileVisibility(ctx context.Context, credential string, public bool) error {
	want := visibilityName(public)
	var response struct {
		Visibility string `json:"visibility"`
	}
	err := c.request(ctx, http.MethodPut, "/v1/account/profile", credential, map[string]string{"visibility": want}, &response)
	if err == nil && response.Visibility != want {
		// Never report a visibility change the server did not confirm.
		err = errors.New("multiplayer server did not confirm the profile visibility")
	}
	return err
}

// SetGroupVisibility makes a group's board public or private. Owner only.
func (c *Client) SetGroupVisibility(ctx context.Context, credential, groupID string, public bool) (Group, error) {
	want := visibilityName(public)
	var group Group
	err := c.request(ctx, http.MethodPut, "/v1/groups/"+url.PathEscape(groupID)+"/visibility", credential, map[string]string{"visibility": want}, &group)
	if err == nil && (!group.valid() || group.Visibility != want || public && group.PublicSlug == "") {
		err = errors.New("multiplayer server did not confirm the group visibility")
	}
	return group, err
}

// DeleteGroup permanently deletes a group. Owner only.
func (c *Client) DeleteGroup(ctx context.Context, credential, groupID string) error {
	return c.request(ctx, http.MethodDelete, "/v1/groups/"+url.PathEscape(groupID), credential, map[string]string{"confirmation": "DELETE"}, nil)
}

// TransferGroup hands ownership to another member by GitHub login. Owner only.
func (c *Client) TransferGroup(ctx context.Context, credential, groupID, login string) error {
	return c.request(ctx, http.MethodPut, "/v1/groups/"+url.PathEscape(groupID)+"/owner", credential, map[string]string{"login": login}, nil)
}

func visibilityName(public bool) string {
	if public {
		return "public"
	}
	return "private"
}

// SetGlobalParticipation joins or leaves the opt-in global board for the
// caller's account.
func (c *Client) SetGlobalParticipation(ctx context.Context, credential string, participating bool) error {
	var response struct {
		Participating *bool `json:"participating"`
	}
	err := c.request(ctx, http.MethodPut, "/v1/account/global-board", credential, map[string]bool{"participating": participating}, &response)
	if err == nil && (response.Participating == nil || *response.Participating != participating) {
		// Never report a visibility change the server did not confirm.
		err = errors.New("multiplayer server did not confirm the global board choice")
	}
	return err
}

func (g Group) valid() bool {
	return g.ID != "" && g.Name != "" && g.Timezone != "" && (g.Role == "owner" || g.Role == "member")
}

func (c *Client) request(ctx context.Context, method, path, credential string, input, output any) error {
	var body io.Reader
	if input != nil {
		encoded, err := json.Marshal(input)
		if err != nil {
			return fmt.Errorf("encode multiplayer request: %w", err)
		}
		body = bytes.NewReader(encoded)
	}
	endpoint := *c.baseURL
	pathPart, queryPart, _ := strings.Cut(path, "?")
	endpoint.Path += pathPart
	endpoint.RawQuery = queryPart
	request, err := http.NewRequestWithContext(ctx, method, endpoint.String(), body)
	if err != nil {
		return fmt.Errorf("create multiplayer request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Authorization", "Bearer "+credential)
	if input != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := c.httpClient.Do(request)
	if err != nil {
		return fmt.Errorf("request multiplayer API: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxResponseBytes))
		switch response.StatusCode {
		case http.StatusUnauthorized:
			return ErrUnauthorized
		case http.StatusNotFound:
			return ErrNotFound
		case http.StatusConflict, http.StatusForbidden:
			return ErrConflict
		default:
			return fmt.Errorf("multiplayer server returned status %s", strconv.Itoa(response.StatusCode))
		}
	}
	if output != nil && response.StatusCode != http.StatusNoContent {
		decoder := json.NewDecoder(io.LimitReader(response.Body, maxResponseBytes))
		if err := decoder.Decode(output); err != nil {
			return fmt.Errorf("decode multiplayer response: %w", err)
		}
	} else {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxResponseBytes))
	}
	return nil
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	address := net.ParseIP(host)
	return address != nil && address.IsLoopback()
}
