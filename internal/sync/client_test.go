package sync

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/ansonnchan/tokenholic/internal/store"
)

func TestClientRetriesBatchesAndAcknowledgesExactRevisions(t *testing.T) {
	outbox := &fakeOutbox{records: outboxRecords(t, MaxBatchBuckets+1)}
	requests := 0
	transport := syncRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests++
		if request.Header.Get("Authorization") != "Bearer device-credential" {
			t.Fatalf("authorization = %q", request.Header.Get("Authorization"))
		}
		if request.Method != http.MethodPost || request.URL.Path != "/v2/sync/buckets" {
			t.Fatalf("request = %s %s, want POST /v2/sync/buckets", request.Method, request.URL.Path)
		}
		if requests == 1 {
			return syncResponse(request, http.StatusServiceUnavailable, "{}", nil), nil
		}
		var batch Batch
		if err := json.NewDecoder(request.Body).Decode(&batch); err != nil {
			t.Fatal(err)
		}
		response := BatchResponse{Acknowledgements: make([]Acknowledgement, 0, len(batch.Buckets))}
		for _, envelope := range batch.Buckets {
			response.Acknowledgements = append(response.Acknowledgements, Acknowledgement{
				BucketKey: envelope.BucketKey, Revision: envelope.Bucket.Revision, Status: AckApplied,
			})
		}
		encoded, _ := json.Marshal(response)
		return syncResponse(request, http.StatusOK, string(encoded), nil), nil
	})
	client, err := NewClient("https://api.example.test", &http.Client{Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	var waits []time.Duration
	client.sleep = func(_ context.Context, duration time.Duration) error { waits = append(waits, duration); return nil }
	client.jitter = func(time.Duration) time.Duration { return 0 }
	summary, err := client.Run(t.Context(), outbox, "device-credential", nil)
	if err != nil {
		t.Fatal(err)
	}
	if requests != 3 || len(waits) != 1 || summary.Batches != 2 || summary.Buckets != 101 || summary.Acknowledged != 101 || len(outbox.acknowledged) != 101 {
		t.Fatalf("sync requests/waits/summary/acks = %d/%v/%#v/%d", requests, waits, summary, len(outbox.acknowledged))
	}
}

func TestClientDryRunUsesRequestSerializationWithoutNetwork(t *testing.T) {
	outbox := &fakeOutbox{records: outboxRecords(t, MaxBatchBuckets+1)}
	var output strings.Builder
	summary, err := DryRun(t.Context(), outbox, &output)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	if len(lines) != 2 || summary.Batches != 2 || summary.Acknowledged != 0 || len(outbox.acknowledged) != 0 {
		t.Fatalf("dry run = lines:%d summary:%#v acks:%d", len(lines), summary, len(outbox.acknowledged))
	}
	for _, line := range lines {
		var batch Batch
		if err := json.Unmarshal([]byte(line), &batch); err != nil || batch.Validate() != nil {
			t.Fatalf("dry-run body = %q, %v", line, err)
		}
	}
}

// show-data must print exactly what a real sync sends, byte for byte.
func TestDryRunOutputMatchesTheUploadedRequestBodies(t *testing.T) {
	records := outboxRecords(t, MaxBatchBuckets+1)
	var uploaded []string
	transport := syncRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Fatal(err)
		}
		uploaded = append(uploaded, string(body))
		var batch Batch
		if err := json.Unmarshal(body, &batch); err != nil {
			t.Fatal(err)
		}
		response := BatchResponse{}
		for _, envelope := range batch.Buckets {
			response.Acknowledgements = append(response.Acknowledgements, Acknowledgement{
				BucketKey: envelope.BucketKey, Revision: envelope.Bucket.Revision, Status: AckApplied,
			})
		}
		encoded, _ := json.Marshal(response)
		return syncResponse(request, http.StatusOK, string(encoded), nil), nil
	})
	client, err := NewClient("https://api.example.test", &http.Client{Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Run(t.Context(), &fakeOutbox{records: records}, "device-credential", nil); err != nil {
		t.Fatal(err)
	}
	var preview strings.Builder
	if _, err := DryRun(t.Context(), &fakeOutbox{records: records}, &preview); err != nil {
		t.Fatal(err)
	}
	if got, want := preview.String(), strings.Join(uploaded, "\n")+"\n"; got != want {
		t.Fatalf("show-data output differs from the uploaded request bodies:\npreview: %s\nuploaded: %s", got, want)
	}
}

func TestClientReportsUpgradeRequiredWithoutRetryOrAcknowledgement(t *testing.T) {
	outbox := &fakeOutbox{records: outboxRecords(t, 1)}
	requests := 0
	transport := syncRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests++
		return syncResponse(request, http.StatusUpgradeRequired, `{"error":"upgrade_required"}`, nil), nil
	})
	client, err := NewClient("https://api.example.test", &http.Client{Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	client.sleep = func(context.Context, time.Duration) error { t.Fatal("client retried a retired protocol"); return nil }
	_, err = client.Run(t.Context(), outbox, "device-credential", nil)
	if !errors.Is(err, ErrUpgradeRequired) || !strings.Contains(err.Error(), "npx @tokenholic/cli@latest") {
		t.Fatalf("Run() error = %v, want an upgrade instruction", err)
	}
	if requests != 1 || len(outbox.acknowledged) != 0 {
		t.Fatalf("requests/acknowledged = %d/%d, want 1/0 so usage stays pending", requests, len(outbox.acknowledged))
	}
}

func outboxRecords(t *testing.T, count int) []store.OutboxRecord {
	t.Helper()
	records := make([]store.OutboxRecord, 0, count)
	for index := 0; index < count; index++ {
		envelope := sampleEnvelope(index, uint64(index+1))
		payload, err := json.Marshal(envelope)
		if err != nil {
			t.Fatal(err)
		}
		records = append(records, store.OutboxRecord{BucketKey: envelope.BucketKey, Revision: envelope.Bucket.Revision, Payload: payload})
	}
	return records
}

type fakeOutbox struct {
	records      []store.OutboxRecord
	acknowledged []store.OutboxAcknowledgement
}

func (f *fakeOutbox) PendingOutbox(context.Context) ([]store.OutboxRecord, error) {
	return append([]store.OutboxRecord(nil), f.records...), nil
}
func (f *fakeOutbox) AcknowledgeOutbox(_ context.Context, values []store.OutboxAcknowledgement) (int64, error) {
	f.acknowledged = append(f.acknowledged, values...)
	return int64(len(values)), nil
}

type syncRoundTripFunc func(*http.Request) (*http.Response, error)

func (function syncRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func syncResponse(request *http.Request, status int, body string, headers http.Header) *http.Response {
	if headers == nil {
		headers = make(http.Header)
	}
	return &http.Response{StatusCode: status, Header: headers, Body: io.NopCloser(strings.NewReader(body)), Request: request}
}

// acceptingTransport acknowledges every batch until it has answered
// `accept` requests, then answers with `after`.
func acceptingTransport(t *testing.T, accept, after int, headers http.Header) (syncRoundTripFunc, *int) {
	t.Helper()
	requests := 0
	return func(request *http.Request) (*http.Response, error) {
		requests++
		if requests > accept {
			return syncResponse(request, after, "{}", headers), nil
		}
		var batch Batch
		if err := json.NewDecoder(request.Body).Decode(&batch); err != nil {
			t.Fatal(err)
		}
		response := BatchResponse{Acknowledgements: make([]Acknowledgement, 0, len(batch.Buckets))}
		for _, envelope := range batch.Buckets {
			response.Acknowledgements = append(response.Acknowledgements, Acknowledgement{
				BucketKey: envelope.BucketKey, Revision: envelope.Bucket.Revision, Status: AckApplied,
			})
		}
		encoded, _ := json.Marshal(response)
		return syncResponse(request, http.StatusOK, string(encoded), nil), nil
	}, &requests
}

// A pass that stops partway must say how far it got: the caller tells the
// user, and the next pass sends only the rest.
func TestClientReportsProgressAndWhatWasAcceptedBeforeItStopped(t *testing.T) {
	outbox := &fakeOutbox{records: outboxRecords(t, 2*MaxBatchBuckets+50)}
	transport, requests := acceptingTransport(t, 2, http.StatusBadRequest, nil)
	client, err := NewClient("https://api.example.test", &http.Client{Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	var seen []Progress
	summary, err := client.Run(t.Context(), outbox, "device-credential", func(progress Progress) { seen = append(seen, progress) })
	if err == nil {
		t.Fatal("Run() succeeded although the third batch was refused")
	}
	if *requests != 3 || summary.Acknowledged != 200 || summary.Pending != 250 || summary.Batches != 2 || len(outbox.acknowledged) != 200 {
		t.Fatalf("requests/summary/acknowledged = %d/%#v/%d", *requests, summary, len(outbox.acknowledged))
	}
	want := []Progress{
		{Batch: 0, Batches: 3, Acknowledged: 0, Pending: 250},
		{Batch: 1, Batches: 3, Acknowledged: 100, Pending: 250},
		{Batch: 2, Batches: 3, Acknowledged: 200, Pending: 250},
	}
	if len(seen) != len(want) {
		t.Fatalf("progress = %#v, want %#v", seen, want)
	}
	for index := range want {
		if seen[index] != want[index] {
			t.Errorf("progress[%d] = %#v, want %#v", index, seen[index], want[index])
		}
	}
}

// A pass has no deadline, so a server cannot be allowed to park it.
func TestClientCapsTheDelayAServerAsksFor(t *testing.T) {
	outbox := &fakeOutbox{records: outboxRecords(t, 1)}
	transport, _ := acceptingTransport(t, 0, http.StatusTooManyRequests, http.Header{"Retry-After": []string{"86400"}})
	client, err := NewClient("https://api.example.test", &http.Client{Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	var waits []time.Duration
	client.sleep = func(_ context.Context, duration time.Duration) error { waits = append(waits, duration); return nil }
	if _, err := client.Run(t.Context(), outbox, "device-credential", nil); err == nil {
		t.Fatal("Run() succeeded against a server that only rate-limits")
	}
	if len(waits) != 1 || waits[0] != maxRetryDelay {
		t.Fatalf("waits = %v, want one of %v", waits, maxRetryDelay)
	}
}

func TestDryRunReportsNoUploadProgress(t *testing.T) {
	outbox := &fakeOutbox{records: outboxRecords(t, MaxBatchBuckets+1)}
	summary, err := runBatches(t.Context(), outbox, io.Discard, func(Progress) { t.Fatal("a dry run reported upload progress") }, nil)
	if err != nil || summary.Batches != 2 || summary.Pending != 101 || summary.Acknowledged != 0 {
		t.Fatalf("dry run = %#v/%v", summary, err)
	}
}
