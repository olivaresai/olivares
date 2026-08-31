// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claudewif

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/identitysource"
	"github.com/olivaresai/olivares/connectors/modelprovider"
)

// NHI lifecycle actuation for Anthropic API keys, verified against the
// Admin API reference (platform.claude.com/docs/en/api/admin-api, 2026-06-10).
// The ONLY write the Admin API exposes for api keys is the update endpoint —
// POST /v1/organizations/api_keys/{api_key_id} with optional "name" and
// "status" ("active"|"inactive"|"archived") body fields, returning the APIKey
// object. There is NO create, delete or rotate endpoint: new keys are
// Console-only ("The Admin API can only manage existing API keys"). By
// contrast, WIF service accounts (svac_), federation issuers (fdis_) and
// federation rules (fdrl_) DO have full Admin API endpoints
// (create/list/update/archive) — but only under an org:admin OAuth bearer
// token, which this sk-ant-admin actuator does not hold (Admin API keys are
// rejected on those endpoints). The Source now READS (lists) that live WIF
// config under a separate org:admin OAuth client for declared-vs-actual
// reconciliation (reconcile.go); governed WIF WRITE actuation (create/update/
// archive of issuers/rules/svacs) remains out of scope here. The actuator
// therefore declares exactly three capabilities (disable/restore/finalize on
// api_key NHIs) and honestly refuses rotate/retire — the rotation story for
// Anthropic is Workload Identity Federation (short-lived sk-ant-oat tokens via
// the Exchanger) or a Console re-issue, never a fabricated Admin API call.

// Anthropic API key status values the Admin API accepts on update. The
// response enum additionally includes the read-only "expired".
const (
	keyStatusActive   = "active"
	keyStatusInactive = "inactive"
	keyStatusArchived = "archived"
)

// maxActuationBody caps the update response body read (the Exchanger bound).
const maxActuationBody = 1 << 20

// maxActuationExcerpt caps the provider error excerpt surfaced in an error.
const maxActuationExcerpt = 256

// Actuator is the write-capable NHI lifecycle arm for Anthropic API keys. It
// is a SEPARATE opt-in type beside the read-only Source (the lifecycle.go
// design rule): the composition root builds it only when the operator supplies
// a write-capable admin credential, it is never part of the Gather plane, and
// the consumer (modules/governance) invokes it only behind a granted HITL
// approval. It holds no state and persists nothing; errors never carry the
// admin key.
type Actuator struct {
	baseURL  string
	adminKey string
	version  string
	doer     modelprovider.Doer
	now      func() time.Time
}

// Compile-time proof that Actuator satisfies the lifecycle contract.
var _ identitysource.LifecycleActuator = (*Actuator)(nil)

// NewActuator builds an Actuator against baseURL (empty => the Anthropic API)
// with the given Admin API key (sk-ant-admin…). A nil doer uses the default
// HTTP client. An empty adminKey leaves the actuator unconfigured: every
// actuation fails safe with an ops error before any HTTP.
func NewActuator(baseURL, adminKey string, doer modelprovider.Doer) *Actuator {
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	if doer == nil {
		doer = &http.Client{}
	}
	return &Actuator{
		baseURL:  strings.TrimRight(baseURL, "/"),
		adminKey: adminKey,
		version:  defaultAnthropicVersion,
		doer:     doer,
	}
}

// Capabilities declares, honestly, what the Admin API can really do to an api
// key. No OpRotate and no OpRetire: rotation of Anthropic static keys does not
// exist in the Admin API (key creation is Console-only).
func (a *Actuator) Capabilities() []identitysource.ActuatorCapability {
	return []identitysource.ActuatorCapability{
		{
			Op: identitysource.OpDisable, TargetKind: kindAPIKey,
			Detail: "sets the Anthropic API key status to inactive (reversible via the Admin API)",
		},
		{
			Op: identitysource.OpRestore, TargetKind: kindAPIKey,
			Detail: "sets the key status back to active",
		},
		{
			Op: identitysource.OpFinalize, TargetKind: kindAPIKey,
			Detail: "archives the Anthropic API key (status=archived); un-archiving via the API is not a documented capability",
		},
	}
}

// Disable reversibly disables the API key (status=inactive).
func (a *Actuator) Disable(ctx context.Context, req identitysource.ActuationRequest) (identitysource.ActuationReceipt, error) {
	return a.setStatus(ctx, identitysource.OpDisable, req, keyStatusInactive,
		" (reversible via the Admin API)")
}

// Restore reverses Disable (status=active).
func (a *Actuator) Restore(ctx context.Context, req identitysource.ActuationRequest) (identitysource.ActuationReceipt, error) {
	return a.setStatus(ctx, identitysource.OpRestore, req, keyStatusActive, "")
}

// Finalize archives the API key (status=archived) — the provider's definitive
// offboarding step; un-archiving via the API is not a documented capability.
func (a *Actuator) Finalize(ctx context.Context, req identitysource.ActuationRequest) (identitysource.ActuationReceipt, error) {
	return a.setStatus(ctx, identitysource.OpFinalize, req, keyStatusArchived,
		"; un-archiving via the API is not a documented capability")
}

// Rotate is honestly unsupported: the Admin API cannot mint Anthropic API keys
// (Console-only creation, no rotate endpoint). The rotation story for Anthropic
// is Workload Identity Federation — short-lived sk-ant-oat tokens minted by the
// Exchanger — or a Console re-issue.
func (a *Actuator) Rotate(context.Context, identitysource.ActuationRequest) (identitysource.RotatedCredential, error) {
	return identitysource.RotatedCredential{}, fmt.Errorf(
		"claude-wif: rotate: the Admin API cannot mint API keys (Console-only); rotate via Workload Identity Federation (short-lived sk-ant-oat tokens) or re-issue in the Console: %w",
		identitysource.ErrUnsupportedOperation)
}

// Retire is honestly unsupported: the Admin API exposes no delete endpoint for
// API keys; the closest definitive step is Finalize (archive).
func (a *Actuator) Retire(context.Context, identitysource.ActuationRequest) (identitysource.ActuationReceipt, error) {
	return identitysource.ActuationReceipt{}, fmt.Errorf(
		"claude-wif: retire: the Admin API cannot delete API keys; archive via finalize, or revoke in the Console: %w",
		identitysource.ErrUnsupportedOperation)
}

// apiKeyUpdate is the JSON body of the api key update endpoint. The API also
// accepts an optional "name"; the actuator only ever sets status.
type apiKeyUpdate struct {
	Status string `json:"status"`
}

// setStatus POSTs {"status": want} to /v1/organizations/api_keys/{ref},
// validates the returned object really carries the requested status, and
// returns the ledger-safe receipt (Detail = resulting status + caveat). It
// fails safe without HTTP when the actuator has no admin key, and its errors
// never carry the credential.
func (a *Actuator) setStatus(ctx context.Context, op identitysource.LifecycleOp, req identitysource.ActuationRequest, want, caveat string) (identitysource.ActuationReceipt, error) {
	if a.adminKey == "" {
		return identitysource.ActuationReceipt{}, fmt.Errorf(
			"claude-wif: %s: actuator not configured with an admin key (lifecycle actuation requires a write-capable sk-ant-admin… credential)", op)
	}
	if req.Kind != "" && req.Kind != kindAPIKey {
		return identitysource.ActuationReceipt{}, fmt.Errorf(
			"claude-wif: %s: identity kind %q is not actuable via the Admin API (only %q): %w",
			op, req.Kind, kindAPIKey, identitysource.ErrUnsupportedOperation)
	}
	if strings.TrimSpace(req.Ref) == "" {
		return identitysource.ActuationReceipt{}, fmt.Errorf("claude-wif: %s: empty identity ref", op)
	}

	body, err := json.Marshal(apiKeyUpdate{Status: want})
	if err != nil {
		return identitysource.ActuationReceipt{}, fmt.Errorf("claude-wif: %s: encode: %w", op, err)
	}
	u := a.baseURL + pathAPIKeys + "/" + url.PathEscape(req.Ref)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return identitysource.ActuationReceipt{}, fmt.Errorf("claude-wif: %s: build request: %w", op, err)
	}
	httpReq.Header.Set("content-type", "application/json")
	httpReq.Header.Set("accept", "application/json")
	// The Admin API auth headers, mirroring modelprovider's AuthAnthropicKey
	// scheme + the static anthropic-version header the Source sends.
	httpReq.Header.Set("x-api-key", a.adminKey)
	httpReq.Header.Set("anthropic-version", a.version)

	resp, err := a.doer.Do(httpReq)
	if err != nil {
		return identitysource.ActuationReceipt{}, fmt.Errorf("claude-wif: %s: post: %w", op, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxActuationBody))
	if err != nil {
		return identitysource.ActuationReceipt{}, fmt.Errorf("claude-wif: %s: read response: %w", op, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return identitysource.ActuationReceipt{}, actuationError(op, resp, raw)
	}

	var out apiKey
	if err := json.Unmarshal(raw, &out); err != nil {
		return identitysource.ActuationReceipt{}, fmt.Errorf("claude-wif: %s: decode response: %w", op, err)
	}
	if out.Status != want {
		return identitysource.ActuationReceipt{}, fmt.Errorf(
			"claude-wif: %s: provider returned key status %q, want %q — actuation not confirmed", op, out.Status, want)
	}

	return identitysource.ActuationReceipt{
		Op: op, Ref: req.Ref, Provider: identitysource.SourceAnthropic,
		Detail:     fmt.Sprintf("Anthropic API key status set to %q%s", out.Status, caveat),
		OccurredAt: a.clock().UTC(),
	}, nil
}

// actuationError builds a non-sensitive error from a failed update: the HTTP
// status, the Anthropic error type, a bounded message excerpt and the request
// id (header or body) for support correlation — never the admin key (the
// exchangeError pattern).
func actuationError(op identitysource.LifecycleOp, resp *http.Response, raw []byte) error {
	reqID := resp.Header.Get("request-id")
	var parsed struct {
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
		RequestID string `json:"request_id"`
	}
	_ = json.Unmarshal(raw, &parsed)
	if parsed.RequestID != "" {
		reqID = parsed.RequestID
	}
	code := parsed.Error.Type
	if code == "" {
		code = "unknown"
	}
	if reqID == "" {
		reqID = "none"
	}
	excerpt := strings.TrimSpace(parsed.Error.Message)
	if excerpt == "" {
		excerpt = strings.TrimSpace(string(raw))
	}
	if len(excerpt) > maxActuationExcerpt {
		excerpt = excerpt[:maxActuationExcerpt]
	}
	return fmt.Errorf("claude-wif: %s rejected: http %d, error %q (%s), request_id %s",
		op, resp.StatusCode, code, excerpt, reqID)
}

// clock returns the actuator's time source (injectable for tests).
func (a *Actuator) clock() time.Time {
	if a.now != nil {
		return a.now()
	}
	return time.Now()
}
