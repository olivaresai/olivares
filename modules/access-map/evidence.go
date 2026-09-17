// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package accessmap

import (
	"context"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/sdk"
)

// evidence.go — the access-map module's READ glue over the v26.9 access-evidence
// records (increment A).
//
// # What this is
//
// The engine keeps policy artifacts, authority transitions, observed action
// stages and authorization decisions in four separate, immutable relations
// (core/store.AccessEvidenceRepo). This file is the module's typed way to read
// the exact separate records back and see their dependency/completeness status.
// It exists here rather than in the store because the access map is the surface
// that will eventually PROJECT them — and a projection that had to re-derive the
// record layout for itself is how two readings of the same evidence appear.
//
// # What this deliberately is not
//
//   - It adds NO route and NO permission. The projection API, its filters and
//     its permissions are increment D; putting a read here does not publish one.
//   - It WRITES nothing. Producers — the runtime, the hooks, the MCP gateway and
//     the source-scope resolver — are increments B and C. In particular this
//     module remains the sole writer of AccessEdge and its Ingest path is
//     untouched: the edge projection and these records are separate, and neither
//     is derived from the other here.
//   - It FUSES nothing. A case links an observation to a decision only when a
//     producer recorded that link; nothing infers a decision from an edge, an
//     allow from a missing denial, or an effect from a dispatch.
//   - It AUTHORIZES nothing. A returned decision with outcome allow is a
//     historical fact about an evaluator at an instant. Whether an action is
//     permitted NOW is a question for the real resolver and the unchanged
//     evidence-or-refuse law, never for a row read from here.

// EvidenceCase is one action's evidence, assembled from the separate records
// without merging them.
//
// The fields stay apart on purpose. Observation is what a boundary declared it
// saw; Decision is what an evaluator decided; Artifacts are the retained inputs
// that decision named; Completeness is this store's own verdict on whether those
// inputs suffice to re-evaluate it. Collapsing any two of them into a single
// "verdict" is exactly the conflation the contract forbids.
type EvidenceCase struct {
	// Observation is the observed stage the case is about.
	Observation model.ActionObservation
	// Decision is the governing decision, present only when the observation
	// recorded a link to one. DecisionRecorded distinguishes "no decision was
	// linked" from "a deny": an absent decision is an absence, never a denial.
	Decision         model.AuthorizationDecision
	DecisionRecorded bool
	// Completeness is the store's verdict on the decision's inputs. It is
	// meaningful only when DecisionRecorded.
	Completeness model.AccessEvidenceCompleteness
	// Artifacts are the decision's REQUIRED policy-artifact inputs that resolved
	// in this tenant. An input that did not resolve is absent from this slice and
	// named in Completeness.MissingRefs — the two are reported separately so a
	// reader cannot mistake a short slice for a complete one.
	Artifacts []model.PolicyArtifact
}

// Reconstructible reports whether the case's decision can be re-evaluated from
// the retained data alone. A case with no recorded decision is NOT
// reconstructible, and saying so is the point: there is nothing to replay.
func (c EvidenceCase) Reconstructible() bool {
	return c.DecisionRecorded && c.Completeness.Reconstructible()
}

// EvidenceCase reads one observation and the records it references.
//
// It is a read: it opens a read-only transaction, so it can neither write nor be
// mistaken for an ingest path. Every lookup is tenant-pinned by that scope, so a
// reference to another tenant's record resolves to nothing here exactly as it
// does at write time.
func (m *Module) EvidenceCase(ctx context.Context, tenant model.TenantID, observationID model.ID) (EvidenceCase, error) {
	if m.data == nil {
		return EvidenceCase{}, errNoData
	}
	var out EvidenceCase
	err := m.data.View(ctx, tenant, func(sc store.Scope) error {
		ev := sc.AccessEvidence()
		obs, err := ev.ActionObservation(ctx, observationID)
		if err != nil {
			return err
		}
		out.Observation = obs
		if obs.DecisionID.IsZero() {
			return nil // no decision was linked; that is an absence, not a deny
		}
		decision, err := ev.AuthorizationDecision(ctx, obs.DecisionID)
		if err != nil {
			return err
		}
		out.Decision, out.DecisionRecorded = decision, true
		completeness, err := ev.DecisionCompleteness(ctx, obs.DecisionID)
		if err != nil {
			return err
		}
		out.Completeness = completeness
		for _, dep := range decision.Decision.Inputs {
			if !dep.Required || dep.Kind != sdk.DependencyPolicyArtifact {
				continue
			}
			id, perr := model.ParseID(dep.Ref)
			if perr != nil {
				continue // reported by Completeness.MissingRefs, never silently "resolved"
			}
			artifact, aerr := ev.PolicyArtifact(ctx, id)
			if aerr != nil {
				continue // likewise: absence is Completeness's to report, not this loop's
			}
			out.Artifacts = append(out.Artifacts, artifact)
		}
		return nil
	})
	if err != nil {
		return EvidenceCase{}, err
	}
	return out, nil
}

// EvidenceStages returns the separate observed stages recorded against one
// canonical question digest, oldest first by the instant each fact occurred.
//
// It returns the stages, not a conclusion. A caller that wants to know whether
// an effect happened reads the stage and its confirmation level; this function
// will never answer that by summarizing, because "requested", "dispatched" and
// "effect_confirmed(receipt)" are three different facts and the summary would
// have to pick one.
func (m *Module) EvidenceStages(ctx context.Context, tenant model.TenantID, questionDigest string) ([]model.ActionObservation, error) {
	if m.data == nil {
		return nil, errNoData
	}
	var out []model.ActionObservation
	err := m.data.View(ctx, tenant, func(sc store.Scope) error {
		stages, err := sc.AccessEvidence().ActionObservationsForQuestion(ctx, questionDigest)
		if err != nil {
			return err
		}
		out = stages
		return nil
	})
	return out, err
}

// AuthorityHistory returns the recorded authority transitions for one subject
// reference, ordered by the instant the issuer attests.
//
// It is history, not current authority. Deciding what is in force now requires
// the real evaluator's own selection — deny/forbid, defaults, globals,
// assignments and constraints — and that selection is the next increment's work.
// A caller must not read the last row of this slice as "the current permission".
func (m *Module) AuthorityHistory(ctx context.Context, tenant model.TenantID, subjectRef string) ([]model.AuthorityTransition, error) {
	if m.data == nil {
		return nil, errNoData
	}
	var out []model.AuthorityTransition
	err := m.data.View(ctx, tenant, func(sc store.Scope) error {
		history, err := sc.AccessEvidence().AuthorityTransitionsFor(ctx, subjectRef)
		if err != nil {
			return err
		}
		out = history
		return nil
	})
	return out, err
}
