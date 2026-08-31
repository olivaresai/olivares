// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance

// (FED-1): ownership federation from the hyperscaler agent registries onto
// the NHI-lifecycle model. The federation connectors (entra-agent,
// agentcore, google-agent) carry the registry-declared accountable humans as
// roster attributes ("owner_ref"/"sponsor_ref" — Entra Agent ID's mandatory
// sponsorship is the pattern the model was shaped after); this bridge maps
// them onto the lifecycle record DURING roster reconciliation, exactly as the
// PUT /nhi/{ref}/ownership route would, so orphan detection (the sweep)
// watches federated agents with zero extra wiring. Deliberately does NOT
// reimplement any lifecycle mechanics here — it only feeds the existing model.

import (
	"context"
	"strings"

	"github.com/olivaresai/olivares/connectors/identitysource"
	"github.com/olivaresai/olivares/core/store"
)

// federatedOwnershipSources are the agent registries whose roster attributes are
// trusted to DECLARE ownership. A closed set on purpose: an arbitrary identity
// connector must not be able to (re)assign lifecycle ownership by smuggling an
// owner_ref attribute — only the federation sources, whose connectors read
// the registry's own owner/sponsor relationships, are honored.
var federatedOwnershipSources = map[identitysource.SourceKind]bool{
	identitysource.SourceEntraAgent:  true,
	identitysource.SourceAgentCore:   true,
	identitysource.SourceGoogleAgent: true,
}

// syncFederatedOwnership maps registry-declared owner/sponsor refs AND the
// registry's own orphan assertion from a federated graph onto the NHI lifecycle
// records, inside the SAME reconciliation transaction (so the refs resolve
// against the identities this very sync just upserted). Ownership semantics
// mirror handleSetNHIOwnership:
//   - each declared ref must resolve to a rostered HUMAN identity (owner and
//     sponsor are accountable people); an unresolvable or non-human ref skips
//     the whole identity (counted, surfaced in the sync audit — never a silent
//     gap, and never a half-assignment);
//   - only DECLARED (non-empty) values are written, so a registry that declares
//     a sponsor but no owner never clears an operator-set owner;
//   - the registry is the source of truth for what it declares: a changed
//     registry value overwrites the stored one on the next sync (last writer is
//     the federation, by design);
//   - an ownership change resets the orphan flag until the next sweep re-proves
//     it (the handler's "freshly (re)sponsored" rule) — unless the registry
//     itself still asserts orphanhood;
//   - a change appends the same "assigned" lifecycle event, attributed to the
//     federation connector.
//
// The registry-orphan signal (Attributes["orphaned"]=="true" — an Entra agent
// identity whose blueprint is gone) lands on its OWN column,
// colNHIRegistryOrphan: the sweep ORs it into `orphaned` and emits the
// nhi_orphaned finding on the transition, so the existing machinery surfaces it
// with its existing dedup semantics and the sponsor-liveness recomputation
// never clobbers it. Asserting it creates the lifecycle row if needed; CLEARING
// it (the attribute disappeared — the agent re-bound to a live blueprint) only
// updates an EXISTING row, so federated agents without lifecycle state never
// get a row minted just to say "not orphaned".
//
// It returns how many lifecycle records were written and how many identities
// were skipped for unresolvable/non-human refs.
func (m *Module) syncFederatedOwnership(ctx context.Context, sc store.Scope, graph identitysource.Graph) (set, skipped int, err error) {
	for _, id := range graph.Identities {
		if !federatedOwnershipSources[id.Source] {
			continue
		}
		// nil-map reads are safe: an identity with no attributes at all is the
		// fully-unasserted case and flows into the clear path below.
		owner := strings.TrimSpace(id.Attributes["owner_ref"])
		sponsor := strings.TrimSpace(id.Attributes["sponsor_ref"])
		registryOrphan := id.Attributes["orphaned"] == "true"
		if owner == "" && sponsor == "" && !registryOrphan {
			// Nothing asserted — except possibly a previously-asserted orphan to
			// clear, which only ever touches an existing row.
			if e := m.clearRegistryOrphan(ctx, sc, id); e != nil {
				return set, skipped, e
			}
			continue
		}
		// A blank Ref cannot anchor a lifecycle row (reconcileGraph skips such
		// identities for the same reason); counted as skipped — a federated row
		// that declared lifecycle state but has no anchor must not vanish silently.
		if strings.TrimSpace(id.Ref) == "" {
			skipped++
			continue
		}
		valid := true
		for _, ref := range []string{owner, sponsor} {
			if ref == "" {
				continue
			}
			found, human, _, e := resolveHumanIdentity(ctx, sc, ref)
			if e != nil {
				return set, skipped, e
			}
			if !found || !human {
				// The registry names someone the roster does not know as a human
				// (e.g. the directory connector for that cloud is not wired). The
				// honest move is to skip and count — assigning an unverifiable
				// accountable person would fabricate governance.
				valid = false
				break
			}
		}
		if !valid {
			skipped++
			continue
		}
		repo, rec, e := foLifecycle(ctx, sc, id.Ref)
		if e != nil {
			return set, skipped, e
		}
		actor := "connector:" + string(id.Source)
		changed, ownershipChanged := false, false
		if owner != "" && rec.String(colNHIOwnerRef) != owner {
			rec[colNHIOwnerRef] = owner
			rec[colNHIOwnerActor] = actor
			changed, ownershipChanged = true, true
		}
		if sponsor != "" && rec.String(colNHISponsorRef) != sponsor {
			rec[colNHISponsorRef] = sponsor
			rec[colNHISponsorActor] = actor
			changed, ownershipChanged = true, true
		}
		if rec.Bool(colNHIRegistryOrphan) != registryOrphan {
			rec[colNHIRegistryOrphan] = registryOrphan
			changed = true
		}
		if !changed {
			continue // idempotent: the registry and the record already agree
		}
		// A freshly (re)sponsored NHI is no longer orphaned until the next sweep
		// proves otherwise (handleSetNHIOwnership parity) — unless the registry
		// itself still asserts orphanhood, which the sweep would re-OR anyway.
		if ownershipChanged && !registryOrphan {
			rec[colNHIOrphaned] = false
		}
		if _, e := repo.Update(ctx, rec); e != nil {
			return set, skipped, e
		}
		detail := "owner=" + owner + " sponsor=" + sponsor + " (federated from " + string(id.Source) + ")"
		evt := "assigned"
		if !ownershipChanged {
			evt = "orphaned"
			detail = "registry-asserted orphan (federated from " + string(id.Source) + ")"
		}
		if e := m.recordLifecycleEvent(ctx, sc, id.Ref, evt, actor, "", detail); e != nil {
			return set, skipped, e
		}
		set++
	}
	return set, skipped, nil
}

// clearRegistryOrphan clears a previously registry-asserted orphan flag when
// the registry's current sync no longer asserts it — touching only an EXISTING
// lifecycle row (never minting one to record an absence). The sweep then
// recomputes `orphaned` from sponsor liveness alone on its next pass.
func (m *Module) clearRegistryOrphan(ctx context.Context, sc store.Scope, id identitysource.Identity) error {
	ref := strings.TrimSpace(id.Ref)
	if ref == "" {
		return nil
	}
	repo, err := sc.Ext(nhiLifecycleKind)
	if err != nil {
		return err
	}
	rec, found, err := findOne(ctx, repo, eq(colNHIIdentityRef, ref))
	if err != nil || !found || !rec.Bool(colNHIRegistryOrphan) {
		return err
	}
	rec[colNHIRegistryOrphan] = false
	if _, err := repo.Update(ctx, rec); err != nil {
		return err
	}
	return m.recordLifecycleEvent(ctx, sc, ref, "orphaned", "connector:"+string(id.Source), "",
		"registry orphan assertion cleared (federated from "+string(id.Source)+")")
}
