// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package identity is shared, connector-internal logic for the data-store
// connectors (pg-audit, mysql-audit, s3-cloudtrail). It captures the raw
// identity a store's native audit attributes an access to, and decides the
// attribution confidence for the shared-service-account problem (ARCHITECTURE.md,
// docs/contracts).
//
// A store audit attributes an access to a non-human identity — a database role,
// a MySQL account, an IAM principal — never to a resolved agent. Resolving an
// identity to an agent is module VI's job; the connector only emits the
// raw identity and, when the credential is shared/pooled, marks the attribution
// as approximate. It never invents an attribution the audit does not give
// (docs/SECURITY-HARDENING.md).
package identity

import (
	"strings"

	"github.com/olivaresai/olivares/sdk/model"
)

// OriginKind is the EdgeObservation.OriginKind every data-store connector emits.
// It is always "identity" (not "agent"): the audit identifies a credential/role,
// and the identity↔agent bridge is module VI's responsibility (docs/contracts).
const OriginKind = "identity"

// SharedSet is a case-insensitive set of identity references the operator has
// declared to be shared service accounts, connection-pool logins or shared IAM
// roles. An access attributed to a member has ambiguous agent attribution, so
// the connector emits it with approximate confidence (the raw identity is still
// emitted; only the trust drops).
type SharedSet struct {
	m map[string]struct{}
}

// ParseSharedAccounts builds a SharedSet from a comma-separated config value
// (the `shared_accounts` setting). Entries are trimmed and lower-cased; empty
// entries are ignored. A nil/empty value yields an empty set (every identity is
// then treated as attributed unless a connector decides otherwise).
func ParseSharedAccounts(csv string) SharedSet {
	return NewSharedSet(strings.Split(csv, ","))
}

// NewSharedSet builds a SharedSet from already-split references. Entries are
// trimmed and lower-cased; blank entries are ignored.
func NewSharedSet(refs []string) SharedSet {
	m := make(map[string]struct{}, len(refs))
	for _, r := range refs {
		r = strings.ToLower(strings.TrimSpace(r))
		if r == "" {
			continue
		}
		m[r] = struct{}{}
	}
	return SharedSet{m: m}
}

// Has reports whether ref is a declared shared account (case-insensitive,
// whitespace-insensitive). A blank ref is never a member.
func (s SharedSet) Has(ref string) bool {
	ref = strings.ToLower(strings.TrimSpace(ref))
	if ref == "" {
		return false
	}
	_, ok := s.m[ref]
	return ok
}

// Len reports the number of declared shared accounts.
func (s SharedSet) Len() int { return len(s.m) }

// ConfidenceFor returns the attribution confidence for an access the connector
// would attribute to any of refs. It is ConfidenceApproximate (ambiguous
// attribution) if ANY ref is a declared shared account, and ConfidenceAttributed
// otherwise. A connector passes the identity reference(s) that matter for the
// access — e.g. a role and an application_name (pg), a user and a user@host
// (mysql), or an assumed-role ARN (s3); a single match collapses confidence.
//
// Callers that already know the attribution is ambiguous for a reason the set
// cannot express (e.g. an s3 AWSService principal) should use
// model.ConfidenceApproximate directly rather than this helper.
func (s SharedSet) ConfidenceFor(refs ...string) model.Confidence {
	for _, r := range refs {
		if s.Has(r) {
			return model.ConfidenceApproximate
		}
	}
	return model.ConfidenceAttributed
}
