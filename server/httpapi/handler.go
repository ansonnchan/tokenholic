package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	protocol "github.com/ansonnchan/tokenholic/internal/sync"
	serveraccount "github.com/ansonnchan/tokenholic/server/account"
	serverauth "github.com/ansonnchan/tokenholic/server/auth"
	servergroup "github.com/ansonnchan/tokenholic/server/group"
	serverleaderboard "github.com/ansonnchan/tokenholic/server/leaderboard"
	serversync "github.com/ansonnchan/tokenholic/server/sync"
)

const (
	maxRequestBytes     = 16 << 10
	maxSyncRequestBytes = 1 << 20
)

type authenticationService interface {
	Start(context.Context, string) (serverauth.StartResponse, error)
	Poll(context.Context, string) (serverauth.PollResponse, error)
	Refresh(context.Context, string) (serverauth.Session, error)
	Logout(context.Context, string) error
}

type usageSyncService interface {
	Apply(context.Context, string, protocol.Batch) (protocol.BatchResponse, error)
}

type accountUsageService interface {
	Usage(context.Context, string) (serveraccount.Usage, error)
	Delete(context.Context, string) error
	SetGlobalOptIn(context.Context, string, bool) error
	Profile(context.Context, string) (serveraccount.Profile, error)
	SetProfileVisibility(context.Context, string, serveraccount.Visibility) error
	PublicProfile(context.Context, string) (serveraccount.PublicProfile, error)
}

type groupService interface {
	Create(context.Context, string, string, string) (servergroup.Group, error)
	List(context.Context, string) ([]servergroup.Group, error)
	Get(context.Context, string, string) (servergroup.Detail, error)
	Invite(context.Context, string, string, time.Duration, int) (servergroup.Invite, error)
	Join(context.Context, string, string) (servergroup.Group, error)
	Leave(context.Context, string, string) error
	Revoke(context.Context, string, string, string) error
	SetVisibility(context.Context, string, string, servergroup.Visibility) (servergroup.Group, error)
	Delete(context.Context, string, string) error
	Transfer(context.Context, string, string, string) error
	InvitePreview(context.Context, string) (servergroup.InvitePreview, error)
}

type leaderboardService interface {
	Get(context.Context, string, string, serverleaderboard.Period, serverleaderboard.Metric) (serverleaderboard.Board, error)
	Global(context.Context, string, serverleaderboard.Period, serverleaderboard.Metric) (serverleaderboard.Board, error)
	PublicGroup(context.Context, string, serverleaderboard.Period, serverleaderboard.Metric) (serverleaderboard.Board, error)
}

// Handler is the hosted tokenholic HTTP API.
type Handler struct {
	auth         authenticationService
	sync         usageSyncService
	account      accountUsageService
	groups       groupService
	leaderboards leaderboardService
	mux          *http.ServeMux
}

// New constructs the API handler with exact method-aware routes.
func New(authService authenticationService, syncService usageSyncService, accountService accountUsageService, groups groupService, leaderboards leaderboardService) (*Handler, error) {
	if authService == nil || syncService == nil || accountService == nil || groups == nil || leaderboards == nil {
		return nil, errors.New("initialize HTTP API: authentication, synchronization, account, group, and leaderboard services are required")
	}
	handler := &Handler{
		auth: authService, sync: syncService, account: accountService,
		groups: groups, leaderboards: leaderboards, mux: http.NewServeMux(),
	}
	handler.mux.HandleFunc("GET /healthz", handler.health)
	handler.mux.HandleFunc("POST /v1/auth/device/start", handler.startDevice)
	handler.mux.HandleFunc("POST /v1/auth/device/poll", handler.pollDevice)
	handler.mux.HandleFunc("POST /v1/auth/refresh", handler.refresh)
	handler.mux.HandleFunc("POST /v1/auth/logout", handler.logout)
	handler.mux.HandleFunc("POST "+protocol.BucketsPath, handler.syncBuckets)
	handler.mux.HandleFunc("POST /v1/sync/buckets", handler.retiredSyncV1)
	handler.mux.HandleFunc("GET /v1/account/usage", handler.accountUsage)
	handler.mux.HandleFunc("DELETE /v1/account", handler.deleteAccount)
	handler.mux.HandleFunc("POST /v1/groups", handler.createGroup)
	handler.mux.HandleFunc("GET /v1/groups", handler.listGroups)
	handler.mux.HandleFunc("GET /v1/groups/{group_id}", handler.getGroup)
	handler.mux.HandleFunc("POST /v1/groups/{group_id}/invites", handler.createGroupInvite)
	handler.mux.HandleFunc("POST /v1/group-invites/join", handler.joinGroup)
	handler.mux.HandleFunc("DELETE /v1/groups/{group_id}/membership", handler.leaveGroup)
	handler.mux.HandleFunc("DELETE /v1/groups/{group_id}/members/{user_id}", handler.revokeGroupMember)
	handler.mux.HandleFunc("GET /v1/groups/{group_id}/leaderboard", handler.groupLeaderboard)
	handler.mux.HandleFunc("GET /v1/global/leaderboard", handler.globalLeaderboard)
	handler.mux.HandleFunc("PUT /v1/account/global-board", handler.setGlobalBoard)
	handler.mux.HandleFunc("GET /v1/account/profile", handler.accountProfile)
	handler.mux.HandleFunc("PUT /v1/account/profile", handler.setAccountProfile)
	handler.mux.HandleFunc("PUT /v1/groups/{group_id}/visibility", handler.setGroupVisibility)
	handler.mux.HandleFunc("DELETE /v1/groups/{group_id}", handler.deleteGroup)
	handler.mux.HandleFunc("PUT /v1/groups/{group_id}/owner", handler.transferGroup)
	// Public, read-only routes. None takes a credential, and each answers a
	// private record exactly as it answers one that does not exist.
	handler.mux.HandleFunc("GET /v1/public/users/{login}", handler.publicProfile)
	handler.mux.HandleFunc("GET /v1/public/groups/{slug}/leaderboard", handler.publicGroupLeaderboard)
	handler.mux.HandleFunc("GET /v1/public/invites/{code}", handler.publicInvite)
	return handler, nil
}

// writePublicNotFound is the one response for every public read that finds
// nothing to show. Private, deleted, and nonexistent records must all end
// here with identical status, headers, and body.
func writePublicNotFound(response http.ResponseWriter) {
	writeError(response, http.StatusNotFound, "not_found")
}

func (h *Handler) publicProfile(response http.ResponseWriter, request *http.Request) {
	profile, err := h.account.PublicProfile(request.Context(), request.PathValue("login"))
	switch {
	case errors.Is(err, serveraccount.ErrProfileNotFound):
		writePublicNotFound(response)
	case err != nil:
		writeError(response, http.StatusInternalServerError, "internal_error")
	default:
		writeJSON(response, http.StatusOK, profile)
	}
}

func (h *Handler) publicGroupLeaderboard(response http.ResponseWriter, request *http.Request) {
	period, metric := boardQuery(request)
	board, err := h.leaderboards.PublicGroup(request.Context(), request.PathValue("slug"), period, metric)
	switch {
	case errors.Is(err, servergroup.ErrNotFound):
		writePublicNotFound(response)
	case errors.Is(err, servergroup.ErrInvalidInput):
		writeError(response, http.StatusBadRequest, "invalid_request")
	case err != nil:
		writeError(response, http.StatusInternalServerError, "internal_error")
	default:
		writeJSON(response, http.StatusOK, board)
	}
}

func (h *Handler) publicInvite(response http.ResponseWriter, request *http.Request) {
	preview, err := h.groups.InvitePreview(request.Context(), request.PathValue("code"))
	switch {
	case errors.Is(err, servergroup.ErrInvalidInvite):
		writePublicNotFound(response)
	case err != nil:
		writeError(response, http.StatusInternalServerError, "internal_error")
	default:
		writeJSON(response, http.StatusOK, preview)
	}
}

func (h *Handler) accountProfile(response http.ResponseWriter, request *http.Request) {
	credential, ok := bearerCredential(request)
	if !ok {
		writeError(response, http.StatusUnauthorized, "unauthorized")
		return
	}
	profile, err := h.account.Profile(request.Context(), credential)
	if errors.Is(err, serverauth.ErrUnauthorized) {
		writeError(response, http.StatusUnauthorized, "unauthorized")
		return
	} else if err != nil {
		writeError(response, http.StatusInternalServerError, "internal_error")
		return
	}
	writeJSON(response, http.StatusOK, profile)
}

// setAccountProfile makes the profile public or private. Only those two
// words are accepted, so nothing else can publish a profile.
func (h *Handler) setAccountProfile(response http.ResponseWriter, request *http.Request) {
	credential, ok := bearerCredential(request)
	if !ok {
		writeError(response, http.StatusUnauthorized, "unauthorized")
		return
	}
	var input struct {
		Visibility string `json:"visibility"`
	}
	if err := decodeRequest(response, request, &input); err != nil {
		writeError(response, http.StatusBadRequest, "invalid_request")
		return
	}
	err := h.account.SetProfileVisibility(request.Context(), credential, serveraccount.Visibility(input.Visibility))
	switch {
	case errors.Is(err, serverauth.ErrUnauthorized):
		writeError(response, http.StatusUnauthorized, "unauthorized")
	case errors.Is(err, serveraccount.ErrInvalidVisibility):
		writeError(response, http.StatusBadRequest, "invalid_request")
	case err != nil:
		writeError(response, http.StatusInternalServerError, "internal_error")
	default:
		writeJSON(response, http.StatusOK, map[string]string{"visibility": input.Visibility})
	}
}

func (h *Handler) setGroupVisibility(response http.ResponseWriter, request *http.Request) {
	credential, ok := bearerCredential(request)
	if !ok {
		writeError(response, http.StatusUnauthorized, "unauthorized")
		return
	}
	var input struct {
		Visibility string `json:"visibility"`
	}
	if err := decodeRequest(response, request, &input); err != nil {
		writeError(response, http.StatusBadRequest, "invalid_request")
		return
	}
	group, err := h.groups.SetVisibility(request.Context(), credential, request.PathValue("group_id"), servergroup.Visibility(input.Visibility))
	if writeGroupError(response, err) {
		return
	}
	writeJSON(response, http.StatusOK, group)
}

// deleteGroup requires an explicit confirmation word, as account deletion
// does, so a stray request cannot delete a group.
func (h *Handler) deleteGroup(response http.ResponseWriter, request *http.Request) {
	credential, ok := bearerCredential(request)
	if !ok {
		writeError(response, http.StatusUnauthorized, "unauthorized")
		return
	}
	var input struct {
		Confirmation string `json:"confirmation"`
	}
	if err := decodeRequest(response, request, &input); err != nil || input.Confirmation != "DELETE" {
		writeError(response, http.StatusBadRequest, "confirmation_required")
		return
	}
	if writeGroupError(response, h.groups.Delete(request.Context(), credential, request.PathValue("group_id"))) {
		return
	}
	response.WriteHeader(http.StatusNoContent)
}

func (h *Handler) transferGroup(response http.ResponseWriter, request *http.Request) {
	credential, ok := bearerCredential(request)
	if !ok {
		writeError(response, http.StatusUnauthorized, "unauthorized")
		return
	}
	var input struct {
		Login string `json:"login"`
	}
	if err := decodeRequest(response, request, &input); err != nil {
		writeError(response, http.StatusBadRequest, "invalid_request")
		return
	}
	if writeGroupError(response, h.groups.Transfer(request.Context(), credential, request.PathValue("group_id"), input.Login)) {
		return
	}
	response.WriteHeader(http.StatusNoContent)
}

// setGlobalBoard joins or leaves the global board. The body must state the
// choice explicitly, so an empty or malformed request can never opt an
// account in.
func (h *Handler) setGlobalBoard(response http.ResponseWriter, request *http.Request) {
	credential, ok := bearerCredential(request)
	if !ok {
		writeError(response, http.StatusUnauthorized, "unauthorized")
		return
	}
	var input struct {
		Participating *bool `json:"participating"`
	}
	if err := decodeRequest(response, request, &input); err != nil || input.Participating == nil {
		writeError(response, http.StatusBadRequest, "invalid_request")
		return
	}
	if err := h.account.SetGlobalOptIn(request.Context(), credential, *input.Participating); errors.Is(err, serverauth.ErrUnauthorized) {
		writeError(response, http.StatusUnauthorized, "unauthorized")
		return
	} else if err != nil {
		writeError(response, http.StatusInternalServerError, "internal_error")
		return
	}
	writeJSON(response, http.StatusOK, map[string]bool{"participating": *input.Participating})
}

func (h *Handler) globalLeaderboard(response http.ResponseWriter, request *http.Request) {
	credential, ok := bearerCredential(request)
	if !ok {
		writeError(response, http.StatusUnauthorized, "unauthorized")
		return
	}
	period, metric := boardQuery(request)
	board, err := h.leaderboards.Global(request.Context(), credential, period, metric)
	if writeGroupError(response, err) {
		return
	}
	writeJSON(response, http.StatusOK, board)
}

// boardQuery reads a board's period and metric, defaulting to today's tokens.
func boardQuery(request *http.Request) (serverleaderboard.Period, serverleaderboard.Metric) {
	period := serverleaderboard.Period(request.URL.Query().Get("period"))
	if period == "" {
		period = serverleaderboard.PeriodToday
	}
	metric := serverleaderboard.Metric(request.URL.Query().Get("metric"))
	if metric == "" {
		metric = serverleaderboard.MetricTotalTokens
	}
	return period, metric
}

func (h *Handler) deleteAccount(response http.ResponseWriter, request *http.Request) {
	credential, ok := bearerCredential(request)
	if !ok {
		writeError(response, http.StatusUnauthorized, "unauthorized")
		return
	}
	var input struct {
		Confirmation string `json:"confirmation"`
	}
	if err := decodeRequest(response, request, &input); err != nil || input.Confirmation != "DELETE" {
		writeError(response, http.StatusBadRequest, "confirmation_required")
		return
	}
	if err := h.account.Delete(request.Context(), credential); errors.Is(err, serverauth.ErrUnauthorized) {
		writeError(response, http.StatusUnauthorized, "unauthorized")
		return
	} else if err != nil {
		writeError(response, http.StatusInternalServerError, "internal_error")
		return
	}
	response.WriteHeader(http.StatusNoContent)
}

func (h *Handler) groupLeaderboard(response http.ResponseWriter, request *http.Request) {
	credential, ok := bearerCredential(request)
	if !ok {
		writeError(response, http.StatusUnauthorized, "unauthorized")
		return
	}
	period, metric := boardQuery(request)
	board, err := h.leaderboards.Get(request.Context(), credential, request.PathValue("group_id"), period, metric)
	if writeGroupError(response, err) {
		return
	}
	writeJSON(response, http.StatusOK, board)
}

func (h *Handler) createGroup(response http.ResponseWriter, request *http.Request) {
	credential, ok := bearerCredential(request)
	if !ok {
		writeError(response, http.StatusUnauthorized, "unauthorized")
		return
	}
	var input struct {
		Name     string `json:"name"`
		Timezone string `json:"timezone"`
	}
	if err := decodeRequest(response, request, &input); err != nil {
		writeError(response, http.StatusBadRequest, "invalid_request")
		return
	}
	group, err := h.groups.Create(request.Context(), credential, input.Name, input.Timezone)
	if writeGroupError(response, err) {
		return
	}
	writeJSON(response, http.StatusCreated, group)
}

func (h *Handler) listGroups(response http.ResponseWriter, request *http.Request) {
	credential, ok := bearerCredential(request)
	if !ok {
		writeError(response, http.StatusUnauthorized, "unauthorized")
		return
	}
	groups, err := h.groups.List(request.Context(), credential)
	if writeGroupError(response, err) {
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{"groups": groups})
}

func (h *Handler) getGroup(response http.ResponseWriter, request *http.Request) {
	credential, ok := bearerCredential(request)
	if !ok {
		writeError(response, http.StatusUnauthorized, "unauthorized")
		return
	}
	detail, err := h.groups.Get(request.Context(), credential, request.PathValue("group_id"))
	if writeGroupError(response, err) {
		return
	}
	writeJSON(response, http.StatusOK, detail)
}

func (h *Handler) createGroupInvite(response http.ResponseWriter, request *http.Request) {
	credential, ok := bearerCredential(request)
	if !ok {
		writeError(response, http.StatusUnauthorized, "unauthorized")
		return
	}
	var input struct {
		ExpiresInSeconds int64 `json:"expires_in_seconds"`
		MaxUses          int   `json:"max_uses"`
	}
	if err := decodeRequest(response, request, &input); err != nil || input.ExpiresInSeconds < 300 || input.ExpiresInSeconds > 30*24*60*60 {
		writeError(response, http.StatusBadRequest, "invalid_request")
		return
	}
	invite, err := h.groups.Invite(
		request.Context(), credential, request.PathValue("group_id"),
		time.Duration(input.ExpiresInSeconds)*time.Second, input.MaxUses,
	)
	if writeGroupError(response, err) {
		return
	}
	writeJSON(response, http.StatusCreated, invite)
}

func (h *Handler) joinGroup(response http.ResponseWriter, request *http.Request) {
	credential, ok := bearerCredential(request)
	if !ok {
		writeError(response, http.StatusUnauthorized, "unauthorized")
		return
	}
	var input struct {
		Code string `json:"code"`
	}
	if err := decodeRequest(response, request, &input); err != nil {
		writeError(response, http.StatusBadRequest, "invalid_request")
		return
	}
	group, err := h.groups.Join(request.Context(), credential, input.Code)
	if writeGroupError(response, err) {
		return
	}
	writeJSON(response, http.StatusOK, group)
}

func (h *Handler) leaveGroup(response http.ResponseWriter, request *http.Request) {
	credential, ok := bearerCredential(request)
	if !ok {
		writeError(response, http.StatusUnauthorized, "unauthorized")
		return
	}
	err := h.groups.Leave(request.Context(), credential, request.PathValue("group_id"))
	if writeGroupError(response, err) {
		return
	}
	response.WriteHeader(http.StatusNoContent)
}

func (h *Handler) revokeGroupMember(response http.ResponseWriter, request *http.Request) {
	credential, ok := bearerCredential(request)
	if !ok {
		writeError(response, http.StatusUnauthorized, "unauthorized")
		return
	}
	err := h.groups.Revoke(request.Context(), credential, request.PathValue("group_id"), request.PathValue("user_id"))
	if writeGroupError(response, err) {
		return
	}
	response.WriteHeader(http.StatusNoContent)
}

func writeGroupError(response http.ResponseWriter, err error) bool {
	if err == nil {
		return false
	}
	switch {
	case errors.Is(err, serverauth.ErrUnauthorized):
		writeError(response, http.StatusUnauthorized, "unauthorized")
	case errors.Is(err, servergroup.ErrNotFound), errors.Is(err, servergroup.ErrInvalidInvite):
		writeError(response, http.StatusNotFound, "group_not_found")
	case errors.Is(err, servergroup.ErrForbidden):
		writeError(response, http.StatusForbidden, "forbidden")
	case errors.Is(err, servergroup.ErrOwnerMustRemain), errors.Is(err, servergroup.ErrAlreadyMember), errors.Is(err, servergroup.ErrGroupFull),
		errors.Is(err, servergroup.ErrNotMember):
		writeError(response, http.StatusConflict, "group_conflict")
	case errors.Is(err, servergroup.ErrInvalidInput):
		writeError(response, http.StatusBadRequest, "invalid_request")
	default:
		writeError(response, http.StatusInternalServerError, "internal_error")
	}
	return true
}

func (h *Handler) accountUsage(response http.ResponseWriter, request *http.Request) {
	credential, ok := bearerCredential(request)
	if !ok {
		writeError(response, http.StatusUnauthorized, "unauthorized")
		return
	}
	usage, err := h.account.Usage(request.Context(), credential)
	if errors.Is(err, serverauth.ErrUnauthorized) {
		writeError(response, http.StatusUnauthorized, "unauthorized")
		return
	}
	if err != nil {
		writeError(response, http.StatusInternalServerError, "internal_error")
		return
	}
	writeJSON(response, http.StatusOK, usage)
}

func (h *Handler) syncBuckets(response http.ResponseWriter, request *http.Request) {
	credential, ok := bearerCredential(request)
	if !ok {
		writeError(response, http.StatusUnauthorized, "unauthorized")
		return
	}
	var batch protocol.Batch
	if err := decodeRequestLimit(response, request, &batch, maxSyncRequestBytes); err != nil || batch.Validate() != nil {
		writeError(response, http.StatusBadRequest, "invalid_batch")
		return
	}
	result, err := h.sync.Apply(request.Context(), credential, batch)
	switch {
	case errors.Is(err, serverauth.ErrUnauthorized):
		writeError(response, http.StatusUnauthorized, "unauthorized")
	case errors.Is(err, serversync.ErrInvalidBatch):
		writeError(response, http.StatusBadRequest, "invalid_batch")
	case errors.Is(err, serversync.ErrRevisionConflict):
		writeError(response, http.StatusConflict, "revision_conflict")
	case err != nil:
		writeError(response, http.StatusInternalServerError, "internal_error")
	default:
		writeJSON(response, http.StatusOK, result)
	}
}

// retiredSyncV1 answers the protocol 0.1.x clients speak. Those uploads carry
// the client's own model resolution, which the server no longer accepts, so
// the reply tells the user how to upgrade instead of failing validation. It
// reads no body and needs no credential: the answer is the same for everyone.
func (h *Handler) retiredSyncV1(response http.ResponseWriter, _ *http.Request) {
	response.Header().Set("Upgrade", "tokenholic-sync/2")
	writeJSON(response, http.StatusUpgradeRequired, map[string]string{
		"error":   "upgrade_required",
		"message": "This tokenholic version is too old to sync. Upgrade with `npx @tokenholic/cli@latest`; local usage is kept and uploads again after the upgrade.",
	})
}

// ServeHTTP applies stable security headers before routing.
func (h *Handler) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Cache-Control", "no-store")
	response.Header().Set("Content-Type", "application/json")
	response.Header().Set("X-Content-Type-Options", "nosniff")
	h.mux.ServeHTTP(response, request)
}

func (h *Handler) health(response http.ResponseWriter, _ *http.Request) {
	writeJSON(response, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *Handler) startDevice(response http.ResponseWriter, request *http.Request) {
	var input struct {
		ClientVersion string `json:"client_version"`
	}
	if err := decodeRequest(response, request, &input); err != nil {
		writeError(response, http.StatusBadRequest, "invalid_request")
		return
	}
	started, err := h.auth.Start(request.Context(), input.ClientVersion)
	if errors.Is(err, serverauth.ErrInvalidInput) {
		writeError(response, http.StatusBadRequest, "invalid_request")
		return
	} else if err != nil {
		writeError(response, http.StatusBadGateway, "authorization_unavailable")
		return
	}
	writeJSON(response, http.StatusCreated, map[string]any{
		"flow_token": started.FlowToken, "user_code": started.UserCode,
		"verification_uri": started.VerificationURI,
		"expires_in":       durationSeconds(started.ExpiresIn),
		"interval":         durationSeconds(started.Interval),
	})
}

func (h *Handler) pollDevice(response http.ResponseWriter, request *http.Request) {
	var input struct {
		FlowToken string `json:"flow_token"`
	}
	if err := decodeRequest(response, request, &input); err != nil || input.FlowToken == "" {
		writeError(response, http.StatusBadRequest, "invalid_request")
		return
	}
	result, err := h.auth.Poll(request.Context(), input.FlowToken)
	if err != nil {
		writeError(response, http.StatusBadGateway, "authorization_unavailable")
		return
	}
	switch result.Status {
	case serverauth.FlowPending, serverauth.FlowSlowDown:
		retry := result.RetryAfter
		if retry <= 0 {
			retry = 5 * time.Second
		}
		response.Header().Set("Retry-After", strconv.FormatInt(durationSeconds(retry), 10))
		writeJSON(response, http.StatusAccepted, map[string]any{
			"status": result.Status, "retry_after": durationSeconds(retry),
		})
	case serverauth.FlowExpired:
		writeError(response, http.StatusGone, "authorization_expired")
	case serverauth.FlowDenied:
		writeError(response, http.StatusForbidden, "authorization_denied")
	case serverauth.FlowApproved:
		if result.Session == nil {
			writeError(response, http.StatusInternalServerError, "internal_error")
			return
		}
		writeJSON(response, http.StatusOK, result.Session)
	default:
		writeError(response, http.StatusBadGateway, "authorization_unavailable")
	}
}

func (h *Handler) refresh(response http.ResponseWriter, request *http.Request) {
	credential, ok := bearerCredential(request)
	if !ok {
		writeError(response, http.StatusUnauthorized, "unauthorized")
		return
	}
	session, err := h.auth.Refresh(request.Context(), credential)
	if errors.Is(err, serverauth.ErrUnauthorized) {
		writeError(response, http.StatusUnauthorized, "unauthorized")
		return
	}
	if err != nil {
		writeError(response, http.StatusInternalServerError, "internal_error")
		return
	}
	writeJSON(response, http.StatusOK, session)
}

func (h *Handler) logout(response http.ResponseWriter, request *http.Request) {
	credential, ok := bearerCredential(request)
	if !ok {
		writeError(response, http.StatusUnauthorized, "unauthorized")
		return
	}
	if err := h.auth.Logout(request.Context(), credential); errors.Is(err, serverauth.ErrUnauthorized) {
		writeError(response, http.StatusUnauthorized, "unauthorized")
		return
	} else if err != nil {
		writeError(response, http.StatusInternalServerError, "internal_error")
		return
	}
	response.WriteHeader(http.StatusNoContent)
}

func decodeRequest(response http.ResponseWriter, request *http.Request, target any) error {
	return decodeRequestLimit(response, request, target, maxRequestBytes)
}

func decodeRequestLimit(response http.ResponseWriter, request *http.Request, target any, maximum int64) error {
	request.Body = http.MaxBytesReader(response, request.Body, maximum)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("request contains trailing JSON")
	}
	return nil
}

func bearerCredential(request *http.Request) (string, bool) {
	scheme, value, found := strings.Cut(strings.TrimSpace(request.Header.Get("Authorization")), " ")
	return value, found && strings.EqualFold(scheme, "Bearer") && value != ""
}

func durationSeconds(duration time.Duration) int64 {
	seconds := int64(duration / time.Second)
	if duration%time.Second != 0 {
		seconds++
	}
	if seconds < 1 {
		return 1
	}
	return seconds
}

func writeError(response http.ResponseWriter, status int, code string) {
	writeJSON(response, status, map[string]string{"error": code})
}

func writeJSON(response http.ResponseWriter, status int, value any) {
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(value)
}
