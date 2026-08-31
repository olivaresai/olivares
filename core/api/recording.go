// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"context"
	"crypto/sha256"
	"errors"
	"hash"
	"io"
	"net/http"
	"net/url"
	"sort"

	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

// SessionRecorder is the privileged-session recording seam (SEC-G5). When configured (Options.Recorder), every module route flows through
// it: Gate runs BEFORE the handler and decides whether this call belongs to a
// recorded privileged surface — deny-closed, so a recorded surface is never
// reachable while its evidence cannot be written — and Record appends the
// completed action's frame AFTER the handler ran. The engine defines only the
// seam; the recorder implementation lives in modules/recording and is injected
// by the composition root (core never imports modules).
//
// The seam deliberately carries no request body and no query VALUES: the
// recording layer is minimal-data by construction (docs/SECURITY-HARDENING.md) — the wrapper
// hands over route shape, identifiers and a one-way body digest, nothing a
// secret could ride in.
type SessionRecorder interface {
	// Gate classifies the call before the handler runs. A decision with
	// Record=false means the surface is not recorded and the request proceeds
	// untouched. An error DENIES the request: ErrRecordingConsentRequired maps to
	// 403 recording_consent_required (the operator has not acknowledged the
	// recording notice); any other error maps to 503 recording_unavailable
	// (deny-closed: no evidence, no privileged action).
	Gate(ctx context.Context, call RecordedCall) (RecordingDecision, error)
	// Record appends the completed call's frame to EXACTLY the session Gate
	// reserved on (dec — never re-resolved, so a session sealed mid-request can
	// only yield a loud gap, never a frame mis-bound into another session's
	// chain). It runs after the response was written, so an error can no longer
	// deny the action — the recorder is responsible for making the gap loud and
	// evident (the wrapper logs it; the session's reserved/written counters keep
	// it permanently visible).
	Record(ctx context.Context, call RecordedCall, dec RecordingDecision, res RecordedResult) error
}

// RecordedCall describes one module-route action on a (potentially) privileged
// surface. Params carry the chi URL parameters (identifiers by convention); the
// recorder redacts and bounds them before persisting. QueryKeys carry query
// parameter NAMES only — never values.
type RecordedCall struct {
	// Namespace is the module API namespace the route belongs to.
	Namespace string
	// Method is the HTTP method.
	Method string
	// Pattern is the chi route pattern (e.g. "/breakglass/{id}/review").
	Pattern string
	// Permission is the permission the route required.
	Permission auth.Permission
	// Principal is the authenticated, authorized caller.
	Principal auth.Principal
	// Tenant is the single resolved tenant of the request.
	Tenant model.TenantID
	// Params are the resolved chi URL parameters (name → value).
	Params map[string]string
	// QueryKeys are the query parameter names, sorted (values never captured).
	QueryKeys []string
}

// RecordingDecision is Gate's verdict for one call.
type RecordingDecision struct {
	// Record is true when the call must be captured.
	Record bool
	// Session is the open recording session's id when Record is true.
	Session model.ID
}

// RecordedResult is the completed call's outcome.
type RecordedResult struct {
	// Status is the HTTP status the handler wrote (200 if none).
	Status int
	// BodySHA256 is the SHA-256 of the request body bytes the handler consumed,
	// nil when the request carried no body. A digest, never content.
	BodySHA256 []byte
	// BodyBytes is the number of request-body bytes consumed.
	BodyBytes int64
	// DurationMS is the handler's wall-clock duration in milliseconds.
	DurationMS int64
}

// ErrRecordingConsentRequired is returned by a SessionRecorder's Gate when the
// calling operator has not yet acknowledged the recording notice for their
// session. It maps to 403 recording_consent_required; the console intercepts
// the code and shows the consent dialog.
var ErrRecordingConsentRequired = errors.New("recording consent required")

// ErrRecordingSessionPrecondition means the exact session reserved by Gate can
// no longer participate in an atomic governed action: it is absent, sealed,
// belongs to another credential, or is already bound. Callers map it to 412 and
// roll back the governed state; storage/audit failures remain distinct errors.
var ErrRecordingSessionPrecondition = errors.New("recording session precondition failed")

// errRecordingUnavailable is what every non-consent Gate failure maps to:
// deny-closed with a distinct code, so an operator can tell a recording outage
// from a generic authz denial. The cause is logged server-side, never leaked.
var errRecordingUnavailable = errors.New("session recording unavailable; privileged surfaces are deny-closed without recording")

// recordedCall builds the seam's call description from a matched module route.
func recordedCall(r *http.Request, ns, method, pattern string, perm auth.Permission, p auth.Principal, tenant model.TenantID) RecordedCall {
	call := RecordedCall{
		Namespace: ns, Method: method, Pattern: pattern,
		Permission: perm, Principal: p, Tenant: tenant,
	}
	if rctx := chi.RouteContext(r.Context()); rctx != nil {
		for i := 0; i < len(rctx.URLParams.Keys); i++ {
			k := rctx.URLParams.Keys[i]
			if k == "" || k == "*" {
				continue // chi's wildcard artifact, not a route parameter
			}
			v := rctx.URLParams.Values[i]
			// chi keeps the matched segment ESCAPED; unescape so the recorder's
			// redactor sees the real value (an %40 must not smuggle an email past it).
			if u, err := url.PathUnescape(v); err == nil {
				v = u
			}
			if call.Params == nil {
				call.Params = make(map[string]string)
			}
			call.Params[k] = v
		}
	}
	if q := r.URL.Query(); len(q) > 0 {
		call.QueryKeys = make([]string, 0, len(q))
		for k := range q {
			call.QueryKeys = append(call.QueryKeys, k)
		}
		sort.Strings(call.QueryKeys)
	}
	return call
}

// bodyHasher tees the request body through SHA-256 so the frame can bind the
// EXACT bytes the handler consumed without ever buffering or storing them.
type bodyHasher struct {
	rc io.ReadCloser
	h  hash.Hash
	n  int64
}

func newBodyHasher(rc io.ReadCloser) *bodyHasher {
	return &bodyHasher{rc: rc, h: sha256.New()}
}

func (b *bodyHasher) Read(p []byte) (int, error) {
	n, err := b.rc.Read(p)
	if n > 0 {
		b.h.Write(p[:n])
		b.n += int64(n)
	}
	return n, err
}

func (b *bodyHasher) Close() error { return b.rc.Close() }

// sum returns the digest of the consumed bytes, or nil when none were read.
func (b *bodyHasher) sum() []byte {
	if b.n == 0 {
		return nil
	}
	return b.h.Sum(nil)
}
