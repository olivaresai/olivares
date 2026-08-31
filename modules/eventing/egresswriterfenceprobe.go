// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only

package eventing

import (
	"context"
	"errors"
	"fmt"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// The writer fence's PROBE MATRIX — Unit H.
//
// A ceremony that wants to know whether the database really enforces the fence cannot ask the
// catalog. Listing triggers proves an object exists; it does not prove it is attached to the right
// table, that it is ENABLED, that its function survived a restore, or that it REJECTS. A restored
// dump, a promoted logical replica or a hand-repaired database can each satisfy the catalog and
// enforce nothing. What the fence promises is a refusal, so a refusal is what gets measured.
//
// ONE PROBE PER GOVERNED MUTATION, and that is the correction an adversarial review of this
// implementation produced. The first version attempted a single INSERT and the ceremony reported
// ENFORCING for the whole fence: drop the UPDATE trigger, or any of the triggers on the sink table, and
// `verify` still returned green — in exactly the restores and repairs it exists for. A behavioral
// probe is stronger than a catalog query for the path it executes, and says nothing whatsoever about
// the paths it never touches.
//
// IT LIVES IN THE MODULE, not in the CLI, because the module owns the governed surface. Putting it
// next to the ceremony would have meant a THIRD copy of the column names — after the entity
// descriptor and the migration SQL — and this campaign has been paid twice already for copies of a
// destination rule drifting apart. Here the probes are built from the same constants the writers
// use, so a column that gets renamed breaks the build instead of quietly un-probing a trigger.
//
// NOTHING A PROBE WRITES EVER COMMITS. Each Attempt is meant to be called inside a transaction the
// caller rolls back unconditionally, and every one of them returns a non-nil error precisely so the
// caller's transaction cannot commit by falling through.

// ErrFenceProbeAccepted is what an Attempt returns when the governed mutation was ACCEPTED — which,
// on an armed deployment, is the finding: that trigger is absent, disabled, or attached elsewhere.
//
// It doubles as the sentinel that unwinds the caller's transaction, so the probe's own rows never
// survive it.
// ErrFenceProbeSeedFailed marks an error raised while SEEDING a probe, before the ungoverned
// mutation under test ever ran.
//
// It exists because the seeds write GOVERNED rows, carrying real proofs — so a seed that fails
// fails with the fence's own refusal, and the dispatcher, which classified by message alone, counted
// it as "this mutation was refused". That reads as enforcement for a trigger the probe never
// reached: `verify` would report ENFORCING having tested nothing, in the restores and repairs the
// ceremony exists for. Phase is now part of the answer instead of being inferred from the text.
var ErrFenceProbeSeedFailed = errors.New("eventing: writer fence probe: SEEDING failed, so the governed mutation under test never ran")

// seedErr tags an error as belonging to the seeding phase, keeping the cause inspectable.
func seedErr(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%w: %w", ErrFenceProbeSeedFailed, err)
}

var ErrFenceProbeAccepted = errors.New("eventing: writer fence probe: the governed mutation was ACCEPTED with no capability attestation")

// FenceProbe is one governed mutation, attempted the way a binary that does not carry the gate would
// attempt it.
type FenceProbe struct {
	// Name is what an operator reads when this one is the mutation the database still accepts.
	Name string
	// Table and Op are the governed (table, operation) this probe exercises, spelled the way the
	// migration spells them: the table name, and `ins` / `upd` / `del`.
	//
	// They exist so coverage is DERIVABLE instead of asserted against a literal. A test scans the
	// embedded fence migrations for the triggers they declare and requires every declared
	// (table, operation) to have at least one probe — so adding a governed surface without a probe
	// fails, which is what the count assertion claimed to do and did not: comparing
	// len(FenceProbes()) to the literal 6 goes red when a PROBE is removed, never when a TRIGGER is
	// added.
	Table string
	Op    string
	// Attempt seeds whatever the mutation needs — WITH proofs, since seeding is not what is being
	// tested — then performs the ungoverned mutation. It returns ErrFenceProbeAccepted when the
	// mutation succeeded, the store's error when the fence refused it, and any other error as
	// itself.
	Attempt func(ctx context.Context, sc store.Scope, generation int64) error
}

// FenceProbeRowName is the name every probe row carries. Nothing it writes ever commits, and the
// name says so in case a future failure mode ever leaves one visible.
//
// EXPORTED so the leftover check has ONE copy of it. It was unexported, and the two tests that
// assert the probe leaves nothing behind wrote the literal themselves — with a trailing hyphen the
// constant does not have. `strings.HasPrefix(name, "olivares-writer-fence-probe-")` never matched,
// so both checks passed on any tree, including one where the probe committed its row. A second copy
// of a name is the same defect as a second copy of a rule, and this campaign has now paid for it
// twice (`sink_cred` vs `sink_cred_sealed` was the first).
const FenceProbeRowName = "olivares-writer-fence-probe"

// fenceProbeName is the internal spelling, kept so the module's own call sites read unchanged.
const fenceProbeName = FenceProbeRowName

// fenceProbeEndpoint is an endpoint that cannot resolve. A probe row never commits and is seeded
// disabled, so nothing could ever dial it; using an unroutable name means that even a bug which
// committed one could not turn into egress.
const fenceProbeEndpoint = "https://writer-fence-probe.invalid/never-delivered"

// probeSubscription seeds a subscription the way a writer carrying the gate would.
func probeSubscription(ctx context.Context, sc store.Scope, generation int64, enabled bool) (model.ID, error) {
	repo, err := sc.Ext(subscriptionKind)
	if err != nil {
		return "", seedErr(err)
	}
	rec := model.Record{
		colSubName: fenceProbeName, colSubEnabled: enabled, colSubTypes: "finding.reported",
		colSubEndpoint: fenceProbeEndpoint, colSubSecret: "sealed:probe", colSubSecretHint: "probe",
		colSubRole: "viewer", colSubOwnerActor: "cli:fence-probe", colSubOwnerActorK: "user",
		colSubAuthType: authTypeNone,
	}
	if err := StampWriterProof(ctx, sc, rec, generation); err != nil {
		return "", seedErr(err)
	}
	created, err := repo.Create(ctx, rec)
	if err != nil {
		return "", seedErr(err)
	}
	return model.ID(created.String(model.ColID)), nil
}

// probeSink seeds a sink profile for a subscription, with its own proof.
func probeSink(ctx context.Context, sc store.Scope, generation int64, subID model.ID) error {
	sinks, err := sc.Ext(subscriptionSinkKind)
	if err != nil {
		return seedErr(err)
	}
	rec := model.Record{
		colSinkSubRef: subID.String(), colSinkKind: "splunk_hec",
		colSinkFormat: "", colSinkCred: "sealed:probe", colSinkOpts: "", colSinkHint: "p",
	}
	if err := StampWriterProof(ctx, sc, rec, generation); err != nil {
		return seedErr(err)
	}
	_, err = sinks.Create(ctx, rec)
	return seedErr(err)
}

// probeSinkRow reads back the single seeded sink row.
func probeSinkRow(ctx context.Context, sc store.Scope) (model.Record, error) {
	sinks, err := sc.Ext(subscriptionSinkKind)
	if err != nil {
		return nil, err
	}
	rows, _, err := sinks.List(ctx, model.Query{Limit: 2})
	if err != nil {
		return nil, err
	}
	if len(rows) != 1 {
		return nil, fmt.Errorf("eventing: writer fence probe: seeded %d sink rows, want 1", len(rows))
	}
	return rows[0], nil
}

// FenceProbes returns one probe per governed mutation: the whole surface the fence claims, in the
// order an operator reads it.
//
// Every probe that needs an existing row seeds it WITH a proof, so what it measures is the
// UNGOVERNED mutation that follows and not the seeding. The seed's proof is consumed by its own
// insert, so carrying the stored nonce forward — which is exactly what a binary without the gate
// does, since its descriptor has no such column to clear — leaves nothing live to match.
func FenceProbes() []FenceProbe {
	return []FenceProbe{
		{
			Name:  "subscription INSERT",
			Table: "eventing_subscription", Op: "ins",
			Attempt: func(ctx context.Context, sc store.Scope, _ int64) error {
				repo, err := sc.Ext(subscriptionKind)
				if err != nil {
					return err
				}
				// No writer_nonce at all: the shape a binary whose descriptor predates the fence
				// emits, because the column does not exist in it.
				if _, err := repo.Create(ctx, model.Record{
					colSubName: fenceProbeName, colSubEnabled: false, colSubTypes: "finding.reported",
					colSubEndpoint: fenceProbeEndpoint, colSubSecret: "sealed:probe", colSubSecretHint: "probe",
					colSubRole: "viewer", colSubOwnerActor: "cli:fence-probe", colSubOwnerActorK: "user",
					colSubAuthType: authTypeNone,
				}); err != nil {
					return err
				}
				return ErrFenceProbeAccepted
			},
		},
		{
			Name:  "subscription UPDATE that MOVES the destination",
			Table: "eventing_subscription", Op: "upd",
			Attempt: func(ctx context.Context, sc store.Scope, generation int64) error {
				id, err := probeSubscription(ctx, sc, generation, false)
				if err != nil {
					return err
				}
				repo, err := sc.Ext(subscriptionKind)
				if err != nil {
					return err
				}
				rec, err := repo.Get(ctx, id)
				if err != nil {
					return err
				}
				rec[colSubEndpoint] = "https://writer-fence-probe.invalid/moved"
				if _, err := repo.Update(ctx, rec); err != nil {
					return err
				}
				return ErrFenceProbeAccepted
			},
		},
		{
			Name:  "subscription UPDATE that REACTIVATES a dormant destination",
			Table: "eventing_subscription", Op: "upd",
			Attempt: func(ctx context.Context, sc store.Scope, generation int64) error {
				id, err := probeSubscription(ctx, sc, generation, false)
				if err != nil {
					return err
				}
				repo, err := sc.Ext(subscriptionKind)
				if err != nil {
					return err
				}
				rec, err := repo.Get(ctx, id)
				if err != nil {
					return err
				}
				rec[colSubEnabled] = true
				if _, err := repo.Update(ctx, rec); err != nil {
					return err
				}
				return ErrFenceProbeAccepted
			},
		},
		{
			Name:  "sink profile INSERT",
			Table: "eventing_subscription_sink", Op: "ins",
			Attempt: func(ctx context.Context, sc store.Scope, generation int64) error {
				id, err := probeSubscription(ctx, sc, generation, false)
				if err != nil {
					return err
				}
				sinks, err := sc.Ext(subscriptionSinkKind)
				if err != nil {
					return err
				}
				if _, err := sinks.Create(ctx, model.Record{
					colSinkSubRef: id.String(), colSinkKind: "splunk_hec",
					colSinkFormat: "", colSinkCred: "sealed:probe", colSinkOpts: "", colSinkHint: "p",
				}); err != nil {
					return err
				}
				return ErrFenceProbeAccepted
			},
		},
		{
			Name:  "sink profile UPDATE that MOVES the rendered destination",
			Table: "eventing_subscription_sink", Op: "upd",
			Attempt: func(ctx context.Context, sc store.Scope, generation int64) error {
				id, err := probeSubscription(ctx, sc, generation, false)
				if err != nil {
					return err
				}
				if err := probeSink(ctx, sc, generation, id); err != nil {
					return err
				}
				rec, err := probeSinkRow(ctx, sc)
				if err != nil {
					return err
				}
				sinks, err := sc.Ext(subscriptionSinkKind)
				if err != nil {
					return err
				}
				rec[colSinkKind] = "https"
				if _, err := sinks.Update(ctx, rec); err != nil {
					return err
				}
				return ErrFenceProbeAccepted
			},
		},
		{
			Name:  "sink profile DELETE while the subscription LIVES",
			Table: "eventing_subscription_sink", Op: "del",
			Attempt: func(ctx context.Context, sc store.Scope, generation int64) error {
				id, err := probeSubscription(ctx, sc, generation, false)
				if err != nil {
					return err
				}
				if err := probeSink(ctx, sc, generation, id); err != nil {
					return err
				}
				rec, err := probeSinkRow(ctx, sc)
				if err != nil {
					return err
				}
				sinks, err := sc.Ext(subscriptionSinkKind)
				if err != nil {
					return err
				}
				if err := sinks.Delete(ctx, model.ID(rec.String(model.ColID))); err != nil {
					return err
				}
				return ErrFenceProbeAccepted
			},
		},
	}
}
