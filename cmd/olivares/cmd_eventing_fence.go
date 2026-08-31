// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/eventing"
)

// The operator's side of the unit-H writer fence: read its posture, arm it, and verify that
// the database is actually enforcing it.
//
// It is a SEPARATE ceremony from `eventing egress`, because it is a decision about a different
// thing. The destination control governs WHERE a tenant may point its stream; the fence governs
// WHICH BINARIES may author one. Unit G's own contract is the reason they cannot share a lever: a
// control whose meaning changes ships as a new key, so it cannot inherit a decision taken about a
// different rule.
//
// WHAT ARMING BUYS, in the verb the mechanism earns. It does not prove the fleet's composition and
// it does not prove the past. It makes a future violation ENFORCEABLE AND OBSERVABLE: a node that
// does not carry the egress gate fails visibly and by name instead of succeeding silently. The
// actuation ceremony for the destination control asks the operator to ASSERT that every authoring
// node is upgraded and records the assertion; this is what turns that signature into infrastructure.
//
// WHAT IT WILL NOT DO is disarm. There is no --mode flag and compatibility is not a target: the
// fence exists because an un-upgraded writer can author a destination nothing governed, and handing
// an operator a lever to reopen that would be shipping the hole with a switch on it. Rolling back to
// a binary that does not carry the gate is a deliberate act with a documented cost, not a flag
// (docs/UPGRADE-AND-ROLLBACK.md).

func newEventingFenceCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "fence",
		Short: "Inspect, arm and verify the cross-version egress writer fence",
		Long: "The writer fence refuses a mutation that introduces or moves an event\n" +
			"destination unless the writer proves, in the same transaction, that it consults\n" +
			"the egress destination control. A deployment whose fleet predates the fence is\n" +
			"classified DORMANT so installing the binary is not a breaking change; a fresh\n" +
			"install is armed by classification, because nothing that predates the fence ever\n" +
			"wrote there.\n\n" +
			"There is no disarm: see `arm --help`.",
		// Every GROUP is runnable here: subcommand_contract.go gives one a RunE so a typo
		// exits non-zero instead of printing help and succeeding (36 of 37 groups used to
		// exit 0 on a mistyped subcommand). That makes the help contract apply to this
		// command too, and it landed without an Example — caught only at integration,
		// because the lane that added the file runs fast lints and this contract is a test.
		Example: "  # What the fence's disposition is, and what the database actually enforces\n" +
			"  olivares eventing fence status\n\n" +
			"  # Arm it after every authoring node carries the gate (one-way; there is no disarm)\n" +
			"  olivares eventing fence arm --reason \"OPS-1234: fleet upgraded\" --assert-writers-upgraded\n\n" +
			"  # Prove the database still refuses, after a restore or a replica promotion\n" +
			"  olivares eventing fence verify",
	}
	cmd.AddCommand(newEventingFenceStatusCmd())
	cmd.AddCommand(newEventingFenceArmCmd())
	cmd.AddCommand(newEventingFenceVerifyCmd())
	return cmd
}

// Enforcement states, as reported. They are three rather than two on purpose: "this ceremony could
// not establish whether the database enforces" must never be delivered as "it does not", and it must
// never be delivered as "it does" either.
const (
	fenceEnforcementLive    = "live"
	fenceEnforcementDormant = "not-verifiable-while-dormant"
	fenceEnforcementMissing = "MISSING"
	fenceEnforcementUnknown = "unknown"
)

// fenceStatus is the machine-readable posture.
type fenceStatus struct {
	Control string `json:"control"`
	// Mode is what is in force; ClassifiedMode is what the engine decided when it first met this
	// deployment and never changes. Both are reported so a reader can tell an inherited disposition
	// from a chosen one.
	Mode                 string `json:"mode"`
	ClassifiedMode       string `json:"classified_mode"`
	EnforcementCommitted bool   `json:"enforcement_committed"`
	Generation           int64  `json:"generation"`
	ClassifiedAt         string `json:"classified_at"`
	Witness              string `json:"witness"`
	DecidedAt            string `json:"decided_at,omitempty"`
	DecidedBy            string `json:"decided_by,omitempty"`
	DecidedReason        string `json:"decided_reason,omitempty"`
	// Armed is the fence's own disposition. RequiredCapability is what a writer must declare under
	// it; BinaryCapability is what THIS binary declares. Reporting both is what makes a refusal
	// diagnosable: a mismatch says the running node is older than the fence demands.
	Armed              bool  `json:"armed"`
	RequiredCapability int64 `json:"required_capability"`
	BinaryCapability   int64 `json:"binary_capability"`
	// The destination control's posture, alongside — the two are read together in every real
	// decision, and an operator who has to run two commands to see them will eventually see one.
	DestinationMode      string `json:"destination_control_mode,omitempty"`
	DestinationCommitted bool   `json:"destination_control_committed"`
	// Enforcement is what the DATABASE does, established by attempting EVERY governed mutation,
	// each in a transaction that is always rolled back — not by trusting that a migration ran.
	Enforcement     string `json:"enforcement"`
	EnforcementNote string `json:"enforcement_note,omitempty"`
	// Unenforced names the governed mutations the database ACCEPTED without a proof. It is the
	// difference between "the fence is broken" and "these two paths of six are open", which is what
	// an operator needs to repair it.
	Unenforced []string `json:"unenforced,omitempty"`
}

// fenceVerifyResult is what `fence verify` reports. Flat, and every key is a key
// `fence status -o json` already uses for the same fact, so a script that reads
// the posture reads the verdict with the same field names.
//
// `verified` exists because the two SUCCESSFUL desenlaces of this command are
// indistinguishable by exit code — ENFORCING and DORMANT both exit 0 — and the
// file's own rule is that "this ceremony could not establish whether the database
// enforces" must never be delivered as either answer. In text an operator reads
// which one they got; a script had only the 0. So DORMANT reports
// `verified: false` alongside `enforcement: not-verifiable-while-dormant`, which
// is the honest pair: nothing was refused, nothing was proved, and the zero means
// "there was nothing here to verify" rather than "the fence holds".
type fenceVerifyResult struct {
	Verified           bool   `json:"verified"`
	Enforcement        string `json:"enforcement"`
	Mode               string `json:"mode"`
	Generation         int64  `json:"generation"`
	RequiredCapability int64  `json:"required_capability"`
}

// fenceVerifyResultFrom projects the posture the command already read. Deriving it
// from the same fenceStatus the text pane prints is what keeps the two panes from
// disagreeing: there is no second read and no second classification.
func fenceVerifyResultFrom(st fenceStatus, verified bool) fenceVerifyResult {
	return fenceVerifyResult{
		Verified:           verified,
		Enforcement:        st.Enforcement,
		Mode:               st.Mode,
		Generation:         st.Generation,
		RequiredCapability: st.RequiredCapability,
	}
}

// Actions `fence arm` reports. They name what the ceremony DID, which is the one
// fact the three text desenlaces differ by and the one a script cannot recover
// from the exit code — all three exit 0.
const (
	fenceArmActionArmed   = "armed"
	fenceArmActionRearmed = "re-armed"
	fenceArmActionAlready = "no-op"
)

// fenceArmResult is what `fence arm` reports.
//
// `verified` is separate from `armed` because the failure this command exists to
// avoid is exactly the two coming apart: a decision recorded that the database
// does not implement. When they diverge the ceremony errors, so a document that
// reaches stdout always carries both true — but a script asserting on `verified`
// is asserting on the measurement rather than on the record, which is the
// distinction the surrounding comments spend their length on.
type fenceArmResult struct {
	Action     string `json:"action"`
	Armed      bool   `json:"armed"`
	Verified   bool   `json:"verified"`
	Generation int64  `json:"generation"`
	DecidedBy  string `json:"decided_by"`
}

// The probe MATRIX lives in the module (eventing.FenceProbes), not here. Building it next to the
// ceremony would have meant a third copy of the governed columns — after the entity descriptor and
// the migration SQL — and a copy of a destination rule has been the wrong one twice in this
// campaign. This file's job is the part that is genuinely the ceremony's: one transaction per probe,
// always rolled back, and the reporting.

// fenceRepairAdvice is what an operator does when the probes find an accepted mutation.
//
// It says "delete the tracking rows, then restart" rather than "restart", and the difference is the
// whole point: the migration runner SKIPS every version already recorded as applied
// (core/migrate/migrate.go), so a restart alone re-creates nothing. An earlier draft of this
// ceremony told operators to restart and would have left them with a one-way decision already
// committed and a recipe that does nothing — found by an adversarial review of this
// implementation, not by a test.
//
// The SQLite DROP is not optional either: its migrations use CREATE TRIGGER IF NOT EXISTS, which
// leaves an existing-but-wrong trigger in place. PostgreSQL's re-create is DROP-then-CREATE inside a
// DO block and CREATE OR REPLACE for the functions, so it converges on its own.
const fenceRepairAdvice = `Those enforcement objects are absent, disabled, or were lost in a restore.

To repair, as the OWNER role (a restart alone does NOT re-create them: the migration runner skips
every version already recorded as applied):

  1. SQLite only — drop the stale triggers so they can be re-created:
       DROP TRIGGER IF EXISTS eventing_subscription_writer_fence_ins;
       DROP TRIGGER IF EXISTS eventing_subscription_writer_fence_upd;
       DROP TRIGGER IF EXISTS eventing_subscription_sink_writer_fence_ins;
       DROP TRIGGER IF EXISTS eventing_subscription_sink_writer_fence_upd;
       DROP TRIGGER IF EXISTS eventing_subscription_sink_writer_fence_del;
  2. Forget the fence migrations so the runner applies them again:
       DELETE FROM schema_migrations_mod_eventing WHERE version >= 3;
  3. Restart a node against this database.
  4. Re-run the arming, which moves the generation and so invalidates every proof outstanding from
     the gap — a proof committed while a trigger was missing stays live and would authorize one
     write by a binary without the gate:
       olivares eventing fence arm --reason "<ticket>: post-repair" --assert-writers-upgraded
  5. Confirm: olivares eventing fence verify`

// probeFenceEnforcement asks the DATABASE whether the fence actually refuses.
//
// It attempts exactly the mutation the fence exists to stop — a subscription with no capability
// attestation — inside a transaction it always rolls back, and reads the answer from whether the
// engine refused it BY NAME.
//
// This is deliberately a behavioral probe rather than a catalog query, and the difference is the
// whole point of running it. Listing triggers in pg_trigger or sqlite_master proves that an object
// exists; it does not prove that it is attached to the right table, that it is ENABLED, that its
// function survived a restore, or that it rejects. A restored dump, a promoted logical replica or a
// hand-repaired database can each satisfy the catalog and enforce nothing. What the fence promises
// is a refusal, so a refusal is what gets measured.
//
// It writes into the SYSTEM tenant, so no tenant's data is touched even in the transaction that is
// discarded.
func probeFenceEnforcement(ctx context.Context, st store.Store) (accepted []string, err error) {
	gen, err := fenceGenerationForProbe(ctx, st)
	if err != nil {
		return nil, err
	}
	for _, p := range eventing.FenceProbes() {
		// Each probe gets its OWN transaction. A refusal aborts the statement, and the store unwinds
		// the whole transaction with it, so sharing one would stop at the first governed mutation
		// and report nothing about the rest — which is the defect this matrix exists to fix.
		//
		// The transaction is rolled back either way: on a refusal by the error the fence raised, and
		// on an acceptance by the sentinel the probe returns instead of nil.
		merr := st.Mutate(ctx, model.SystemTenantID, func(sc store.Scope) error {
			return p.Attempt(ctx, sc, gen)
		})
		switch {
		case errors.Is(merr, eventing.ErrFenceProbeAccepted):
			// The mutation was ACCEPTED. On an armed deployment that means this trigger is absent,
			// disabled, or attached to the wrong table.
			accepted = append(accepted, p.Name)
		case merr == nil:
			// Unreachable: every Attempt returns non-nil. Counted as accepted rather than as
			// success, because the safe reading of an impossible answer is the one that does not
			// claim a fence.
			accepted = append(accepted, p.Name)
		case errors.Is(merr, eventing.ErrFenceProbeSeedFailed):
			// The SEED failed, so the governed mutation under test never ran. This must never be
			// read as a refusal: the seeds write governed rows carrying real proofs, so a seed that
			// fails does so with the fence's own message, and classifying by message alone reported
			// ENFORCING for a trigger the probe never reached.
			return nil, fmt.Errorf("writer fence probe %q could not be SEEDED, so that mutation was not tested: %w", p.Name, merr)
		case isWriterFenceRefusal(merr):
			// Refused, by the fence, for its own reason.
		default:
			return nil, fmt.Errorf("writer fence probe %q: %w", p.Name, merr)
		}
	}
	return accepted, nil
}

// fenceGenerationForProbe reads the generation the probes must attest against, BEFORE any of their
// transactions opens — the same rule every governed writer follows, and for the same reason: this
// read takes a pooled connection and SQLite has one.
func fenceGenerationForProbe(ctx context.Context, st store.Store) (int64, error) {
	rs, ok := st.(store.RolloutStater)
	if !ok {
		return 0, fmt.Errorf("this store does not expose durable rollout state")
	}
	state, err := rs.RolloutState(ctx, eventing.EgressWriterFenceControlKey)
	if err != nil {
		return 0, err
	}
	return state.Generation, nil
}

// isWriterFenceRefusal reports that an error is THIS fence refusing, rather than any error that
// happens to carry the words.
//
// It matches the engine-specific signal first — PostgreSQL raises SQLSTATE OL441, SQLite raises
// SQLITE_CONSTRAINT_TRIGGER (1811) — and only then the message. Matching on the words alone was a
// finding of the implementation contrast: any constraint, wrapper or unrelated function whose text
// contained them would have counted as enforcement.
func isWriterFenceRefusal(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	if !strings.Contains(msg, "eventing egress writer fence") {
		return false
	}
	// The message is this fence's own. Require an engine constraint signal with it, so a plain string
	// carried by something else cannot pass.
	//
	// HONEST ABOUT THE THIRD TOKEN. `OL441` and `1811` are engine CODES — PostgreSQL's SQLSTATE and
	// SQLite's extended result code — and either alone is decisive. `constraint failed` is not a
	// code: it is the prose modernc.org/sqlite puts in front of a RAISE(ABORT), and it would match
	// any constraint on any table. It is kept because on that driver it is the only signal that
	// travels with the message, and it is safe HERE only because the fence's own sentence is already
	// required above it — this function is an AND, not an OR of three equals. A future reader
	// tempted to reuse this shape elsewhere should take the codes and leave the prose.
	return strings.Contains(msg, "OL441") || strings.Contains(msg, "1811") ||
		strings.Contains(msg, "constraint failed")
}

// readFenceStatus assembles the posture, including what the database actually does.
func readFenceStatus(ctx context.Context, st store.Store) (fenceStatus, error) {
	rs, ok := st.(store.RolloutStater)
	if !ok {
		return fenceStatus{}, fmt.Errorf("this store does not expose durable rollout state, so the egress writer fence cannot be established")
	}
	state, err := rs.RolloutState(ctx, eventing.EgressWriterFenceControlKey)
	if err != nil {
		return fenceStatus{}, fmt.Errorf("read rollout state for %q: %w", eventing.EgressWriterFenceControlKey, err)
	}
	out := fenceStatus{
		Control:              state.Key,
		Mode:                 string(state.CurrentMode),
		ClassifiedMode:       string(state.ClassifiedMode),
		EnforcementCommitted: state.EnforcementCommitted,
		Generation:           state.Generation,
		Witness:              state.WitnessKind + ":" + state.WitnessDetail,
		DecidedBy:            state.DecidedBy,
		DecidedReason:        state.DecidedReason,
		Armed:                state.CurrentMode == store.RolloutEnforced,
		BinaryCapability:     eventing.EgressWriterCapability,
	}
	if !state.ClassifiedAt.IsZero() {
		out.ClassifiedAt = state.ClassifiedAt.UTC().Format("2006-01-02T15:04:05Z07:00")
	}
	if !state.DecidedAt.IsZero() {
		out.DecidedAt = state.DecidedAt.UTC().Format("2006-01-02T15:04:05Z07:00")
	}
	if out.Armed {
		out.RequiredCapability = eventing.EgressWriterCapability
	}
	// The destination control, read alongside. A failure is REPORTED rather than swallowed: this
	// used to discard the error and leave the field empty, which made "absent", "corrupt",
	// "permission denied" and "the plane is down" all read as the same blank — in a surface whose
	// output is recorded as the evidence a decision was approved against.
	if dest, derr := rs.RolloutState(ctx, eventing.EgressRolloutControlKey); derr == nil {
		out.DestinationMode = string(dest.CurrentMode)
		out.DestinationCommitted = dest.EnforcementCommitted
	} else {
		out.DestinationMode = "unreadable: " + derr.Error()
	}

	if !out.Armed {
		out.Enforcement = fenceEnforcementDormant
		out.EnforcementNote = "the fence is dormant, so an unproved write is accepted BY DESIGN and the probe cannot tell a working fence from an absent one"
		return out, nil
	}
	acceptedBy, perr := probeFenceEnforcement(ctx, st)
	switch {
	case perr != nil:
		out.Enforcement = fenceEnforcementUnknown
		out.EnforcementNote = perr.Error()
	case len(acceptedBy) == 0:
		out.Enforcement = fenceEnforcementLive
	default:
		out.Enforcement = fenceEnforcementMissing
		out.Unenforced = acceptedBy
		out.EnforcementNote = "the fence is ARMED but the database ACCEPTED these mutations with no capability attestation: " +
			strings.Join(acceptedBy, "; ") + " — those enforcement objects are absent, disabled, or were lost in a restore"
	}
	return out, nil
}

func printFenceStatus(out io.Writer, st fenceStatus) {
	fmt.Fprintf(out, "control              %s\n", st.Control)
	fmt.Fprintf(out, "mode                 %s (classified %s, generation %d)\n", st.Mode, st.ClassifiedMode, st.Generation)
	fmt.Fprintf(out, "armed                %t\n", st.Armed)
	fmt.Fprintf(out, "capability           required %d, this binary declares %d\n", st.RequiredCapability, st.BinaryCapability)
	fmt.Fprintf(out, "enforcement          %s\n", st.Enforcement)
	if st.EnforcementNote != "" {
		fmt.Fprintf(out, "                     %s\n", st.EnforcementNote)
	}
	if st.DestinationMode != "" {
		fmt.Fprintf(out, "destination control  %s%s\n", st.DestinationMode,
			map[bool]string{true: ", committed", false: ""}[st.DestinationCommitted])
	}
	if st.DecidedBy != "" {
		fmt.Fprintf(out, "decided              %s by %s — %s\n", st.DecidedAt, st.DecidedBy, st.DecidedReason)
	}
	fmt.Fprintf(out, "witness              %s\n", st.Witness)
	// The closing sentence is CONDITIONAL on what was measured, and that is a correction: it used to
	// print "a future violation FAILS VISIBLY" unconditionally, one line below an enforcement status
	// that could say MISSING. A report that contradicts itself is worse than one that says less.
	switch st.Enforcement {
	case fenceEnforcementLive:
		fmt.Fprintln(out, "\nArmed and enforcing: a future violation FAILS VISIBLY. That is not a statement")
		fmt.Fprintln(out, "about the fleet's composition and not a statement about writes already in the")
		fmt.Fprintln(out, "database.")
	case fenceEnforcementMissing:
		fmt.Fprintln(out, "\nARMED BUT NOT ENFORCING. The mutations listed above are accepted with no proof,")
		fmt.Fprintln(out, "so a violation on those paths does NOT fail. Repair before relying on this.")
	case fenceEnforcementDormant:
		fmt.Fprintln(out, "\nDormant: nothing is demanded of a writer, and whether the database COULD enforce")
		fmt.Fprintln(out, "cannot be established from here — an unproved write is accepted by design.")
	default:
		fmt.Fprintln(out, "\nEnforcement UNKNOWN. This is not 'not enforcing' and not 'enforcing': it is a")
		fmt.Fprintln(out, "measurement that did not complete, and it must not be read as either.")
	}
}

func newEventingFenceStatusCmd() *cobra.Command {
	var dataDir, engine, dsn, adminDSN, ownerDSN string
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Report the writer fence's posture and whether the database enforces it",
		Long: "Reads the deployment's durable disposition for the egress writer fence and, when\n" +
			"it is armed, establishes whether the database actually refuses an unproved write\n" +
			"by attempting one inside a transaction that is always rolled back.\n\n" +
			"That probe needs a WRITABLE connection, so against a read-only replica this\n" +
			"reports the disposition and `enforcement: unknown` with the reason — never a\n" +
			"guess in either direction.",
		Example: "  # Posture, and what the database really does\n" +
			"  olivares eventing fence status\n\n" +
			"  # On Postgres in a split-role deployment\n" +
			"  olivares eventing fence status --engine postgres --dsn \"env:DATABASE_URL\" \\\n" +
			"    --owner-dsn \"env:DATABASE_OWNER_URL\" --json",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			eng, err := egressBoot(cmd, dataDir, engine, dsn, adminDSN, ownerDSN)
			if err != nil {
				return err
			}
			defer func() { _ = eng.Close() }()
			st, err := readFenceStatus(cmd.Context(), eng.store)
			if err != nil {
				return err
			}
			// ONE RENDERER, NOT TWO (integration fix, 2026-08-06). This owned a second
			// JSON encoder, so the global `-o/--output` never reached it: `-o json`
			// printed the human table while `--json` printed JSON, and the two flags
			// meant different things on the same command. Neither branch was red on its
			// own — this file arrived with #457 and the coverage guard
			// (render_coverage_test.go) with another lane, and only the merge produces
			// the failure. That is what integration is for.
			//
			// `--json` keeps working: it is this command's older spelling of `-o json`,
			// so it SELECTS the format instead of owning an encoder. Under a bare
			// constructor (no root, hence no `output` flag) selectedOutput already reads
			// a local `--json`, so the Lookup guard is not a fallback — it is the case
			// where setting it would error.
			if asJSON && cmd.Flags().Lookup("output") != nil {
				if err := cmd.Flags().Set("output", "json"); err != nil {
					return fmt.Errorf("selecting json output: %w", err)
				}
			}
			return renderOut(cmd, func(out io.Writer) error {
				printFenceStatus(out, st)
				return nil
			}, st)
		},
	}
	addStoreFlags(cmd, &dataDir, &engine, &dsn)
	cmd.Flags().StringVar(&adminDSN, "admin-dsn", "", "Postgres: the dedicated BYPASSRLS role")
	cmd.Flags().StringVar(&ownerDSN, "owner-dsn", "", "Postgres: the owner role, required in a split-role deployment (the app role has no schema CREATE)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit JSON")
	return cmd
}

func newEventingFenceVerifyCmd() *cobra.Command {
	var dataDir, engine, dsn, adminDSN, ownerDSN string
	cmd := &cobra.Command{
		Use:   "verify",
		Short: "Fail unless the database is actually enforcing an armed writer fence",
		Long: "Attempts the exact mutation the fence exists to stop — a subscription carrying no\n" +
			"capability attestation — inside a transaction that is always rolled back, and\n" +
			"exits non-zero unless the engine refused it.\n\n" +
			"This is the check to run after a RESTORE, a point-in-time recovery, or the\n" +
			"promotion of a replica. A dump reloaded without its triggers, or a logical\n" +
			"replication subscriber promoted to writer, carries every row and enforces\n" +
			"nothing — and the rollout state it also carries would report ARMED\n" +
			"(docs/DR-RUNBOOK.md).\n\n" +
			"On a DORMANT deployment it reports that it cannot verify, and exits zero: an\n" +
			"unproved write is accepted there by design, so the probe cannot tell a working\n" +
			"fence from an absent one.",
		Example: "  # After restoring a backup or promoting a replica\n" +
			"  olivares eventing fence verify --engine postgres --dsn \"env:DATABASE_URL\" \\\n" +
			"    --owner-dsn \"env:DATABASE_OWNER_URL\"",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			eng, err := egressBoot(cmd, dataDir, engine, dsn, adminDSN, ownerDSN)
			if err != nil {
				return err
			}
			defer func() { _ = eng.Close() }()
			st, err := readFenceStatus(cmd.Context(), eng.store)
			if err != nil {
				return err
			}
			switch st.Enforcement {
			case fenceEnforcementLive:
				return renderOut(cmd, func(out io.Writer) error {
					_, werr := fmt.Fprintf(out, "the egress writer fence is ENFORCING (generation %d, required capability %d)\n",
						st.Generation, st.RequiredCapability)
					return werr
				}, fenceVerifyResultFrom(st, true))
			case fenceEnforcementDormant:
				return renderOut(cmd, func(out io.Writer) error {
					_, werr := fmt.Fprintf(out, "the egress writer fence is DORMANT (mode %s, generation %d); nothing to verify\n", st.Mode, st.Generation)
					return werr
				}, fenceVerifyResultFrom(st, false))
			default:
				// THE THIRD DESENLACE STAYS AN ERROR, with no JSON pane, and that is the
				// point of the exit-code contract rather than a gap in this one. The two
				// above are the two ways this command SUCCEEDS; this is the way it fails,
				// so it travels as stderr plus a non-zero code exactly as before. Handing
				// a failure back as a well-formed document on stdout is how a `set -e`
				// script comes to treat a broken fence as an answer.
				return fmt.Errorf("the egress writer fence is %s: %s.\n\n%s", st.Enforcement, st.EnforcementNote, fenceRepairAdvice)
			}
		},
	}
	addStoreFlags(cmd, &dataDir, &engine, &dsn)
	cmd.Flags().StringVar(&adminDSN, "admin-dsn", "", "Postgres: the dedicated BYPASSRLS role")
	cmd.Flags().StringVar(&ownerDSN, "owner-dsn", "", "Postgres: the owner role, required in a split-role deployment (the app role has no schema CREATE)")
	return cmd
}

func newEventingFenceArmCmd() *cobra.Command {
	var dataDir, engine, dsn, adminDSN, ownerDSN, reason, actor string
	var assertWritersUpgraded bool
	cmd := &cobra.Command{
		Use:   "arm",
		Short: "Require every writer to prove it carries the egress gate",
		Long: "Moves the fence's durable disposition to ENFORCED. From then on, a mutation that\n" +
			"introduces or moves an event destination is refused unless the writer proves, in\n" +
			"the same transaction, that it consults the egress destination control.\n\n" +
			"There is no --mode flag and no disarm. Compatibility is what a deployment is\n" +
			"CLASSIFIED into, never a target: the fence exists because an un-upgraded writer\n" +
			"can author a destination nothing governed, and a lever that reopens that would be\n" +
			"shipping the hole with a switch on it. Rolling back to a binary without the gate\n" +
			"is a deliberate act with a documented cost (docs/UPGRADE-AND-ROLLBACK.md).\n\n" +
			"Arming is a database-side change alone: every writer that carries the gate\n" +
			"already proves it while the fence is dormant, so nothing about their behavior\n" +
			"changes here. What changes is that a writer that does NOT carry it stops\n" +
			"succeeding silently.\n\n" +
			"ORDER MATTERS when the destination control is also pending: arm the fence FIRST.\n" +
			"If the sequence is interrupted between the two, the state you are left in is more\n" +
			"restrictive rather than a silent authorization.",
		Example: "  olivares eventing fence arm \\\n" +
			"    --reason \"CHG-4822: eventing fleet converged on 1.9.0\" --assert-writers-upgraded",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if strings.TrimSpace(reason) == "" {
				return fmt.Errorf("--reason is required: a rollout decision with no recorded reason is the ownerless state this control exists to eliminate")
			}
			if !assertWritersUpgraded {
				return fmt.Errorf("--assert-writers-upgraded is required: arming makes an authoring node that does not carry the egress gate FAIL, loudly and by name. That is the point, and it is a change an operator should be making on purpose rather than discovering")
			}
			eng, err := egressBoot(cmd, dataDir, engine, dsn, adminDSN, ownerDSN)
			if err != nil {
				return err
			}
			defer func() { _ = eng.Close() }()

			st, err := readFenceStatus(cmd.Context(), eng.store)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			// Being ALREADY armed is not the same as having DECIDED it — a fresh install is
			// classified enforced with the commitment clear, and that deployment must still be able
			// to record who decided and why. The same correction unit G's actuation needed.
			//
			// AND being already committed is not the same as being ENFORCED. This shortcut used to
			// return success on the sole basis of the durable record, which meant a failed arming —
			// committed, because the transition commits before the verification runs — became a
			// SUCCESS on the operator's second attempt while the database enforced nothing. Now the
			// shortcut requires the measurement too, and anything else falls through to re-arm.
			if st.Armed && st.EnforcementCommitted && st.Enforcement == fenceEnforcementLive {
				return renderOut(cmd, func(w io.Writer) error {
					_, werr := fmt.Fprintf(w, "the egress writer fence is already armed, committed and enforcing (generation %d, decided by %s); nothing to do\n", st.Generation, st.DecidedBy)
					return werr
				}, fenceArmResult{
					Action: fenceArmActionAlready, Armed: true, Verified: true,
					Generation: st.Generation, DecidedBy: st.DecidedBy,
				})
			}
			// Which desenlace this run is, decided BEFORE the transition, because after
			// it the distinguishing fact (was the fence already committed?) is gone.
			action := fenceArmActionArmed
			if st.Armed && st.EnforcementCommitted {
				action = fenceArmActionRearmed
			}
			// The re-arming note is TEXT-ONLY, and it stays where it is instead of being
			// deferred into the renderer below. It is a warning about the operation that
			// is ABOUT TO RUN, so an operator whose SetRolloutMode then fails must still
			// have seen it — folding it into the final render would delete it from
			// exactly the run that needs it most. Under -o json the same fact travels as
			// action="re-armed", so nothing is lost and no prose lands on stdout in front
			// of the document a script is parsing.
			format, ferr := selectedOutput(cmd)
			if ferr != nil {
				return ferr
			}
			if action == fenceArmActionRearmed && format == "text" {
				// Re-arming an already-committed fence is the REPAIR path, and the generation bump
				// it performs is load-bearing rather than bookkeeping: a proof that a writer
				// committed while a trigger was missing stays live and row-bound, and would
				// authorize exactly one write by a binary without the gate once the trigger came
				// back. Moving the generation invalidates every outstanding proof at a stroke,
				// because the fence compares the generation the writer OBSERVED.
				fmt.Fprintf(out, "the fence is armed and committed but reports %s; re-arming to move the generation, which invalidates every proof outstanding from the gap\n", st.Enforcement)
			}
			rs, ok := eng.store.(store.RolloutStater)
			if !ok {
				return fmt.Errorf("this store does not expose durable rollout state")
			}
			who := strings.TrimSpace(actor)
			if who == "" {
				who = cliEgressActor()
			}
			next, aerr := rs.SetRolloutMode(cmd.Context(), store.RolloutTransition{
				Key:    eventing.EgressWriterFenceControlKey,
				Mode:   store.RolloutEnforced,
				Actor:  who,
				Reason: reason + " [operator assertion: every authoring node carries the egress gate]",
				// The evidence is what the operator was looking at: the posture BEFORE the
				// decision, including whether the database was enforcing.
				Evidence: fmt.Sprintf("mode=%s classified=%s generation=%d enforcement=%s binary_capability=%d destination_control=%s",
					st.Mode, st.ClassifiedMode, st.Generation, st.Enforcement, st.BinaryCapability, st.DestinationMode),
				ExpectGeneration: st.Generation,
			})
			if aerr != nil {
				return aerr
			}
			// TEXT-ONLY, and printed HERE for the same reason as the re-arming note: the
			// decision is ALREADY COMMITTED at this line, and the verification below can
			// still fail. On that path today's stdout carries this sentence and stderr
			// carries the refusal, and an operator needs exactly that pair — the record
			// moved, the database did not follow. Deferring it into the renderer would
			// erase it from the one run where losing it is dangerous.
			if format == "text" {
				fmt.Fprintf(out, "egress writer fence is now ARMED (generation %d, decided by %s)\n", next.Generation, next.DecidedBy)
			}

			// VERIFY, rather than announce. An arming that records a decision the database does not
			// implement is the worst of both worlds: the status surface would report ARMED and an
			// un-upgraded writer would keep succeeding.
			acceptedBy, perr := probeFenceEnforcement(cmd.Context(), eng.store)
			if perr != nil {
				return fmt.Errorf("the decision is recorded, but this ceremony could NOT establish whether the database enforces it: %w.\n\nRun `olivares eventing fence verify` before relying on it", perr)
			}
			if len(acceptedBy) > 0 {
				return fmt.Errorf("the decision is recorded, but the database ACCEPTED these governed mutations with no capability attestation:\n  - %s\n\n%s", strings.Join(acceptedBy, "\n  - "), fenceRepairAdvice)
			}
			return renderOut(cmd, func(w io.Writer) error {
				if _, werr := fmt.Fprintln(w, "verified: the database refuses EVERY governed mutation that carries no capability attestation."); werr != nil {
					return werr
				}
				if _, werr := fmt.Fprintln(w, "\nThis does NOT prove the fleet's composition and does NOT prove the past. It"); werr != nil {
					return werr
				}
				if _, werr := fmt.Fprintln(w, "makes a future violation fail visibly instead of succeeding silently."); werr != nil {
					return werr
				}
				_, werr := fmt.Fprintln(w, "\n→ running nodes converge within seconds; no restart is needed.")
				return werr
			}, fenceArmResult{
				Action: action, Armed: true, Verified: true,
				Generation: next.Generation, DecidedBy: next.DecidedBy,
			})
		},
	}
	addStoreFlags(cmd, &dataDir, &engine, &dsn)
	cmd.Flags().StringVar(&adminDSN, "admin-dsn", "", "Postgres: the dedicated BYPASSRLS role")
	cmd.Flags().StringVar(&ownerDSN, "owner-dsn", "", "Postgres: the owner role, required in a split-role deployment (the app role has no schema CREATE)")
	cmd.Flags().StringVar(&reason, "reason", "", "why (a change ticket reference belongs here) — required")
	cmd.Flags().StringVar(&actor, "actor", "", "who is deciding (default: $OLIVARES_ACTOR or the OS user)")
	cmd.Flags().BoolVar(&assertWritersUpgraded, "assert-writers-upgraded", false, "acknowledge that arming makes an un-upgraded authoring node fail — required")
	return cmd
}
