// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claudemanagedagents

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/sdk/model"
)

// maxWebhookBody bounds a received webhook body (a CMA webhook is a small {type,id}
// envelope; this protects memory against a hostile or runaway sender).
const maxWebhookBody = 64 << 10

// webhookReplayTTL bounds how long an event.id is remembered for replay de-dup. Anthropic
// retries deliver the SAME event.id; a duplicate is acknowledged (2xx) without re-emitting.
const webhookReplayTTL = 30 * time.Minute

// vault_credential.refresh_failed is the highest-signal vault event: an mcp_oauth credential
// can no longer be refreshed, so agents using it will lose access.
const evtRefreshFailed = "vault_credential.refresh_failed"

// webhookEnvelope is the CMA webhook delivery: a thin {type,id} envelope (the full object is
// fetched by id, never delivered, to avoid stale data on retries). Only structural fields
// are read — never a payload body.
type webhookEnvelope struct {
	Type      string `json:"type"` // "event"
	ID        string `json:"id"`   // event_... (unique per event, stable across retries)
	CreatedAt string `json:"created_at"`
	Data      struct {
		Type        string `json:"type"` // e.g. session.status_idled, vault_credential.refresh_failed
		ID          string `json:"id"`   // the resource id (sesn_/vlt_/vcrd_/...)
		WorkspaceID string `json:"workspace_id"`
	} `json:"data"`
}

// webhookReceiver is the inbound CMA webhook handler. It is FAIL-CLOSED: an unsigned,
// wrongly-signed, stale or malformed delivery is rejected and produces NO observation; a
// replayed delivery (same event.id) is acknowledged without re-emitting. Build it with
// newWebhookReceiver; it implements http.Handler and is safe for concurrent use.
//
// enrich, when non-nil, turns a thin verified envelope into the full governance
// observations by GETting the resource back (the documented thin-webhook pattern: the
// delivery carries only {type,id}; the object is fetched by id so a retry never acts on
// stale data). It runs AFTER verification, so only authenticated, fresh, first-seen
// events trigger an upstream read.
type webhookReceiver struct {
	key     []byte
	maxSkew time.Duration
	now     func() time.Time
	emit    func(context.Context, model.Observation) error
	enrich  func(context.Context, webhookEnvelope, time.Time) []model.Observation

	mu   sync.Mutex
	seen map[string]time.Time // event.id -> retain-until
}

// newWebhookReceiver builds a receiver. A receiver with no derivable signing key is never
// constructed (an unverifiable receiver is a security hole). emit delivers each mapped
// observation; enrich (nil = none) expands a verified envelope via the GET-back; clock is
// injectable for deterministic freshness/replay tests (nil = wall).
func newWebhookReceiver(secret string, maxSkew time.Duration, clock func() time.Time, emit func(context.Context, model.Observation) error, enrich func(context.Context, webhookEnvelope, time.Time) []model.Observation) (*webhookReceiver, error) {
	key := deriveWebhookKey(secret)
	if len(key) == 0 {
		return nil, fmt.Errorf("claude-managed-agents: webhook receiver requires a whsec_ signing secret (an unverifiable receiver must not be mounted)")
	}
	if maxSkew <= 0 {
		maxSkew = defaultWebhookMaxSkew
	}
	if clock == nil {
		clock = func() time.Time { return time.Now().UTC() }
	}
	return &webhookReceiver{
		key:     key,
		maxSkew: maxSkew,
		now:     clock,
		emit:    emit,
		enrich:  enrich,
		seen:    map[string]time.Time{},
	}, nil
}

// ServeHTTP verifies a webhook delivery and, on success, emits the mapped observations.
// Every verification failure is a fail-closed 4xx and nothing is emitted.
func (r *webhookReceiver) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	// The signature is over the RAW body bytes, so read them verbatim before any decode.
	body, err := io.ReadAll(io.LimitReader(req.Body, maxWebhookBody))
	if err != nil {
		http.Error(w, "cannot read body", http.StatusBadRequest)
		return
	}
	if !verifyWebhookSignature(r.key, body, req.Header.Get(webhookSigHeader)) {
		http.Error(w, "invalid signature", http.StatusUnauthorized) // fail closed
		return
	}
	var ev webhookEnvelope
	// Deny-closed: a conformant delivery carries an event id, a created_at and a data.type.
	// A signed-but-malformed delivery missing any of them is REJECTED rather than allowed to
	// silently skip the freshness/replay defenses below (every accepted delivery is dated +
	// de-dupable, not merely authenticated).
	if err := json.Unmarshal(body, &ev); err != nil || ev.Data.Type == "" || ev.ID == "" || ev.CreatedAt == "" {
		http.Error(w, "unrecognized webhook", http.StatusBadRequest)
		return
	}
	// Freshness: reject a delivery whose envelope created_at is unparseable, older than the
	// skew window (the documented unwrap() rejects payloads >5 min old), or implausibly in
	// the future. The check is unconditional — a missing/zero timestamp already failed above.
	created := parseTime(ev.CreatedAt)
	if created.IsZero() {
		http.Error(w, "bad timestamp", http.StatusBadRequest)
		return
	}
	now := r.now()
	if now.Sub(created) > r.maxSkew || created.Sub(now) > r.maxSkew {
		http.Error(w, "stale webhook", http.StatusUnauthorized)
		return
	}
	// Replay de-dup: a retry delivers the same event.id; acknowledge it (2xx) but do not
	// re-emit. The id is guaranteed non-empty (rejected above), so dedup is unconditional
	// — an id-less delivery can never bypass it. The id is REMEMBERED only after every
	// observation emitted successfully (fix: recording it before emission meant a
	// sink failure answered 503, but the retry found the id "seen" and was acked without
	// re-emitting — a silently lost observation). Two concurrent same-id deliveries may
	// therefore both emit; the bus contract is at-least-once and consumers de-dup on the
	// natural key, so a rare double-emit is honest where a lost fact is not.
	at := r.now().UTC()
	if r.alreadySeen(ev.ID) {
		w.WriteHeader(http.StatusOK)
		return
	}
	obs := mapWebhookEvent(ev, at)
	if r.enrich != nil {
		obs = append(obs, r.enrich(req.Context(), ev, at)...)
	}
	for _, o := range obs {
		if err := r.emit(req.Context(), o); err != nil {
			http.Error(w, "downstream unavailable", http.StatusServiceUnavailable)
			return
		}
	}
	r.remember(ev.ID)
	w.WriteHeader(http.StatusOK)
}

// alreadySeen reports whether the event id was delivered within the retention window.
// It evicts lazily so the map stays bounded.
func (r *webhookReceiver) alreadySeen(id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	for k, until := range r.seen {
		if now.After(until) {
			delete(r.seen, k)
		}
	}
	until, ok := r.seen[id]
	return ok && !now.After(until)
}

// remember records a fully-delivered event id for the replay window.
func (r *webhookReceiver) remember(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seen[id] = r.now().Add(webhookReplayTTL)
}

// mapWebhookEvent maps a verified webhook envelope to observations (structural only, never a
// payload body). A vault_credential.refresh_failed is the high-signal governance event (an
// mcp_oauth credential can no longer refresh → agents lose access); other vault.* events are
// governance Info; session.* events are forensic Info session-lifecycle facts. An
// unrecognized family is dropped.
func mapWebhookEvent(ev webhookEnvelope, at time.Time) []model.Observation {
	dataType := ev.Data.Type
	resID := redact.Clean(ev.Data.ID)
	switch {
	case dataType == evtRefreshFailed:
		return []model.Observation{model.FindingReport{
			Kind:        findingGovernance,
			Severity:    model.SeverityMedium,
			SubjectKind: kindVaultCred,
			SubjectRef:  labelRef(resID, "credential"),
			Title:       "CMA vault credential cannot refresh (mcp_oauth refresh_failed)",
			DetailHash:  redact.Hash("webhook " + dataType + " credential=" + resID + " ws=" + ev.Data.WorkspaceID + "; the refresh token is invalid or the OAuth server failed irrecoverably — agents using this credential lose access (CMA webhooks)"),
			OWASPASI:    []string{asiIdentityAbuse},
			OccurredAt:  at,
		}}
	case strings.HasPrefix(dataType, "vault"):
		subj := kindVault
		if strings.HasPrefix(dataType, "vault_credential") {
			subj = kindVaultCred
		}
		return []model.Observation{model.FindingReport{
			Kind:        findingGovernance,
			Severity:    model.SeverityInfo,
			SubjectKind: subj,
			SubjectRef:  labelRef(resID, "vault"),
			Title:       "CMA vault webhook: " + dataType,
			DetailHash:  redact.Hash("webhook " + dataType + " id=" + resID + " ws=" + ev.Data.WorkspaceID),
			OccurredAt:  at,
		}}
	case strings.HasPrefix(dataType, "session"):
		return []model.Observation{model.FindingReport{
			Kind:        findingForensic,
			Severity:    model.SeverityInfo,
			SubjectKind: kindManagedAgent,
			SubjectRef:  labelRef(resID, "session"),
			Title:       "CMA managed-agent session webhook: " + dataType,
			DetailHash:  redact.Hash("webhook " + dataType + " session=" + resID + " ws=" + ev.Data.WorkspaceID),
			OccurredAt:  at,
		}}
	default:
		return nil
	}
}
