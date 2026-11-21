package main

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/ansonnchan/tokenholic/internal/modelcatalog"
	serveraccount "github.com/ansonnchan/tokenholic/server/account"
	serverauth "github.com/ansonnchan/tokenholic/server/auth"
	servergroup "github.com/ansonnchan/tokenholic/server/group"
	"github.com/ansonnchan/tokenholic/server/httpapi"
	serverleaderboard "github.com/ansonnchan/tokenholic/server/leaderboard"
	"github.com/ansonnchan/tokenholic/server/postgres"
	serverpricing "github.com/ansonnchan/tokenholic/server/pricing"
	"github.com/ansonnchan/tokenholic/server/ratelimit"
	serversync "github.com/ansonnchan/tokenholic/server/sync"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "tokenholic api:", err)
		os.Exit(1)
	}
}

func run() error {
	databaseURL := strings.TrimSpace(os.Getenv("DATABASE_URL"))
	githubClientID := strings.TrimSpace(os.Getenv("GITHUB_CLIENT_ID"))
	stateKey, err := decodeStateKey(os.Getenv("TOKENHOLIC_AUTH_STATE_KEY"))
	if err != nil {
		return err
	}
	if githubClientID == "" {
		return errors.New("GITHUB_CLIENT_ID is required")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	pool, err := postgres.Open(ctx, databaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := postgres.Migrate(ctx, pool); err != nil {
		return err
	}
	catalog, pricing, err := modelcatalog.Bundled()
	if err != nil {
		return fmt.Errorf("load bundled pricing: %w", err)
	}
	deriver, err := modelcatalog.NewDeriver(catalog, pricing)
	if err != nil {
		return err
	}
	pricer, err := serverpricing.NewEngine(deriver)
	if err != nil {
		return err
	}
	if err := postgres.EnsurePricingRevision(ctx, pool, pricing); err != nil {
		return err
	}
	// Bring stored history to this build's prices before serving any request,
	// so no response ever mixes two pricing revisions.
	repriced, err := postgres.RepriceUsage(ctx, pool, pricer)
	if err != nil {
		return fmt.Errorf("reprice stored usage: %w", err)
	}
	if repriced.Repriced > 0 {
		fmt.Fprintf(os.Stderr, "tokenholic api: repriced %d usage buckets to pricing revision %s\n", repriced.Repriced, repriced.Revision)
	}
	repository, err := postgres.NewAuthRepository(pool)
	if err != nil {
		return err
	}
	github, err := serverauth.NewGitHubClient(&http.Client{Timeout: 15 * time.Second}, githubClientID)
	if err != nil {
		return err
	}
	authentication, err := serverauth.NewService(github, repository, stateKey)
	if err != nil {
		return err
	}
	usageRepository, err := postgres.NewUsageRepository(pool)
	if err != nil {
		return err
	}
	synchronization, err := serversync.NewService(authentication, usageRepository, pricer)
	if err != nil {
		return err
	}
	accountRepository, err := postgres.NewAccountRepository(pool)
	if err != nil {
		return err
	}
	accountUsage, err := serveraccount.NewService(authentication, accountRepository)
	if err != nil {
		return err
	}
	groupRepository, err := postgres.NewGroupRepository(pool)
	if err != nil {
		return err
	}
	groups, err := servergroup.NewService(authentication, groupRepository)
	if err != nil {
		return err
	}
	// Invite links point at the website. Without its origin the API still
	// returns invite codes, just no links.
	if origin := strings.TrimSpace(os.Getenv("TOKENHOLIC_WEB_ORIGIN")); origin != "" {
		if err := groups.SetWebOrigin(origin); err != nil {
			return fmt.Errorf("TOKENHOLIC_WEB_ORIGIN: %w", err)
		}
	}
	leaderboardRepository, err := postgres.NewLeaderboardRepository(pool)
	if err != nil {
		return err
	}
	leaderboards, err := serverleaderboard.NewService(authentication, leaderboardRepository)
	if err != nil {
		return err
	}
	handler, err := httpapi.New(authentication, synchronization, accountUsage, groups, leaderboards)
	if err != nil {
		return err
	}
	limited, err := rateLimited(handler, os.Getenv)
	if err != nil {
		return err
	}
	address, err := listenAddress(os.Getenv)
	if err != nil {
		return err
	}
	server := &http.Server{
		Addr: address, Handler: limited,
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second,
		WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second,
	}
	result := make(chan error, 1)
	go func() {
		result <- server.ListenAndServe()
	}()
	select {
	case err := <-result:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("serve HTTP API: %w", err)
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("shut down HTTP API: %w", err)
		}
		return nil
	}
}

// The limits below are sized for the single demo instance. A first sync of a
// long history is the heaviest legitimate client: it uploads 100 buckets per
// request, so the burst covers 30,000 buckets before the client has to slow
// to the refill rate. The server's documentation lists these values.
var (
	// perAddressLimit covers every request from one client address.
	perAddressLimit = ratelimit.Limit{Burst: 300, Every: 200 * time.Millisecond}
	// perCredentialLimit covers every request presenting one credential.
	perCredentialLimit = ratelimit.Limit{Burst: 300, Every: 200 * time.Millisecond}
	// deviceStartLimit covers starting a device login, which calls GitHub
	// without any credential. A login needs one start.
	deviceStartLimit = ratelimit.Limit{Burst: 10, Every: 6 * time.Second}
)

// rateLimited wraps the API in the in-memory limiter. Its state is local to
// this process, which is correct only while one instance serves the API.
func rateLimited(handler http.Handler, getenv func(string) string) (http.Handler, error) {
	hops, err := trustedProxyHops(getenv)
	if err != nil {
		return nil, err
	}
	limits := httpapi.RateLimits{TrustedProxyHops: hops}
	for _, limiter := range []struct {
		target *httpapi.Allower
		limit  ratelimit.Limit
	}{
		{&limits.PerAddress, perAddressLimit}, {&limits.PerCredential, perCredentialLimit}, {&limits.DeviceStart, deviceStartLimit},
	} {
		created, err := ratelimit.New(limiter.limit, 0, nil)
		if err != nil {
			return nil, err
		}
		*limiter.target = created
	}
	return httpapi.RateLimited(handler, limits)
}

// trustedProxyHops reads how many reverse proxies in front of the API append
// to X-Forwarded-For. Unset means none: the connection's address is the
// client. It must match the deployment, so an invalid value stops startup
// instead of silently trusting or ignoring the header.
func trustedProxyHops(getenv func(string) string) (int, error) {
	value := strings.TrimSpace(getenv("TOKENHOLIC_TRUSTED_PROXY_HOPS"))
	if value == "" {
		return 0, nil
	}
	hops, err := strconv.Atoi(value)
	if err != nil || hops < 0 || hops > httpapi.MaxTrustedProxyHops {
		return 0, fmt.Errorf("TOKENHOLIC_TRUSTED_PROXY_HOPS must be an integer between 0 and %d", httpapi.MaxTrustedProxyHops)
	}
	return hops, nil
}

func listenAddress(getenv func(string) string) (string, error) {
	if address := strings.TrimSpace(getenv("TOKENHOLIC_LISTEN_ADDR")); address != "" {
		return address, nil
	}
	port := strings.TrimSpace(getenv("PORT"))
	if port == "" {
		return "127.0.0.1:8080", nil
	}
	value, err := strconv.ParseUint(port, 10, 16)
	if err != nil || value == 0 {
		return "", errors.New("PORT must be an integer between 1 and 65535")
	}
	return net.JoinHostPort("0.0.0.0", port), nil
}

func decodeStateKey(encoded string) ([]byte, error) {
	value := strings.TrimSpace(encoded)
	key, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		key, err = base64.RawStdEncoding.DecodeString(value)
	}
	if err != nil || len(key) != 32 {
		return nil, errors.New("TOKENHOLIC_AUTH_STATE_KEY must be base64 for exactly 32 bytes")
	}
	return key, nil
}
