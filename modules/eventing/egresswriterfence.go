// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only

package eventing

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// The writer fence — Unit H.
//
// Unit G made the egress destination control's disposition durable, and left exactly one limit
// declared as UNVERIFIED: nothing proves that every node able to author a destination carries the
// gate. A binary that predates G does not consult the classification, so it can create or re-point
// a subscription whose destination passed no policy, no classification and no compatibility
// record — and under compatibility mode it produces a subscription that is NOT grandfathered and
// will die the moment a policy is authored, without anybody having decided that.
//
// The actuation ceremony asks for --assert-writers-upgraded and RECORDS AN ASSERTION. This file is
// what turns that signature into infrastructure.
//
// What it promises, with the verb that survives scrutiny: it does NOT verify the fleet's
// composition and it does NOT verify the past. It makes a future violation ENFORCEABLE AND
// OBSERVABLE — a writer that does not carry the gate fails visibly and by name instead of
// succeeding silently. An earlier draft of this design said "arming makes the assertion
// verifiable", which is a stronger verb than the mechanism earns.

// EgressWriterFenceControlKey is the rollout control that carries the fence's durable
// requirement.
//
// It is a SEPARATE key from the destination control on purpose, and that separation is the
// correction an adversarial review of this design produced — it was wrong in both directions
// before:
//
//   - An installation created AFTER unit G but before this one is classified `enforced` by G,
//     because G's question is "did this deployment predate the destination control". Its fleet may
//     still not know the fence. Arming from G's answer would activate the fence while a pre-fence
//     leader is still serving authoring, and its next create would fail — during a rolling update
//     that is supposed to be invisible.
//   - An estate that had already COMMITTED G to enforced before this unit existed could never be
//     armed at all: the actuation command returns "nothing to do" once the commitment is set
//     (cmd/olivares/cmd_eventing_egress.go).
//
// Unit G's own contract states the rule that resolves it: a control whose MEANING changes ships as
// a new key so it cannot inherit a decision taken about a different rule
// (core/store/rollout.go). This is that new key. Registering it needs no engine change — the
// ExtensionRegistry seam unit G added already accepts a second control.
const EgressWriterFenceControlKey = "eventing.egress.writer_fence.v1"

// EgressWriterCapability is what THIS binary declares it can do: consult the durable disposition
// of the egress destination control before accepting a destination.
//
// It is a capability level, not a release version, and the distinction is deliberate: a backport
// that carries the gate can declare it honestly, and a release that does not carry it cannot claim
// it by being newer. It is monotone — a later level means "everything the previous level meant,
// plus more" — so the comparison in the fence is `declared >= required` and never equality.
//
// Level 1 means: this binary reads the destination control's durable rollout state and refuses a
// destination the disposition does not permit (units F and G).
const EgressWriterCapability int64 = 1

// egressWriterFenceControl is the declaration the engine classifies, once, under the migration
// lock and before it creates this module's tables.
//
// The witness is the same table the destination control uses, and it answers a DIFFERENT question
// here. For the destination control it meant "could a destination have been authored before the
// gate existed". For the fence it means "could a writer that does not know about the fence have
// written here" — and because the fence is younger, the answer for every pre-existing deployment
// is yes:
//
//	witness absent  → enforced       nothing that predates the fence ever wrote here
//	witness present → legacy_compat  a fleet that may not know the fence exists; arming would
//	                                 break the rolling update that is replacing it
//
// So a deployment created before this unit is DORMANT by classification, whatever the destination
// control says about it, and an operator arms it deliberately once the fleet has converged.
func egressWriterFenceControl() store.RolloutControl {
	return store.RolloutControl{
		Key:          EgressWriterFenceControlKey,
		WitnessTable: subscriptionTable,
		LegacyMode:   store.RolloutLegacyCompat,
		FreshMode:    store.RolloutEnforced,
	}
}

// EgressWriterFenceSource reads the fence's durable state. The composition root wires it over the
// store's store.RolloutStater capability, exactly as it wires the destination control's.
type EgressWriterFenceSource interface {
	EgressWriterFence(ctx context.Context) (store.RolloutState, error)
}

// WithEgressWriterFence wires the fence's durable state.
func WithEgressWriterFence(s EgressWriterFenceSource) Option {
	return func(m *Module) {
		if s != nil {
			m.writerFence = s
		}
	}
}

// FenceRequirement is what the fence demands of a writer right now.
type FenceRequirement struct {
	// Armed reports that a writer must prove its capability to mutate a destination. It is false
	// on any deployment the fence was classified DORMANT on and that nobody has armed.
	Armed bool
	// RequiredCapability is the minimum level a writer must declare. Zero when dormant.
	RequiredCapability int64
	// Generation is the fence state's generation, carried into the attestation so a writer cannot
	// prove a capability against a disposition it read before the last decision.
	Generation int64
	// Mode and Committed mirror the durable rollout record, for the status surface.
	Mode      store.RolloutMode
	Committed bool
}

// resolveFence reads the fence's durable state and turns it into what a writer must satisfy.
//
// A nil source is NOT armed, which is the only upgrade-safe reading of a seam an embedder has not
// adopted — and it is the same asymmetry unit G settled: the module tolerates it, and the
// first-party composition root treats a store without the capability as a boot failure, so the
// tolerance cannot become the shipped behavior by accident.
//
// An UNREADABLE state is an error the caller must surface, never a dormant fence. "This plane
// could not establish whether the fence is armed" must not be delivered as "the fence is not
// armed" — the failure this campaign keeps finding.
func (m *Module) resolveFence(ctx context.Context) (FenceRequirement, error) {
	if m.writerFence == nil {
		return FenceRequirement{}, nil
	}
	st, err := m.writerFence.EgressWriterFence(ctx)
	if err != nil {
		return FenceRequirement{}, fmt.Errorf("eventing: the durable state of %q is unreadable: %w", EgressWriterFenceControlKey, err)
	}
	if !st.CurrentMode.Valid() {
		return FenceRequirement{}, fmt.Errorf("eventing: %q holds mode %q, which this binary does not know", st.Key, st.CurrentMode)
	}
	return FenceRequirement{
		Armed:              st.CurrentMode == store.RolloutEnforced,
		RequiredCapability: fenceRequiredCapability(st),
		Generation:         st.Generation,
		Mode:               st.CurrentMode,
		Committed:          st.EnforcementCommitted,
	}, nil
}

// fenceRequiredCapability maps a durable mode to the level a writer must declare.
//
// Only `enforced` demands anything. `legacy_compat` is the dormant classification every
// pre-existing deployment gets, and `policy_optional` is a recorded decision that the DESTINATION
// control need not be configured — which says nothing about writer versions, so it does not arm
// the fence either. Reading `policy_optional` as "armed" would surprise an operator who chose it
// for a laboratory box.
func fenceRequiredCapability(st store.RolloutState) int64 {
	if st.CurrentMode == store.RolloutEnforced {
		return EgressWriterCapability
	}
	return 0
}

// WriterProof is the SINGLE implementation of "this transaction's writer carries the egress gate".
//
// One implementation is the point, not a convenience. This campaign has already shipped a second
// copy of a destination rule twice — the CLI's endpoint check and the CLI's HTTP client — and both
// times the copy was the one that was wrong. Every path that introduces or moves a destination goes
// through this: the API's create, update and restore, the CLI's create, and every effective change
// of a sink profile.
type WriterProof struct {
	// Capability is what this binary declares. Every path this module ships sets it from the
	// compiled-in constant, and the exported CLI helper does too — but the field is exported, so a
	// Go caller inside the binary CAN construct a WriterProof with any value. That is not a hole in
	// the fence: such a caller is already inside the process and can write the governed table
	// directly. It is a correction to an earlier comment here, which said "never a parameter a
	// caller can raise" and described the discipline rather than the type.
	Capability int64
	// Generation is the fence generation this writer OBSERVED. The fence compares it, so a node
	// whose cached read is stale is refused and retries rather than proving a capability against a
	// disposition that has since moved.
	Generation int64
}

// writerProof builds the proof for this module's current view of the fence.
//
// IT MUST BE CALLED BEFORE THE TRANSACTION OPENS, and that is a correctness rule rather than a
// style preference. Reading the fence's durable state takes a pooled connection, and this engine
// pins SQLite to exactly ONE (core/internal/store/sqlstore/store.go:754, "SQLite is
// single-writer"), so a read issued from inside an open store transaction waits for the connection
// that transaction is already holding — an unbounded hang in the first-party binary, which wires
// this seam fatally at boot. On PostgreSQL it does not deadlock; it quietly takes a SECOND
// connection per governed write and reads OUTSIDE the transaction's snapshot, which is a slower way
// of being wrong. This module already obeys the rule for the secret sealer, in the same handlers,
// with the same words: the sealer is never invoked inside an open store transaction.
//
// Reading before the transaction is also exactly what the design says a writer does: the proof
// carries the generation the writer OBSERVED, and if an arming moves it in between, the fence
// refuses the write and the caller retries. A stale read is a refusal, never a silent pass — and on
// PostgreSQL the shared lock the trigger takes on the control row is what makes the arming wait for
// writers already in flight.
//
// It reads the fence even when it is DORMANT, and stamps regardless. That is deliberate: if writers
// only stamped once armed, arming would change writer behavior at the same instant it changes the
// database's, and the window between the two would be exactly the one nobody tested. Stamping
// unconditionally makes arming a database-side change alone — every writer already behaves as the
// armed world expects, so arming cannot surprise one.
func (m *Module) writerProof(ctx context.Context) (WriterProof, error) {
	req, err := m.resolveFence(ctx)
	if err != nil {
		return WriterProof{}, err
	}
	return WriterProof{Capability: EgressWriterCapability, Generation: req.Generation}, nil
}

// nonceBytes is the width of a mutation nonce. 32 bytes of crypto/rand: the fence's only defense
// against an orphaned proof being bound to a future mutation is that no row ever received its
// nonce, so the value has to be unguessable rather than merely unique.
const nonceBytes = 32

// Stamp writes the proof into the caller's OPEN TRANSACTION and returns the nonce the caller must
// put on the governed row.
//
// Both halves matter and neither is optional:
//
//   - The write goes through the caller's Scope, so the proof commits or rolls back WITH the
//     mutation it authorizes. A panic or an error anywhere in the closure takes the proof with it;
//     there is no path that leaves a usable proof behind a mutation that did not happen.
//   - The returned nonce must land on the row. A proof that exists without being bound to a row is
//     the shape that authorized an old write on SQLite, and the reason the nonce is on the row at
//     all.
func (p WriterProof) Stamp(ctx context.Context, sc store.Scope) (string, error) {
	repo, err := sc.Ext(writerAttestKind)
	if err != nil {
		return "", err
	}
	raw := make([]byte, nonceBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("eventing: writer proof: %w", err)
	}
	nonce := hex.EncodeToString(raw)
	if _, err := repo.Create(ctx, model.Record{
		colAttestNonce:      nonce,
		colAttestCapability: p.Capability,
		colAttestGeneration: p.Generation,
	}); err != nil {
		return "", err
	}
	return nonce, nil
}

// StampInto is the shape every governed write uses: write the proof and put its nonce on the record
// about to be created or updated.
//
// It takes the record so a caller cannot forget the second half. Returning only the nonce and
// trusting each stamping site to assign it is exactly how one of them would
// eventually not. (Six until the P1-1 fix put credential rotation under the fence; the number was
// corrected in the PR and the lane status and left stale HERE, in the file most people read.)
//
// The PROOF is built earlier, outside the transaction (writerProof), and carried in. Splitting the
// two halves is what makes the rule enforceable by shape: everything StampInto does is a write, so
// there is no read left inside the transaction to get wrong. One WriterProof value can stamp
// SEVERAL rows in the same transaction — each call mints its own nonce and its own attestation,
// because the fence consumes one per governed mutation.
func (p WriterProof) StampInto(ctx context.Context, sc store.Scope, rec model.Record) error {
	nonce, err := p.Stamp(ctx, sc)
	if err != nil {
		return err
	}
	rec[colWriterNonce] = nonce
	return nil
}

// StampWriterProof is the writer proof for a caller OUTSIDE this module — the CLI, which writes a
// subscription directly and is therefore a second writer against the same database.
//
// It is exported rather than duplicated because a second copy of a destination rule has been the
// wrong one twice in this campaign: the CLI's endpoint check refused every hostname the engine
// accepted, and the CLI's HTTP client carried a narrower reserved-address set. The caller supplies
// the fence generation it observed; the capability is this binary's compiled-in constant and is not
// a parameter, so no caller can raise it.
func StampWriterProof(ctx context.Context, sc store.Scope, rec model.Record, generation int64) error {
	return WriterProof{Capability: EgressWriterCapability, Generation: generation}.StampInto(ctx, sc, rec)
}

// isEgressWriterFenceRefusal reports that a store error is THIS fence refusing.
//
// It matches the message the migrations raise on both engines. That is a string comparison across a
// layer, which is not free of risk, and the alternative was worse: the engines report the refusal as
// a generic constraint violation, so the only other option is to let it fall through to a 500 — and
// then the writer that "is refused and retries" has no way to know it should.
func isEgressWriterFenceRefusal(err error) bool {
	return err != nil && strings.Contains(err.Error(), "eventing egress writer fence")
}

// FenceGeneration reads the generation a writer must attest against, for a caller outside this
// module.
//
// A nil source returns generation zero with no error, and that is a deliberate seam for an embedder
// that has not adopted the fence — NOT a claim that an unreadable fence is safe. An unreadable one
// is an error. The first-party composition root makes a store without the capability a boot failure
// precisely so the nil case cannot become the shipped behavior (cmd/olivares/boot.go). An earlier
// version of this comment said "an unreadable fence is an error, never generation zero", which
// described the second half and not the first.
func FenceGeneration(ctx context.Context, src EgressWriterFenceSource) (int64, error) {
	if src == nil {
		return 0, nil
	}
	st, err := src.EgressWriterFence(ctx)
	if err != nil {
		return 0, fmt.Errorf("eventing: the durable state of %q is unreadable: %w", EgressWriterFenceControlKey, err)
	}
	return st.Generation, nil
}
