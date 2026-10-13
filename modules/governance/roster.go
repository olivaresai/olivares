// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance

import (
	"context"
	"errors"
	"strings"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// errNoData is returned when a seam method is used before the engine (or a test)
// wired the data handle via UseData.
var errNoData = errors.New("governance: module has no data handle; call UseData first")

// RosterReport summarizes one reconciliation pass.
type RosterReport struct {
	Source              string `json:"source,omitempty"`
	Sources             int    `json:"sources"`
	ProvidersConfigured int    `json:"providers_configured"`
	Identities          int    `json:"identities"`
	Collections         int    `json:"collections"`
	Memberships         int    `json:"memberships"`
	// ProvidersFailed names the identity sources whose Snapshot did not answer this
	// call. It exists so that ONE failing source stops taking the whole tenant down
	// with it — see handleRosterSync — and so that surviving is not the same as
	// succeeding silently: a caller reading only `sources` would see a smaller number
	// and no reason for it.
	ProvidersFailed []RosterProviderFailure `json:"providers_failed,omitempty"`
	Note            string                  `json:"note,omitempty"`
}

// RosterProviderFailure identifies one identity source that could not be snapshotted.
//
// Provider is the connector's Go type (e.g. "*conjur.Connector"), not a configured
// name: GraphProvider carries no name, and adding one to a public interface for a
// diagnostic would make every implementer pay for it. The type is enough to tell an
// operator WHICH connector to look at, which is the question this field answers.
//
// Reason is deliberately FIXED and carries no error text. Before this change the route
// answered a generic "identity source snapshot failed" and put the real error in the
// debug log, and that disclosure posture is not what is being changed here: a directory
// error can carry a host, a DN or a token fragment. This change is about AVAILABILITY —
// one dead source no longer kills the others — and it would be a poor trade to buy that
// by leaking what the old 502 was careful not to say. The detail stays in the log.
type RosterProviderFailure struct {
	Provider string `json:"provider"`
	Reason   string `json:"reason"`
}

// identityByExternalID resolves a core Identity by external_id with the bridge's
// own ambiguity tolerance: it returns the first match (Limit 2 distinguishes 0/1
// from 2+), never silently picking one of several for ATTRIBUTION — but for an
// idempotent UPSERT, enriching the first match is correct. Under the single-writer
// SQLite deployment (MaxOpenConns(1)) there is no concurrent second creator, so no
// duplicate arises; there is no DB-level unique index on identity.external_id
// (single-writer model, see access-map/bridge.go), which is why this and the
// bridge must both key on external_id alone to converge.
func identityByExternalID(ctx context.Context, sc store.Scope, ref string) (model.Identity, bool, error) {
	list, _, err := sc.Identities().List(ctx, model.Query{Filters: []model.Filter{eq("external_id", ref)}, Limit: 2})
	if err != nil {
		return model.Identity{}, false, err
	}
	if len(list) == 0 {
		return model.Identity{}, false, nil
	}
	return list[0], true, nil
}

// containsInlineCredential rejects the obvious ways a credential could be smuggled
// into a value field (basic-auth userinfo in a URL, or a secret-like assignment).
// It is a guardrail, not a scanner — the real guarantee is the allow-list on
// identity metadata and the typed specs elsewhere. It never stores the matched value.
func containsInlineCredential(s string) bool {
	if s == "" {
		return false
	}
	low := strings.ToLower(s)
	if i := strings.Index(low, "://"); i >= 0 {
		rest := low[i+3:]
		if at := strings.IndexByte(rest, '@'); at >= 0 && strings.IndexByte(rest[:at], ':') >= 0 {
			return true
		}
	}
	for _, kw := range []string{"token=", "secret=", "password=", "passwd=", "api_key=", "apikey=", "access_key=", "client_secret="} {
		if strings.Contains(low, kw) {
			return true
		}
	}
	return false
}
