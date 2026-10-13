// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claudewif

import (
	"strings"
	"time"
)

// tokenPrefixOAT is the documented prefix of a minted access token.
const tokenPrefixOAT = "sk-ant-oat"

// ExchangeParams identifies the federation rule and target for one exchange. The
// rule (fdrl_), service account (svac_) and organization (UUID) are required; the
// workspace (wrkspc_ or "default") is required only when the rule spans more than
// one workspace, otherwise the server selects the rule's sole workspace.
type ExchangeParams struct {
	FederationRuleID string
	OrganizationID   string
	ServiceAccountID string
	WorkspaceID      string
}

// MintedToken is the short-lived credential the exchange returns. AccessToken is a
// SECRET (sk-ant-oat…) and MUST NOT be logged or persisted — the helper never does,
// and the host must treat it likewise, using it transiently and re-running the
// exchange before expiry (there is no refresh token for the WIF path). Use Audit()
// for anything written to the ledger or a log.
type MintedToken struct {
	AccessToken string // SECRET — never log or persist
	TokenType   string // "Bearer"
	Scope       string
	ExpiresIn   int // seconds
	ObtainedAt  time.Time

	rule, org, serviceAccount, workspace string // exchange provenance (non-secret)
}

// ExpiresAt is the absolute expiry derived from the obtained time and lifetime.
func (t MintedToken) ExpiresAt() time.Time {
	return t.ObtainedAt.Add(time.Duration(t.ExpiresIn) * time.Second)
}

// ExchangeAudit is the non-secret record of an exchange, suitable for the ledger
// (docs/SECURITY-HARDENING.md). It deliberately carries NO token.
type ExchangeAudit struct {
	FederationRuleID string
	OrganizationID   string
	ServiceAccountID string
	WorkspaceID      string
	Scope            string
	TokenType        string
	ExpiresAt        time.Time
}

// Audit returns the non-secret audit record the host writes to the ledger.
func (t MintedToken) Audit() ExchangeAudit {
	return ExchangeAudit{
		FederationRuleID: t.rule, OrganizationID: t.org, ServiceAccountID: t.serviceAccount,
		WorkspaceID: t.workspace, Scope: t.Scope, TokenType: t.TokenType, ExpiresAt: t.ExpiresAt(),
	}
}

// hasOATPrefix reports whether tok carries the documented minted-token prefix; used
// by callers/tests to sanity-check a minted token without logging it.
func hasOATPrefix(tok string) bool { return strings.HasPrefix(tok, tokenPrefixOAT) }
