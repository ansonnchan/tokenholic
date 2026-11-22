package sync

import (
	"bytes"
	"context"
	cryptorand "crypto/rand"
	"encoding/binary"
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

	"github.com/ansonnchan/tokenholic/internal/store"
)

const (
	maxSyncResponseBytes = 1 << 20
	baseRetryDelay       = 200 * time.Millisecond
	// maxRetryDelay caps how long a server can make one pass wait. A pass
	// has no overall deadline, so nothing else would.
	maxRetryDelay = time.Minute
)

// ErrUpgradeRequired means the server no longer accepts this client's upload
// protocol. Local usage is unaffected and stays pending until the upgrade.
var ErrUpgradeRequired = errors.New("synchronize usage: this tokenholic version is too old to sync; upgrade with `npx @tokenholic/cli@latest`")

// Outbox is the local absolute-revision boundary consumed by Client.
type Outbox interface {
	PendingOutbox(context.Context) ([]store.OutboxRecord, error)
	AcknowledgeOutbox(context.Context, []store.OutboxAcknowledgement) (int64, error)
}

// Summary reports one synchronization or dry-run pass. A pass that stops on
// an error still reports what it acknowledged before stopping.
type Summary struct {
	Batches      int
	Buckets      int
	Acknowledged int64
	// Pending is how many buckets were waiting when the pass began.
	Pending int
}

// Progress reports an upload pass: once before the first request, with
// Batch zero, and once after each acknowledged batch.
type Progress struct {
	Batch, Batches int
	// Acknowledged counts the buckets the server has accepted so far, out
	// of the Pending that were waiting when the pass began.
	Acknowledged int64
	Pending      int
}

// Client uploads bounded batches and retries transient failure once. A pass
// has no deadline of its own: every request is bounded and retried at most
// once, so a pass either acknowledges another batch or stops, and a long
// history is uploaded in one run however slow each batch is.
type Client struct {
	baseURL    *url.URL
	httpClient *http.Client
	sleep      func(context.Context, time.Duration) error
	jitter     func(time.Duration) time.Duration
}

// NewClient validates the API origin. Plain HTTP is accepted only on loopback.
func NewClient(baseURL string, httpClient *http.Client) (*Client, error) {
	parsed, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("invalid tokenholic API URL")
	}
	if parsed.Scheme != "https" && !(parsed.Scheme == "http" && syncLoopbackHost(parsed.Hostname())) {
		return nil, errors.New("tokenholic API URL must use HTTPS or loopback HTTP")
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	clientCopy := *httpClient
	clientCopy.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	return &Client{
		baseURL: parsed, httpClient: &clientCopy,
		sleep: sleepSyncContext, jitter: cryptoJitter,
	}, nil
}

// Run uploads pending revisions and acknowledges the exact revisions accepted
// by the server. Each batch is committed locally as it is acknowledged, so a
// pass that stops partway loses nothing and the next one continues from
// there. progress, when not nil, is told how far the pass has got.
func (c *Client) Run(ctx context.Context, outbox Outbox, credential string, progress func(Progress)) (Summary, error) {
	if outbox == nil || credential == "" {
		return Summary{}, errors.New("synchronize usage: outbox and credential are required")
	}
	return runBatches(ctx, outbox, nil, progress, func(body []byte) (BatchResponse, error) {
		return c.sendWithRetry(ctx, credential, body)
	})
}

// DryRun writes the exact request body for each pending batch without needing
// an API origin, account, or device credential.
func DryRun(ctx context.Context, outbox Outbox, output io.Writer) (Summary, error) {
	if outbox == nil || output == nil {
		return Summary{}, errors.New("show usage data: outbox and output are required")
	}
	return runBatches(ctx, outbox, output, nil, nil)
}

func runBatches(ctx context.Context, outbox Outbox, output io.Writer, progress func(Progress), send func([]byte) (BatchResponse, error)) (Summary, error) {
	records, err := outbox.PendingOutbox(ctx)
	if err != nil {
		return Summary{}, err
	}
	summary := Summary{Pending: len(records)}
	batches := (len(records) + MaxBatchBuckets - 1) / MaxBatchBuckets
	report := func() {
		if progress != nil && send != nil {
			progress(Progress{Batch: summary.Batches, Batches: batches, Acknowledged: summary.Acknowledged, Pending: summary.Pending})
		}
	}
	report()
	for offset := 0; offset < len(records); offset += MaxBatchBuckets {
		end := offset + MaxBatchBuckets
		if end > len(records) {
			end = len(records)
		}
		batch, body, err := BuildBatch(records[offset:end])
		if err != nil {
			return summary, err
		}
		if send == nil {
			if _, err := output.Write(append(body, '\n')); err != nil {
				return summary, fmt.Errorf("write sync dry run: %w", err)
			}
			summary.Batches++
			summary.Buckets += len(batch.Buckets)
			continue
		}
		response, err := send(body)
		if err != nil {
			return summary, err
		}
		acknowledgements, err := validateAcknowledgements(batch, response)
		if err != nil {
			return summary, err
		}
		count, err := outbox.AcknowledgeOutbox(ctx, acknowledgements)
		if err != nil {
			return summary, err
		}
		// Counted only once the batch is committed locally, so a pass that
		// stops reports exactly what the next one will not send again.
		summary.Batches++
		summary.Buckets += len(batch.Buckets)
		summary.Acknowledged += count
		report()
	}
	return summary, nil
}

func (c *Client) sendWithRetry(ctx context.Context, credential string, body []byte) (BatchResponse, error) {
	for attempt := 0; attempt < 2; attempt++ {
		response, retryAfter, retry, err := c.send(ctx, credential, body)
		if !retry {
			return response, err
		}
		if attempt == 1 {
			if err != nil {
				return BatchResponse{}, err
			}
			return BatchResponse{}, errors.New("synchronize usage: server remained unavailable after retry")
		}
		if retryAfter <= 0 {
			retryAfter = baseRetryDelay + c.jitter(baseRetryDelay/2)
		}
		retryAfter = min(retryAfter, maxRetryDelay)
		if err := c.sleep(ctx, retryAfter); err != nil {
			return BatchResponse{}, fmt.Errorf("wait to retry synchronization: %w", err)
		}
	}
	return BatchResponse{}, errors.New("synchronize usage: retry loop exhausted")
}

func (c *Client) send(ctx context.Context, credential string, body []byte) (BatchResponse, time.Duration, bool, error) {
	endpoint := *c.baseURL
	endpoint.Path += BucketsPath
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(body))
	if err != nil {
		return BatchResponse{}, 0, false, fmt.Errorf("create sync request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Authorization", "Bearer "+credential)
	request.Header.Set("Content-Type", "application/json")
	response, err := c.httpClient.Do(request)
	if err != nil {
		return BatchResponse{}, 0, true, fmt.Errorf("upload usage batch: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusTooManyRequests || response.StatusCode >= 500 {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxSyncResponseBytes))
		return BatchResponse{}, retryAfterDuration(response.Header.Get("Retry-After")), true, nil
	}
	if response.StatusCode == http.StatusUnauthorized {
		return BatchResponse{}, 0, false, errors.New("synchronize usage: device credential is unauthorized")
	}
	if response.StatusCode == http.StatusUpgradeRequired {
		return BatchResponse{}, 0, false, ErrUpgradeRequired
	}
	if response.StatusCode == http.StatusConflict {
		return BatchResponse{}, 0, false, errors.New("synchronize usage: equal revision conflicts with server payload")
	}
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxSyncResponseBytes))
		return BatchResponse{}, 0, false, fmt.Errorf("synchronize usage: server returned status %d", response.StatusCode)
	}
	var result BatchResponse
	decoder := json.NewDecoder(io.LimitReader(response.Body, maxSyncResponseBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil {
		return BatchResponse{}, 0, false, fmt.Errorf("decode sync acknowledgement: %w", err)
	}
	return result, 0, false, nil
}

func validateAcknowledgements(batch Batch, response BatchResponse) ([]store.OutboxAcknowledgement, error) {
	expected := make(map[string]uint64, len(batch.Buckets))
	for _, envelope := range batch.Buckets {
		expected[envelope.BucketKey] = envelope.Bucket.Revision
	}
	acknowledgements := make([]store.OutboxAcknowledgement, 0, len(response.Acknowledgements))
	for _, acknowledgement := range response.Acknowledgements {
		revision, found := expected[acknowledgement.BucketKey]
		if !found || revision != acknowledgement.Revision {
			return nil, errors.New("sync acknowledgement does not match request")
		}
		if acknowledgement.Status != AckApplied && acknowledgement.Status != AckUnchanged && acknowledgement.Status != AckStale {
			return nil, errors.New("sync acknowledgement has invalid status")
		}
		delete(expected, acknowledgement.BucketKey)
		acknowledgements = append(acknowledgements, store.OutboxAcknowledgement{
			BucketKey: acknowledgement.BucketKey, Revision: acknowledgement.Revision,
		})
	}
	if len(expected) != 0 {
		return nil, errors.New("sync acknowledgement is incomplete")
	}
	return acknowledgements, nil
}

func syncLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	address := net.ParseIP(host)
	return address != nil && address.IsLoopback()
}

func retryAfterDuration(value string) time.Duration {
	seconds, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil || seconds <= 0 {
		return 0
	}
	return time.Duration(seconds) * time.Second
}

func cryptoJitter(maximum time.Duration) time.Duration {
	if maximum <= 0 {
		return 0
	}
	var raw [8]byte
	if _, err := cryptorand.Read(raw[:]); err != nil {
		return 0
	}
	return time.Duration(binary.LittleEndian.Uint64(raw[:]) % uint64(maximum))
}

func sleepSyncContext(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
