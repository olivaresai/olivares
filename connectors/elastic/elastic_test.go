// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package elastic

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

func sampleNotification() sdk.Notification {
	return sdk.Notification{
		Type:     "governance.policy.denied",
		Title:    "policy denied tool call",
		Body:     "agent claude-1 blocked from write",
		Severity: model.SeverityHigh,
		Tenant:   "acme",
		Fields:   map[string]string{"agent": "claude-1", "decision": "deny"},
		Time:     time.Date(2026, 6, 6, 10, 0, 0, 0, time.UTC),
	}
}

type capture struct {
	method      string
	path        string
	contentType string
	auth        string
	body        []byte
}

func newServer(t *testing.T, status int, respBody string, cap *capture) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cap.method = r.Method
		cap.path = r.URL.Path
		cap.contentType = r.Header.Get("Content-Type")
		cap.auth = r.Header.Get("Authorization")
		cap.body, _ = io.ReadAll(r.Body)
		w.WriteHeader(status)
		_, _ = io.WriteString(w, respBody)
	}))
}

func TestNotifyBulkRoundTrip(t *testing.T) {
	var cap capture
	srv := newServer(t, http.StatusOK, `{"took":3,"errors":false,"items":[{"create":{"status":201}}]}`, &cap)
	defer srv.Close()

	o := New()
	if err := o.Open(context.Background(), sdk.Config{Settings: map[string]string{
		"endpoint": srv.URL, "index": "logs-olivares-default", "api_key": "theKey",
	}}); err != nil {
		t.Fatal(err)
	}
	defer o.Close(context.Background())
	if err := o.Notify(context.Background(), sampleNotification()); err != nil {
		t.Fatalf("Notify: %v", err)
	}

	if cap.method != http.MethodPost {
		t.Errorf("method = %q, want POST", cap.method)
	}
	if cap.path != "/_bulk" {
		t.Errorf("path = %q, want /_bulk", cap.path)
	}
	if cap.contentType != "application/x-ndjson" {
		t.Errorf("content-type = %q, want application/x-ndjson", cap.contentType)
	}
	if cap.auth != "ApiKey theKey" {
		t.Errorf("auth = %q, want ApiKey theKey", cap.auth)
	}

	// The _bulk body is NDJSON: an action line, a source line, and a trailing
	// newline. Assert exactly two non-empty JSON lines plus the trailing newline.
	if n := len(cap.body); n == 0 || cap.body[n-1] != '\n' {
		t.Fatalf("body must end with a trailing newline; got %q", cap.body)
	}
	lines := bytes.Split(cap.body, []byte("\n"))
	// Split on the trailing newline yields a final empty element.
	if len(lines) != 3 || len(lines[2]) != 0 {
		t.Fatalf("body must be exactly two JSON lines + trailing newline; got %d segments: %q", len(lines), cap.body)
	}
	if len(lines[0]) == 0 || len(lines[1]) == 0 {
		t.Fatalf("both NDJSON lines must be non-empty; got %q", cap.body)
	}

	// Line 1 is the action line: {"create":{"_index":"<index>"}}.
	var action struct {
		Create struct {
			Index string `json:"_index"`
		} `json:"create"`
	}
	if err := json.Unmarshal(lines[0], &action); err != nil {
		t.Fatalf("action line is not valid JSON: %v (%q)", err, lines[0])
	}
	if action.Create.Index != "logs-olivares-default" {
		t.Errorf("action _index = %q, want logs-olivares-default", action.Create.Index)
	}

	// Line 2 is the ECS source document: an object carrying "@timestamp" and "ecs".
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(lines[1], &doc); err != nil {
		t.Fatalf("source line is not a JSON object: %v (%q)", err, lines[1])
	}
	if _, ok := doc["@timestamp"]; !ok {
		t.Errorf("ECS document missing @timestamp: %q", lines[1])
	}
	if _, ok := doc["ecs"]; !ok {
		t.Errorf("ECS document missing ecs: %q", lines[1])
	}
}

func TestNotifyBearerAuthAndEndpointTrim(t *testing.T) {
	var cap capture
	srv := newServer(t, http.StatusOK, `{"errors":false,"items":[{"create":{"status":201}}]}`, &cap)
	defer srv.Close()

	o := New()
	// Trailing slash on the endpoint must not double the /_bulk path.
	if err := o.Open(context.Background(), sdk.Config{Settings: map[string]string{
		"endpoint": srv.URL + "/", "index": "idx", "bearer": "tok",
	}}); err != nil {
		t.Fatal(err)
	}
	defer o.Close(context.Background())
	if err := o.Notify(context.Background(), sampleNotification()); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	if cap.path != "/_bulk" {
		t.Errorf("path = %q, want /_bulk (no doubling)", cap.path)
	}
	if cap.auth != "Bearer tok" {
		t.Errorf("auth = %q, want Bearer tok", cap.auth)
	}
}

func TestNotifyApiKeyWinsOverBearer(t *testing.T) {
	var cap capture
	srv := newServer(t, http.StatusOK, `{"errors":false,"items":[{"create":{"status":201}}]}`, &cap)
	defer srv.Close()

	o := New()
	if err := o.Open(context.Background(), sdk.Config{Settings: map[string]string{
		"endpoint": srv.URL, "index": "idx", "api_key": "theKey", "bearer": "tok",
	}}); err != nil {
		t.Fatal(err)
	}
	defer o.Close(context.Background())
	if err := o.Notify(context.Background(), sampleNotification()); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	if cap.auth != "ApiKey theKey" {
		t.Errorf("auth = %q, want ApiKey theKey (api_key wins over bearer)", cap.auth)
	}
}

func TestNotifyBulkLogicalErrorIsError(t *testing.T) {
	var cap capture
	srv := newServer(t, http.StatusOK,
		`{"took":1,"errors":true,"items":[{"create":{"status":400,"error":{"type":"mapper_parsing_exception","reason":"bad"}}}]}`,
		&cap)
	defer srv.Close()

	o := New()
	if err := o.Open(context.Background(), sdk.Config{Settings: map[string]string{
		"endpoint": srv.URL, "index": "idx", "api_key": "theKey",
	}}); err != nil {
		t.Fatal(err)
	}
	defer o.Close(context.Background())

	err := o.Notify(context.Background(), sampleNotification())
	if err == nil {
		t.Fatal("a _bulk response with errors:true must surface as an error")
	}
	// The first item's error detail must be present; the credential must not.
	msg := err.Error()
	if !contains(msg, "mapper_parsing_exception") || !contains(msg, "bad") {
		t.Errorf("error = %q, want it to include the item error type+reason", msg)
	}
	if contains(msg, "theKey") {
		t.Errorf("error %q leaks the credential", msg)
	}
}

func TestOpenRejectsBadConfig(t *testing.T) {
	for i, cfg := range []map[string]string{
		{"index": "idx"},          // missing endpoint
		{"endpoint": "https://x"}, // missing index
		{},                        // missing both
	} {
		o := New()
		if err := o.Open(context.Background(), sdk.Config{Settings: cfg}); err == nil {
			t.Errorf("case %d: Open(%v) = nil, want error", i, cfg)
		}
	}
}

func contains(s, sub string) bool {
	return bytes.Contains([]byte(s), []byte(sub))
}

// TestBulkCarriesTheStableIdempotencyKey pins the property that turns
// at-least-once redelivery into effectively-once for this destination.
//
// Without an explicit _id, Elasticsearch mints a fresh identifier for every
// request, so each redelivery — the ordinary consequence of a timeout after the
// write landed, or of a stale-claim rescue — creates ANOTHER copy of the same
// event. Nothing in the response reveals it, and every count drawn from the index
// silently overstates reality afterwards.
func TestBulkCarriesTheStableIdempotencyKey(t *testing.T) {
	var cap capture
	srv := newServer(t, http.StatusOK, `{"took":1,"errors":false,"items":[{"create":{"status":201}}]}`, &cap)
	defer srv.Close()

	o := New()
	if err := o.Open(context.Background(), sdk.Config{Settings: map[string]string{
		"endpoint": srv.URL, "index": "idx", "api_key": "theKey",
	}}); err != nil {
		t.Fatal(err)
	}
	defer o.Close(context.Background())

	n := sampleNotification()
	if n.Fields == nil {
		n.Fields = map[string]string{}
	}
	n.Fields[sdk.IdempotencyKeyField] = "delivery-abc-123"
	if err := o.Notify(context.Background(), n); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(cap.body, []byte(`"_id":"delivery-abc-123"`)) {
		t.Fatalf("the action line must carry the stable delivery key as _id, got: %s", string(cap.body))
	}
}

// TestAConflictOnOurOwnKeyIsNotARejection is the other half of that mechanism.
// A "create" refused with 409 on an _id that IS our stable delivery key means the
// previous attempt already indexed this exact event. Counting that as a refusal
// would dead-letter a notification that is present in the operator's index.
func TestAConflictOnOurOwnKeyIsNotARejection(t *testing.T) {
	outcome, rejected, _, _, _ := classifyBulk([]bulkItem{
		{Create: &bulkItemResult{Status: 409, Error: &bulkItemError{Type: "version_conflict_engine_exception", Reason: "already exists"}}},
	})
	if outcome != sdk.OutcomeDelivered {
		t.Fatalf("a 409 on our own idempotency key means the event already landed, got %s", outcome)
	}
	if rejected != 0 {
		t.Fatalf("rejected = %d, want 0: the document is in the index", rejected)
	}
}

// TestNotifyTreatsOurOwnConflictAsDelivered exercises the WHOLE Notify path, not
// the classifier in isolation.
//
// The earlier test asserted classifyBulk alone and passed while Notify still
// reported a failure: after the classifier correctly read the 409 as "already
// indexed", execution fell through to the generic errors:true handler, which
// returned an error and dead-lettered a document that was in the index. Testing
// the helper proved the helper; it did not prove the behavior.
func TestNotifyTreatsOurOwnConflictAsDelivered(t *testing.T) {
	var cap capture
	srv := newServer(t, http.StatusOK,
		`{"took":1,"errors":true,"items":[{"create":{"status":409,"error":{"type":"version_conflict_engine_exception","reason":"already exists"}}}]}`,
		&cap)
	defer srv.Close()

	o := New()
	if err := o.Open(context.Background(), sdk.Config{Settings: map[string]string{
		"endpoint": srv.URL, "index": "idx", "api_key": "theKey",
	}}); err != nil {
		t.Fatal(err)
	}
	defer o.Close(context.Background())

	n := sampleNotification()
	if n.Fields == nil {
		n.Fields = map[string]string{}
	}
	n.Fields[sdk.IdempotencyKeyField] = "delivery-abc-123"

	if err := o.Notify(context.Background(), n); err != nil {
		t.Fatalf("a version conflict on our own idempotency key means the document is already indexed; reporting it as a failure dead-letters an event that landed: %v", err)
	}
}

// TestOrdinalsNameEveryRefusedPosition keeps the locator honest. Declaring
// LocatorOrdinal promises that a caller can resubmit exactly the refused records;
// carrying only the first position would make that impossible for every rejection
// after the first, while still claiming the precision.
func TestOrdinalsNameEveryRefusedPosition(t *testing.T) {
	outcome, rejected, first, _, ordinals := classifyBulk([]bulkItem{
		{Create: &bulkItemResult{Status: 201}},
		{Create: &bulkItemResult{Status: 400, Error: &bulkItemError{Type: "mapper_parsing_exception"}}},
		{Create: &bulkItemResult{Status: 201}},
		{Create: &bulkItemResult{Status: 400, Error: &bulkItemError{Type: "mapper_parsing_exception"}}},
	})
	if outcome != sdk.OutcomePartial {
		t.Fatalf("outcome = %s, want partial: two of four landed", outcome)
	}
	if rejected != 2 || first != 1 {
		t.Fatalf("rejected=%d first=%d, want 2 and 1", rejected, first)
	}
	if len(ordinals) != 2 || ordinals[0] != 1 || ordinals[1] != 3 {
		t.Fatalf("ordinals = %v, want every refused position [1 3]", ordinals)
	}
}

// TestAMixedBatchIsNotBlindlyRetried pins the interaction that would duplicate
// data. A retryable item makes the whole request retryable ONLY when nothing
// landed; otherwise re-sending the batch re-sends the records that succeeded.
func TestAMixedBatchIsNotBlindlyRetried(t *testing.T) {
	// Some landed, one refused with a retryable status: partial, not unavailable.
	outcome, _, _, _, _ := classifyBulk([]bulkItem{
		{Create: &bulkItemResult{Status: 201}},
		{Create: &bulkItemResult{Status: 429, Error: &bulkItemError{Type: "es_rejected_execution_exception"}}},
	})
	if outcome != sdk.OutcomePartial {
		t.Fatalf("outcome = %s, want partial: retrying the whole batch would duplicate the item that landed", outcome)
	}
	if outcome.Retryable() {
		t.Fatal("a partially accepted batch must never be blindly retried")
	}
	// Nothing landed and the refusal is a full queue: the whole request is retryable.
	outcome, _, _, _, _ = classifyBulk([]bulkItem{
		{Create: &bulkItemResult{Status: 429, Error: &bulkItemError{Type: "es_rejected_execution_exception"}}},
	})
	if outcome != sdk.OutcomeUnavailable {
		t.Fatalf("outcome = %s, want unavailable: Elasticsearch documents 429 as the status to retry", outcome)
	}
}

// TestABulkAnswerMustDescribeOurRequest pins the response invariant. Elasticsearch
// states one item per action line, and this connector sends exactly one. An answer
// carrying a different number is not describing our write — it is Elasticsearch
// answering a request we did not make, or something in front of it answering for
// Elasticsearch. Reading a verdict out of it would let a response about other
// people's documents decide the fate of ours.
func TestABulkAnswerMustDescribeOurRequest(t *testing.T) {
	for _, body := range []string{
		`{"took":1,"errors":false,"items":[]}`,
		`{"took":1,"errors":true,"items":[{"create":{"status":400,"error":{"type":"x"}}},{"create":{"status":400,"error":{"type":"y"}}}]}`,
	} {
		if _, _, _, _, _, ok := bulkVerdict(body); ok {
			t.Fatalf("body %s does not describe a one-line request and must not yield a verdict", body)
		}
	}
	// The shape we actually send is honored.
	if _, _, _, _, _, ok := bulkVerdict(`{"took":1,"errors":false,"items":[{"create":{"status":201}}]}`); !ok {
		t.Fatal("a well-formed one-item answer must yield a verdict")
	}
}

// TestBulkResponseMustCarryTheErrorsMember. Elastic documents "errors" and "items"
// as always present. As a plain bool an ABSENT member was indistinguishable from
// errors:false, so a document carrying items but no "errors" — which _bulk never
// produces, and therefore is not Elasticsearch answering — was read as a clean
// acceptance of our document.
func TestBulkResponseMustCarryTheErrorsMember(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		wantErr    bool
	}{
		{name: "errors absent", body: `{"took":1,"items":[{"create":{"status":201}}]}`, wantErr: true},
		{name: "errors present and false", body: `{"took":1,"errors":false,"items":[{"create":{"status":201}}]}`, wantErr: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var cap capture
			srv := newServer(t, http.StatusOK, tc.body, &cap)
			defer srv.Close()

			o := New()
			if err := o.Open(context.Background(), sdk.Config{Settings: map[string]string{
				"endpoint": srv.URL, "index": "idx", "api_key": "theKey",
			}}); err != nil {
				t.Fatal(err)
			}
			defer o.Close(context.Background())

			err := o.Notify(context.Background(), sampleNotification())
			if tc.wantErr && err == nil {
				t.Fatal("a document that is not a _bulk response was read as a delivery")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("a valid _bulk success was refused: %v", err)
			}
		})
	}
}
