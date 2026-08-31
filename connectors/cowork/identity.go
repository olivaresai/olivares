// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package cowork

import (
	"sync"
	"time"

	"github.com/olivaresai/olivares/sdk/model"
)

// Identity resource kinds. Cowork tags every OTEL record with the organization and
// the account that owns the session, in the same id namespace as the Anthropic
// admin/Compliance APIs. The connector links the session to those identities as
// topology edges (ModeUnknown — an identity link is not an R/RW access), so the
// inventory/access-map/governance modules attribute Cowork activity per-org/
// per-account instead of seeing a bare session id — AND so the account entity
// becomes the JOIN POINT for the OTEL↔Compliance correlation (see AccountRef).
const (
	resIdentityOrg     = "identity.org"
	resIdentityAccount = "identity.account"
)

// coworkIdentity is the session-scoped identity a Cowork OTEL record tags: the org
// and the shared account identifier. user.email is deliberately not part of it
// (PII the minimal-data posture never carries; docs/SECURITY-HARDENING.md).
type coworkIdentity struct {
	sessionID   string
	orgID       string
	accountUUID string // user.account_uuid (a UUID)
	accountID   string // user.account_id (Anthropic user_01… tag) — the cross-API correlation key
}

// AccountRef resolves the canonical account correlation reference shared across
// Anthropic's surfaces. It is the JOIN KEY for the OTEL↔Compliance correlation
// (A1): Cowork OTEL exposes the account as user.account_id /
// user.account_uuid; the Compliance API Activity Feed exposes the same account as
// actor.user_id; the Enterprise Analytics API as user.id. All three live in the
// Anthropic account/user id namespace, so a consumer that materializes the account
// entity from any of them lands on the SAME entity. The connector prefers
// user.account_id (the stable account tag, the form the Compliance feed carries),
// falling back to the account UUID, then the user id — never fabricating one. It is
// EXPORTED so the Compliance-side correlation bridge derives the identical ref from
// a Compliance actor, guaranteeing the two sources join. Returns "" when none is
// present (no account to correlate).
func AccountRef(accountID, accountUUID, userID string) string {
	return firstNonEmpty(accountID, firstNonEmpty(accountUUID, userID))
}

// identityEdges builds the session→identity topology edges for an event's identity:
// one session→identity.account edge keyed on the shared account ref (the
// correlation join point) and one session→identity.org edge. The mode is Unknown
// (a link, not an access) and the confidence is Attributed (the origin is a
// concrete session). Returns nil when there is no session.
func identityEdges(id coworkIdentity, at time.Time) []model.EdgeObservation {
	if id.sessionID == "" {
		return nil
	}
	var out []model.EdgeObservation
	add := func(kind, ref string) {
		if ref == "" {
			return
		}
		out = append(out, model.EdgeObservation{
			OriginKind:   originSession,
			OriginRef:    id.sessionID,
			ResourceKind: kind,
			ResourceRef:  ref,
			Mode:         model.ModeUnknown,
			Source:       model.SignalOTEL,
			Confidence:   model.ConfidenceAttributed,
			ObservedAt:   at,
		})
	}
	add(resIdentityAccount, AccountRef(id.accountID, id.accountUUID, ""))
	add(resIdentityOrg, id.orgID)
	return out
}

// identitySeen tracks which sessions have already had their identity edges emitted
// so the connector links a session to its org/account ONCE rather than on every
// event (the link is stable for a session's lifetime; the engine merges any
// cross-restart re-emission by natural key). It is safe for concurrent use.
type identitySeen struct {
	mu   sync.Mutex
	seen map[string]struct{}
}

func newIdentitySeen() *identitySeen { return &identitySeen{seen: map[string]struct{}{}} }

// first reports whether this is the first time the session is seen, recording it.
// An empty session id is never "first" (nothing to attribute).
func (s *identitySeen) first(sessionID string) bool {
	if sessionID == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.seen[sessionID]; ok {
		return false
	}
	s.seen[sessionID] = struct{}{}
	return true
}
