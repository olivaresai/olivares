// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package accessmap

import (
	"context"
	"errors"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// Audit actions for the privileged graph reads (docs/SECURITY-HARDENING.md).
const (
	actionGraphRead = "access_map.graph.read"
	actionDriftRead = "access_map.drift.read"
)

// Graph returns the access edges matching q (filter by origin, resource, mode,
// confidence, signal_source). It is a PRIVILEGED, self-audited read: viewing the
// R/RW map is recon-relevant (docs/SECURITY-HARDENING.md), so the self-audit event seals in the
// same transaction before any edge is returned (docs/SECURITY-HARDENING.md/§5).
func (m *Module) Graph(ctx context.Context, tenant model.TenantID, actor, actorKind string, q model.Query) ([]model.AccessEdge, error) {
	if m.data == nil {
		return nil, errNoData
	}
	var edges []model.AccessEdge
	err := m.data.Mutate(ctx, tenant, func(sc store.Scope) error {
		if _, err := sc.Audit().Append(ctx, model.AuditDraft{Actor: actor, ActorKind: actorKind, Action: actionGraphRead}); err != nil {
			return err
		}
		var e error
		edges, _, e = sc.AccessEdges().List(ctx, q)
		return e
	})
	return edges, err
}

// AuditedNeighbors performs a privileged, self-audited read of the edges touching
// one node in a direction (docs/SECURITY-HARDENING.md, §4, §5). The self-audit append and the
// read share one Mutate transaction, so a read whose audit cannot be sealed
// commits nothing and yields nothing.
func (m *Module) AuditedNeighbors(ctx context.Context, tenant model.TenantID, actor, actorKind string, node model.NodeRef, dir model.Direction) ([]model.AccessEdge, error) {
	if m.data == nil {
		return nil, errNoData
	}
	var edges []model.AccessEdge
	err := m.data.Mutate(ctx, tenant, func(sc store.Scope) error {
		if _, err := sc.Audit().Append(ctx, model.AuditDraft{
			Actor: actor, ActorKind: actorKind, Action: actionGraphRead,
			TargetKind: model.Kind(node.Kind), TargetID: node.ID,
			Meta: map[string]any{"direction": string(dir)},
		}); err != nil {
			return err
		}
		var e error
		edges, e = sc.AccessEdges().Neighbors(ctx, node, dir)
		return e
	})
	return edges, err
}

// PrivilegeDiff is the permitted-vs-observed result for a tenant (ARCHITECTURE.md —
// the killer feature): the least-privilege drift split into the kinds that
// matter operationally.
type PrivilegeDiff struct {
	// UnusedGrants are permitted accesses never observed (over-provisioning),
	// on resource kinds an observed-side collector covers — so "never observed"
	// is real evidence.
	UnusedGrants []model.PrivilegeDrift
	// UnexpectedAccesses are observed accesses that are NOT permitted — the
	// security-relevant half, surfaced as the headline finding (ARCHITECTURE.md).
	UnexpectedAccesses []model.PrivilegeDrift
	// InventoryGrants are permitted accesses on resource kinds with NO
	// observed-side source in the product (permittedInventoryKinds): there,
	// "never observed" is the expected steady state, not evidence of
	// over-provisioning, so they are kept out of the headline UnusedGrants.
	// Additive — a caller that ignores the field simply sees a cleaner diff.
	InventoryGrants []model.PrivilegeDrift
	// Truncated reports that drainDrift hit its page bound and the diff was
	// reconciled over a PARTIAL window: rows past the bound are invisible, and a
	// reconciling pair straddling it yields false drift. Never silent (docs/SECURITY-HARDENING.md
	// §6): a consumer must label a truncated diff as partial, not authoritative.
	Truncated bool
}

// Diff computes the permitted-vs-observed least-privilege drift for the tenant
// and splits it into unused grants (headline + inventory) and unexpected
// accesses. It is a PRIVILEGED, self-audited read. Filters narrow the underlying
// edges (e.g. exclude approximate-confidence edges, or focus one signal source);
// the drift window itself is fully drained (drainDrift), so a flood of
// permitted-only edges cannot hide real violations behind the store's row cap.
func (m *Module) Diff(ctx context.Context, tenant model.TenantID, actor, actorKind string, q model.Query) (PrivilegeDiff, error) {
	if m.data == nil {
		return PrivilegeDiff{}, errNoData
	}
	var out PrivilegeDiff
	err := m.data.Mutate(ctx, tenant, func(sc store.Scope) error {
		if _, err := sc.Audit().Append(ctx, model.AuditDraft{Actor: actor, ActorKind: actorKind, Action: actionDriftRead}); err != nil {
			return err
		}
		drifts, truncated, err := drainDrift(ctx, sc, q)
		if err != nil {
			return err
		}
		out, err = reconcileDrift(ctx, sc, drifts)
		out.Truncated = truncated
		return err
	})
	return out, err
}

// ReconciledDrift computes the cross-origin-reconciled privilege diff over an
// EXISTING tenant scope, without the audit append and Mutate that Module.Diff wraps
// it in. It is the read-only seam a sibling AGPL module calls when it already holds a
// self-audited Scope and must consume the SAME reconciled drift the headline UI shows
// — never the raw store Drift, which double-counts cross-origin access (C2 /
// an agent's observed access against the identity it assumes looks like both
// a false unexpected access and a false unused grant). Module III owns the
// reconciliation logic (decision A: single writer/owner of the diff); callers
// consume this seam, they never reimplement reconcileDrift. The caller is responsible
// for its own authorization and audit (e.g. compliance's gatherEvidence runs inside
// its own audited transaction and must not double-audit here).
func ReconciledDrift(ctx context.Context, sc store.Scope, q model.Query) (PrivilegeDiff, error) {
	drifts, truncated, err := drainDrift(ctx, sc, q)
	if err != nil {
		return PrivilegeDiff{}, err
	}
	out, err := reconcileDrift(ctx, sc, drifts)
	out.Truncated = truncated
	return out, err
}

// driftPageCap mirrors the store's hard per-query row cap (sqlstore maxLimit):
// AccessEdges().Drift returns at most this many rows, ordered by id ASC, with
// no continuation token — the store's own Drift doc defers full pagination of
// the drift set to this module.
const driftPageCap = 1000

// maxDriftPages hard-bounds drainDrift at 50 pages (50k drift rows) as a memory
// guard. An estate beyond the bound reconciles a truncated — but 50× larger —
// window; what was drained is returned WITH the truncation reported (never an
// error, never silent: PrivilegeDiff.Truncated tells the consumer the diff is
// partial).
const maxDriftPages = 50

// drainDrift pages the store's capped Drift window with id-keyset re-queries
// until the whole drift set is in hand. Without it a flood of permitted-only
// grant edges (the identity-source inventories) would permanently occupy
// the first 1000 ids and HIDE real violations sorted after them. The caller's
// filters are preserved on every page; the caller's Limit is NOT (a
// reconciliation over a partial window fabricates drift — both halves of a
// reconciling pair must be visible). truncated reports that the page bound was
// hit with rows possibly remaining.
func drainDrift(ctx context.Context, sc store.Scope, q model.Query) (drifts []model.PrivilegeDrift, truncated bool, err error) {
	page := q
	page.Limit = driftPageCap
	var out []model.PrivilegeDrift
	for i := 0; i < maxDriftPages; i++ {
		batch, err := sc.AccessEdges().Drift(ctx, page)
		if err != nil {
			return nil, false, err
		}
		out = append(out, batch...)
		if len(batch) < driftPageCap {
			return out, false, nil
		}
		// Keyset continuation: Drift orders by id ASC, so re-query strictly past
		// the last row seen. The filter slice is rebuilt from the caller's each
		// page — never appended in place (the caller's slice is not ours to grow).
		lastID := batch[len(batch)-1].Edge.ID.String()
		page.Filters = append(append([]model.Filter{}, q.Filters...),
			model.Filter{Column: "id", Op: model.OpGt, Value: lastID})
	}
	// The page bound fired with the last page full: rows may remain unseen.
	return out, true, nil
}

// permittedInventoryKinds are the resource kinds whose PERMITTED SignalPolicy
// edges have NO observed-side source registered in the product: nothing collects
// who actually USED an LDAP directory, an Okta/Entra app or an Infisical
// project, so a grant on them being "never observed" is the expected steady
// state — inventory, not evidence of over-provisioning. reconcileDrift routes
// their unused grants to PrivilegeDiff.InventoryGrants instead of the headline
// UnusedGrants. A kind leaves this set the day an observed-side collector for it
// ships (the criterion is the collector's existence, not the grant's source).
var permittedInventoryKinds = map[string]bool{
	"ldap.directory":    true, // ldap privileged-directory grants
	"okta.app":          true, // idp Okta app-assignment grants
	"entra.app":         true, // idp Entra app/scope grants
	"infisical.project": true, // infisical project grants
}

// reconcileDrift cancels CROSS-ORIGIN false drift before classifying. The store
// merges an observation and a grant only when they share a natural key
// (origin_kind, origin_id, resource_id, mode). But in production the two sides
// arrive with different origins: a datastore observation is bridged to an AGENT
// (bridge.go), while a policy grant names the credential IDENTITY the agent runs
// as. They therefore live on different keys and the raw Drift reports BOTH a
// false unexpected access AND a false unused grant for one fully-permitted access
// (ARCHITECTURE.md — the worst failure of the killer feature). This pass reconciles
// them through the agent↔identity link (Agent.IdentityID, established by module
// VI):
//
//   - An observed agent edge and a permitted identity edge for the same
//     resource reconcile when the agent runs as that identity and the grant's
//     mode COVERS the observed mode (modeCovers: readwrite covers everything,
//     read covers read, write covers write) — the access is permitted, so BOTH
//     drop out. This holds even when the identity maps to several agents (which
//     the bridge refuses to attribute to one), and ONE grant reconciles ANY
//     NUMBER of observed edges it covers: a second agent running as the same
//     identity is not a violation. The grant is only marked used, never consumed.
//   - A grant whose own mode is UNKNOWN proves nothing: when one exists on the
//     same (principal, resource) and no covering grant matched, the observed
//     edge is flagged reconciliation_pending — honest uncertainty, not a firm
//     violation (docs/SECURITY-HARDENING.md). The same applies to an observed edge whose MODE is
//     unknown under a partial (read/write) grant: it can be neither proven nor
//     ruled out.
//   - When an observed edge cannot be tied to any identity (an agent with no
//     resolved identity yet) but a grant on the same resource could cover the
//     mode, the access is likewise flagged reconciliation_pending. It resolves
//     cleanly once links the credential to the agent.
//   - Unused grants on a resource kind with no observed-side collector
//     (permittedInventoryKinds) are inventory, not over-provisioning evidence,
//     and go to InventoryGrants.
func reconcileDrift(ctx context.Context, sc store.Scope, drifts []model.PrivilegeDrift) (PrivilegeDiff, error) {
	var observed, grants []model.AccessEdge
	for _, d := range drifts {
		if d.Kind == model.DriftUnusedGrant {
			grants = append(grants, d.Edge)
		} else {
			observed = append(observed, d.Edge)
		}
	}

	identOf := map[model.ID]model.ID{} // agentID -> IdentityID (cached; zero if none)
	agentIdentity := func(agentID model.ID) (model.ID, error) {
		if v, ok := identOf[agentID]; ok {
			return v, nil
		}
		a, err := sc.Agents().Get(ctx, agentID)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				identOf[agentID] = ""
				return "", nil
			}
			return "", err
		}
		identOf[agentID] = a.IdentityID
		return a.IdentityID, nil
	}
	// principal is the identity-level key an edge reconciles on: for an identity
	// origin, itself; for an agent origin, the identity it runs as (zero if none).
	principal := func(e model.AccessEdge) (model.ID, error) {
		switch e.OriginKind {
		case originIdentity:
			return e.OriginID, nil
		case originAgent:
			return agentIdentity(e.OriginID)
		default:
			return "", nil // session/other: not cross-reconciled here
		}
	}

	type pk struct{ principal, resource string }
	grantsByPrincipal := map[pk][]int{}                          // (principal, resource) -> grant indices
	grantModesByResource := map[model.ID][]sdkmodel.AccessMode{} // resource -> grant modes on ANY identity
	for i, g := range grants {
		p, err := principal(g)
		if err != nil {
			return PrivilegeDiff{}, err
		}
		grantModesByResource[g.ResourceID] = append(grantModesByResource[g.ResourceID], g.Mode)
		if !p.IsZero() {
			k := pk{p.String(), g.ResourceID.String()}
			grantsByPrincipal[k] = append(grantsByPrincipal[k], i)
		}
	}

	used := make([]bool, len(grants)) // decides UnusedGrants only — never consumption
	var out PrivilegeDiff
	for _, o := range observed {
		p, err := principal(o)
		if err != nil {
			return PrivilegeDiff{}, err
		}
		if !p.IsZero() {
			indices := grantsByPrincipal[pk{p.String(), o.ResourceID.String()}]
			covered, undecidable := false, false
			for _, gi := range indices {
				switch {
				case modeCovers(grants[gi].Mode, o.Mode):
					// Permitted via the shared identity. The grant stays available:
					// one grant reconciles every observed edge it covers (two agents
					// running as the same identity are both permitted).
					used[gi] = true
					covered = true
				case modeUndecidable(grants[gi].Mode, o.Mode):
					undecidable = true
				}
			}
			// Split grants jointly prove what neither proves alone: a read grant
			// PLUS a write grant on the same (principal, resource) — two policy
			// sources, or two policies of one source, each naming half — together
			// cover any observed mode exactly like a readwrite grant. Both halves
			// are exercised, so both are marked used.
			if !covered && grantUnionIsReadWrite(grants, indices) {
				for _, gi := range indices {
					if m := grants[gi].Mode; m == sdkmodel.ModeRead || m == sdkmodel.ModeWrite {
						used[gi] = true
					}
				}
				covered = true
			}
			if covered {
				continue // reconciled, not a drift
			}
			if undecidable {
				// A grant on this (principal, resource) can neither prove nor rule
				// out the observed mode: flag, do not headline (docs/SECURITY-HARDENING.md).
				markPending(&o)
			}
		} else if grantCouldCover(grantModesByResource[o.ResourceID], o.Mode) {
			// An access we cannot tie to an identity, but a grant on this resource
			// could cover the mode: cannot prove permitted — flag, do not headline.
			markPending(&o)
		}
		out.UnexpectedAccesses = append(out.UnexpectedAccesses, model.PrivilegeDrift{Edge: o, Kind: model.DriftViolation})
	}
	for i, g := range grants {
		if used[i] {
			continue
		}
		d := model.PrivilegeDrift{Edge: g, Kind: model.DriftUnusedGrant}
		if rk, _ := g.Metadata["resource_kind"].(string); permittedInventoryKinds[rk] {
			out.InventoryGrants = append(out.InventoryGrants, d)
			continue
		}
		out.UnusedGrants = append(out.UnusedGrants, d)
	}
	return out, nil
}

// modeCovers reports whether a grant of mode g PROVES an observed access of
// mode o is permitted: readwrite covers read/write/readwrite/unknown (whatever
// the access was, both halves were granted), read covers read, write covers
// write, and an exact match always covers. A grant whose own mode is UNKNOWN
// proves nothing — the caller treats that pairing as undecidable, never a
// silent reconciliation (docs/SECURITY-HARDENING.md).
func modeCovers(g, o sdkmodel.AccessMode) bool {
	switch g {
	case sdkmodel.ModeReadWrite:
		return true
	case sdkmodel.ModeUnknown:
		return false
	default:
		return g == o
	}
}

// modeUndecidable reports whether a grant of mode g can neither prove nor rule
// out an observed access of mode o: an unknown-mode grant proves nothing, and
// an unknown observed mode under a partial (read/write) grant may or may not be
// covered. (A readwrite grant is never undecidable — it covers everything.)
func modeUndecidable(g, o sdkmodel.AccessMode) bool {
	if g == sdkmodel.ModeUnknown {
		return true
	}
	return o == sdkmodel.ModeUnknown && g != sdkmodel.ModeReadWrite
}

// grantUnionIsReadWrite reports whether the indexed grants include BOTH a read
// and a write grant — jointly equivalent to readwrite for coverage.
func grantUnionIsReadWrite(grants []model.AccessEdge, indices []int) bool {
	var hasRead, hasWrite bool
	for _, gi := range indices {
		switch grants[gi].Mode {
		case sdkmodel.ModeRead:
			hasRead = true
		case sdkmodel.ModeWrite:
			hasWrite = true
		}
	}
	return hasRead && hasWrite
}

// grantCouldCover reports whether any grant mode on a resource could permit the
// observed mode — covering it outright, being undecidable, or jointly covering
// it as a read+write pair. Used for an observed edge with no resolved
// principal, where even a covering grant cannot PROVE the access (the identity
// link is missing), only soften it to pending.
func grantCouldCover(modes []sdkmodel.AccessMode, o sdkmodel.AccessMode) bool {
	var hasRead, hasWrite bool
	for _, m := range modes {
		if modeCovers(m, o) || modeUndecidable(m, o) {
			return true
		}
		switch m {
		case sdkmodel.ModeRead:
			hasRead = true
		case sdkmodel.ModeWrite:
			hasWrite = true
		}
	}
	return hasRead && hasWrite
}

// markPending flags an unexpected access whose permitted-ness cannot yet be
// decided (the agent↔identity link is unresolved). It mutates only the in-memory
// drift copy returned to the caller; nothing is persisted.
func markPending(e *model.AccessEdge) {
	if e.Metadata == nil {
		e.Metadata = map[string]any{}
	}
	e.Metadata["reconciliation_pending"] = true
}
