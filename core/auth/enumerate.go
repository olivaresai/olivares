// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth

import "context"

// reverse / enumeration queries as an AGGREGATION of real decisions.
//
// Cedar (and the scoped engine on top of it) answers exactly one question:
// "may THIS principal do THIS on THIS resource?" (Authorizer.Authorize). It has no
// native reverse query — "what can this principal access" / "who can access this
// resource" — the one Zanzibar capability it lacks, and the one access-reviews
// need. The decision (master-plan §6.1, NOT re-litigated here) is to build those
// reverse queries by ENUMERATING the candidate set and BATCH-AUTHORIZING it through
// the SAME Authorize the request path uses — never a parallel authorization algebra
// that could diverge from what is enforced.
//
// AuthorizeBatch is that aggregation primitive, and it is deliberately tiny: it
// loops Authorize. It holds NO policy logic of its own — no RBAC, no Cedar, no
// deny-overlay — so it is impossible for an enumeration to allow a pair the request
// path would deny (or vice versa). The cost of an honest reverse query is therefore
// entirely in (a) BOUNDING the candidate set and (b) the per-candidate Authorize;
// the caller owns the bounding (it sources candidates from the store), this owns the
// faithful evaluation. That split keeps core/auth free of any store dependency.

// AuthorizeBatch evaluates each Request through the live Authorizer and returns the
// decisions in the SAME order, so decisions[i] is exactly Authorize(ctx, reqs[i]).
// It is the honest core of the reverse queries and the AuthZEN batch endpoint:
// an aggregation of real decisions, never a reimplementation of the decision algebra
// (which lives only in Authorize). It is safe for concurrent use (Authorizer is).
//
// It is intentionally SEQUENTIAL. Each entity-level Authorize may resolve the
// scope tree from the store (one indexed read), so a fan-out of thousands of
// concurrent calls would amplify load on the single-writer store far more than it
// would shorten wall-clock — the Cedar evaluation itself is sub-millisecond. The
// real lever is the candidate count, which the caller bounds and pages; this stays
// simple and predictable. It honors context cancellation between requests so an
// over-large enumeration a caller forgot to bound can still be aborted, returning
// the decisions accumulated so far alongside the context error.
func (az *Authorizer) AuthorizeBatch(ctx context.Context, reqs []Request) ([]Decision, error) {
	out := make([]Decision, 0, len(reqs))
	for _, req := range reqs {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		out = append(out, az.Authorize(ctx, req))
	}
	return out, nil
}
