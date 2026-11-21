package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	defaultGitHubDeviceURL = "https://github.com/login/device/code"
	defaultGitHubTokenURL  = "https://github.com/login/oauth/access_token"
	defaultGitHubUserURL   = "https://api.github.com/user"
	gitHubAPIVersion       = "2022-11-28"
	maxGitHubResponseBytes = 1 << 20
)

// GitHubClient implements the minimum GitHub OAuth device-flow surface.
type GitHubClient struct {
	httpClient *http.Client
	clientID   string
	deviceURL  string
	tokenURL   string
	userURL    string
	now        func() time.Time
}

// NewGitHubClient creates a device-flow client for a registered GitHub app.
func NewGitHubClient(httpClient *http.Client, clientID string) (*GitHubClient, error) {
	if strings.TrimSpace(clientID) == "" {
		return nil, errors.New("GitHub client ID is required")
	}
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &GitHubClient{
		httpClient: httpClient, clientID: clientID,
		deviceURL: defaultGitHubDeviceURL, tokenURL: defaultGitHubTokenURL,
		userURL: defaultGitHubUserURL, now: time.Now,
	}, nil
}

// Start requests a short-lived user and device code without scopes beyond
// public identity.
func (c *GitHubClient) Start(ctx context.Context) (DeviceAuthorization, error) {
	values := url.Values{"client_id": {c.clientID}}
	var response struct {
		DeviceCode      string `json:"device_code"`
		UserCode        string `json:"user_code"`
		VerificationURI string `json:"verification_uri"`
		ExpiresIn       int64  `json:"expires_in"`
		Interval        int64  `json:"interval"`
	}
	if err := c.postForm(ctx, c.deviceURL, values, &response); err != nil {
		return DeviceAuthorization{}, err
	}
	if response.ExpiresIn <= 0 || response.Interval <= 0 {
		return DeviceAuthorization{}, errors.New("GitHub device response has invalid timing")
	}
	return DeviceAuthorization{
		DeviceCode: response.DeviceCode, UserCode: response.UserCode,
		VerificationURI: response.VerificationURI,
		ExpiresAt:       c.now().UTC().Add(time.Duration(response.ExpiresIn) * time.Second),
		Interval:        time.Duration(response.Interval) * time.Second,
	}, nil
}

// Poll checks whether the user approved the device code.
func (c *GitHubClient) Poll(ctx context.Context, deviceCode string) (GitHubPollResult, error) {
	values := url.Values{
		"client_id":   {c.clientID},
		"device_code": {deviceCode},
		"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
	}
	var response struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
		Interval    int64  `json:"interval"`
	}
	if err := c.postForm(ctx, c.tokenURL, values, &response); err != nil {
		return GitHubPollResult{}, err
	}
	switch response.Error {
	case "":
		if response.AccessToken == "" {
			return GitHubPollResult{}, errors.New("GitHub token response is incomplete")
		}
		return GitHubPollResult{Status: FlowApproved, AccessToken: response.AccessToken}, nil
	case "authorization_pending":
		return GitHubPollResult{Status: FlowPending}, nil
	case "slow_down":
		retry := time.Duration(response.Interval) * time.Second
		if retry < 5*time.Second {
			retry = 5 * time.Second
		}
		return GitHubPollResult{Status: FlowSlowDown, RetryAfter: retry}, nil
	case "expired_token":
		return GitHubPollResult{Status: FlowExpired}, nil
	case "access_denied":
		return GitHubPollResult{Status: FlowDenied}, nil
	default:
		return GitHubPollResult{}, fmt.Errorf("GitHub device authorization failed with %q", response.Error)
	}
}

// User resolves only the stable numeric ID, login, display name, and avatar
// image URL. The access token remains in memory for this request and is never
// retained.
func (c *GitHubClient) User(ctx context.Context, accessToken string) (GitHubUser, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.userURL, nil)
	if err != nil {
		return GitHubUser{}, fmt.Errorf("create GitHub user request: %w", err)
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("Authorization", "Bearer "+accessToken)
	request.Header.Set("X-GitHub-Api-Version", gitHubAPIVersion)
	response, err := c.httpClient.Do(request)
	if err != nil {
		return GitHubUser{}, fmt.Errorf("request GitHub user: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxGitHubResponseBytes))
		return GitHubUser{}, fmt.Errorf("request GitHub user: unexpected status %d", response.StatusCode)
	}
	var user struct {
		ID        int64  `json:"id"`
		Login     string `json:"login"`
		Name      string `json:"name"`
		AvatarURL string `json:"avatar_url"`
	}
	if err := decodeLimitedJSON(response.Body, &user); err != nil {
		return GitHubUser{}, fmt.Errorf("decode GitHub user: %w", err)
	}
	return GitHubUser{ID: user.ID, Login: user.Login, Name: user.Name, AvatarURL: AvatarURL(user.AvatarURL)}, nil
}

// avatarHost is the only host an avatar may be served from. Storing whatever
// URL a response contains would let it point anywhere a page later loads.
const avatarHost = "avatars.githubusercontent.com"

// AvatarURL returns value when it is an HTTPS URL on GitHub's avatar host and
// short enough to store, and "" otherwise. A missing avatar is not an error:
// the account simply has none to show.
func AvatarURL(value string) string {
	parsed, err := url.Parse(value)
	if err != nil || len(value) > 512 || parsed.Scheme != "https" || parsed.Host != avatarHost || parsed.User != nil || parsed.Fragment != "" {
		return ""
	}
	return value
}

func (c *GitHubClient) postForm(ctx context.Context, endpoint string, values url.Values, target any) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(values.Encode()))
	if err != nil {
		return fmt.Errorf("create GitHub request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := c.httpClient.Do(request)
	if err != nil {
		return fmt.Errorf("request GitHub: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxGitHubResponseBytes))
		return fmt.Errorf("request GitHub: unexpected status %d", response.StatusCode)
	}
	if err := decodeLimitedJSON(response.Body, target); err != nil {
		return fmt.Errorf("decode GitHub response: %w", err)
	}
	return nil
}

func decodeLimitedJSON(reader io.Reader, target any) error {
	decoder := json.NewDecoder(io.LimitReader(reader, maxGitHubResponseBytes))
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("response contains trailing JSON")
	}
	return nil
}
