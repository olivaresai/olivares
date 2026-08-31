// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/eventing"
)

// The operator's side of the unit-G rollout: read the disposition, see what
// enforcing would block, and decide.
//
// It is a CLI ceremony rather than a console button, and that is the deliberate
// choice. Actuating is a platform-operator act on a deployment-wide control: it is not
// scoped to a tenant, a tenant admin must never be able to reach it, and the operator
// running it is the person with shell access to the host that holds the policy file.
// The console shows the state and the diff (GET /egress-policy, GET
// /egress-policy/compat); it does not offer the lever, because a lever reachable over
// HTTP would have to be defended against every path that reaches HTTP.
//
// What the ceremony refuses to do is guess. It will not report coverage it could not
// establish, it will not accept a transition with no recorded reason, and it will not
// let a deployment go back to compatibility mode once it has left — because
// compatibility honors an unbounded set of destinations collected from the
// deployment's own history, and restoring it would reopen all of them at once.

func newEventingEgressCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "egress",
		Short: "Inspect and actuate the egress destination control's rollout",
		Long: "The egress destination control governs where a tenant may point its own event\n" +
			"stream. A deployment that predates the control starts in COMPATIBILITY mode so\n" +
			"installing the binary is not a breaking change; a fresh install starts ENFORCED.\n" +
			"These commands report which one this deployment is in, what enforcing would\n" +
			"block, and apply the transition.",
		Example: "  olivares eventing egress status\n" +
			"  olivares eventing egress actuate --mode enforced",
	}
	cmd.AddCommand(newEventingEgressStatusCmd())
	cmd.AddCommand(newEventingEgressActuateCmd())
	return cmd
}

// egressRolloutStatus is the machine-readable status.
type egressRolloutStatus struct {
	Control string `json:"control"`
	// Mode is what is in force; ClassifiedMode is what the engine decided when it first
	// met this deployment and never changes. Reporting both is what lets a reader tell
	// an inherited disposition from a chosen one.
	Mode                 string                      `json:"mode"`
	ClassifiedMode       string                      `json:"classified_mode"`
	EnforcementCommitted bool                        `json:"enforcement_committed"`
	Generation           int64                       `json:"generation"`
	ClassifiedAt         string                      `json:"classified_at"`
	Witness              string                      `json:"witness"`
	DecidedAt            string                      `json:"decided_at,omitempty"`
	DecidedBy            string                      `json:"decided_by,omitempty"`
	DecidedReason        string                      `json:"decided_reason,omitempty"`
	PolicyFile           string                      `json:"policy_file,omitempty"`
	Tenants              []egressRolloutTenantStatus `json:"tenants,omitempty"`
	// CoverageComplete reports that every tenant in this deployment was inspected. It
	// is false when tenants could not be enumerated, and then the tenant list below
	// describes a SUBSET — which is why actuation refuses to treat it as a diff.
	CoverageComplete bool `json:"coverage_complete"`
	// CoverageNote says why coverage is incomplete, when it is.
	CoverageNote string `json:"coverage_note,omitempty"`
}

type egressRolloutTenantStatus struct {
	Tenant string `json:"tenant"`
	// Seeded reports that the compatibility line was drawn for this tenant; Intact that the
	// rows still reproduce the seed's own count and digest. Seeded WITHOUT Intact is a partial
	// restore, and it is the more dangerous of the two because the report looks complete.
	Seeded     bool   `json:"seeded"`
	Intact     bool   `json:"intact"`
	SeedDigest string `json:"seed_digest,omitempty"`
	Integrity  string `json:"integrity,omitempty"`
	// Recorded counts every destination the line captured, including those the policy in force
	// already covers; StillNeeded counts only the ones that stop working when it is enforced.
	Recorded    int      `json:"recorded"`
	StillNeeded int      `json:"still_needed"`
	Unparsable  int      `json:"unparsable"`
	Blocked     []string `json:"blocked,omitempty"`
}

// egressBoot opens the engine for these commands, passing the dedicated BYPASSRLS
// admin and owner roles when the operator supplied them.
//
// It exists because auditBoot cannot: its bootConfig carries only the data dir, engine and
// application DSN. Both extra roles are needed, for different reasons, and an earlier revision
// of this command supplied only the first:
//
//   - ADMIN, because on Postgres the application role is deliberately NOBYPASSRLS, so without it
//     the cross-tenant enumeration this ceremony's coverage proof depends on cannot be exhaustive.
//   - OWNER, because opening the store executes CREATE TABLE IF NOT EXISTS for the rollout tables,
//     and PostgreSQL requires schema CREATE for that statement even when the relation already
//     exists. The documented split application role has USAGE and DML and deliberately not CREATE
//     (deploy/postgres/01-app-role.sql), so in exactly the topology this repository recommends the
//     command failed with "permission denied for schema public" before it read anything. Telling an
//     operator to pass the owner credential as --dsn would have been a workaround that abandons the
//     split, not a fix.
func egressBoot(cmd *cobra.Command, dataDir, engineName, dsn, adminDSN, ownerDSN string) (*engine, error) {
	return boot(cmd.Context(), bootConfig{
		DataDir: dataDir, Engine: engineName, DSN: dsn, AdminDSN: adminDSN, OwnerDSN: ownerDSN,
		Version: version, Logger: slog.Default(), NoImplicitInstall: true,
	})
}

// egressBootRO is egressBoot for `status`, which only REPORTS the rollout
// disposition. It never creates the data directory, mints a signing key or
// creates the store file — a status command that manufactured an
// installation in the operator's working directory was reporting on something it
// had just built. The owner-credential note above is unaffected: it concerns the
// CREATE TABLE IF NOT EXISTS an EXISTING store still executes on open.
func egressBootRO(cmd *cobra.Command, dataDir, engineName, dsn, adminDSN, ownerDSN string) (*engine, error) {
	return boot(cmd.Context(), bootConfig{
		DataDir: dataDir, Engine: engineName, DSN: dsn, AdminDSN: adminDSN, OwnerDSN: ownerDSN,
		Version: version, Logger: slog.Default(), ReadOnly: true,
	})
}

func newEventingEgressStatusCmd() *cobra.Command {
	var dataDir, engine, dsn, adminDSN, ownerDSN string
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Report the rollout disposition and what enforcing would block",
		Long: "Reads the deployment's durable disposition for the egress destination control\n" +
			"and, per tenant, which destinations currently work only because this deployment\n" +
			"predates the control. Those are exactly what stops working when it is enforced.",
		Example: "  # What is in force, and what enforcing would block\n" +
			"  olivares eventing egress status\n\n" +
			"  # On Postgres, the admin role is what makes the tenant coverage provable\n" +
			"  olivares eventing egress status --engine postgres --dsn \"env:DATABASE_URL\" \\\n" +
			"    --admin-dsn \"env:DATABASE_ADMIN_URL\" --json",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			eng, err := egressBootRO(cmd, dataDir, engine, dsn, adminDSN, ownerDSN)
			if err != nil {
				return err
			}
			defer func() { _ = eng.Close() }()
			st, err := readEgressRolloutStatus(cmd.Context(), eng.store)
			if err != nil {
				return err
			}
			// E2: through renderOut, so the GLOBAL -o/--output works here.
			// It did not: only this command's own --json switched the shape, so
			// `olivares eventing egress status -o json` printed the text report.
			return renderOut(cmd, func(io.Writer) error {
				printEgressRolloutStatus(cmd, st)
				return nil
			}, st)
		},
	}
	addStoreFlags(cmd, &dataDir, &engine, &dsn)
	cmd.Flags().StringVar(&adminDSN, "admin-dsn", "", "Postgres: the dedicated BYPASSRLS role, required to enumerate every tenant")
	cmd.Flags().StringVar(&ownerDSN, "owner-dsn", "", "Postgres: the owner role, required in a split-role deployment (the app role has no schema CREATE)")
	addDeprecatedJSONFlag(cmd)
	return cmd
}

// readEgressRolloutStatus assembles the disposition and the per-tenant diff.
//
// Enumerating tenants is where honesty costs something. On Postgres the application
// and owner roles are deliberately NOBYPASSRLS and the module tables carry FORCE row
// level security, so a cross-tenant read is not merely discouraged — without the
// dedicated admin role it cannot be assumed to return the whole set. An empty or short
// answer is therefore reported as INCOMPLETE COVERAGE rather than as "no tenants rely
// on compatibility", because those two readings differ by exactly the destinations an
// operator is about to switch off.
func readEgressRolloutStatus(ctx context.Context, st store.Store) (egressRolloutStatus, error) {
	rs, ok := st.(store.RolloutStater)
	if !ok {
		return egressRolloutStatus{}, fmt.Errorf("this store does not expose durable rollout state")
	}
	state, err := rs.RolloutState(ctx, eventing.EgressRolloutControlKey)
	if err != nil {
		return egressRolloutStatus{}, fmt.Errorf("read rollout state for %q: %w", eventing.EgressRolloutControlKey, err)
	}
	out := egressRolloutStatus{
		Control:              state.Key,
		Mode:                 string(state.CurrentMode),
		ClassifiedMode:       string(state.ClassifiedMode),
		EnforcementCommitted: state.EnforcementCommitted,
		Generation:           state.Generation,
		Witness:              state.WitnessKind + " " + state.WitnessDetail,
		DecidedBy:            state.DecidedBy,
		DecidedReason:        state.DecidedReason,
		PolicyFile:           strings.TrimSpace(os.Getenv(envEventingEgressPolicy)),
	}
	if !state.ClassifiedAt.IsZero() {
		out.ClassifiedAt = state.ClassifiedAt.UTC().Format("2006-01-02T15:04:05Z07:00")
	}
	if !state.DecidedAt.IsZero() {
		out.DecidedAt = state.DecidedAt.UTC().Format("2006-01-02T15:04:05Z07:00")
	}

	var orgs []model.Org
	if serr := st.System(ctx, func(sys store.SystemScope) error {
		var lerr error
		orgs, lerr = sys.ListOrgs(ctx)
		return lerr
	}); serr != nil {
		out.CoverageNote = "tenants could not be enumerated: " + serr.Error()
		return out, nil
	}
	if len(orgs) == 0 {
		// Zero is ambiguous under Postgres row-level security and must not be read as "there
		// are none" — but on SQLite there is no such scoping, so zero IS exhaustive and
		// refusing it would make the ceremony unusable on the single-box deployment that is
		// most likely to run it.
		if st.Engine() != store.EngineSQLite {
			out.CoverageNote = "no tenants were returned; on Postgres a cross-tenant read needs the dedicated BYPASSRLS admin role (--admin-dsn), so this may be a scoping result rather than an empty deployment"
			return out, nil
		}
		out.CoverageComplete = true
		return out, nil
	}
	pol, perr := loadEventingEgressPolicy(os.Getenv)
	if perr != nil {
		return egressRolloutStatus{}, perr
	}
	reporter := eventing.NewCompatReporter(st, pol)
	for _, org := range orgs {
		// The system tenant is skipped for the same reason the delivery pump skips it:
		// platform events are tenant-scoped facts and no subscription lives there.
		if org.TenantID.IsZero() || org.TenantID.IsSystem() {
			continue
		}
		rep, rerr := reporter.Report(ctx, org.TenantID)
		if rerr != nil {
			out.CoverageNote = fmt.Sprintf("tenant %s could not be read: %v", org.TenantID, rerr)
			return out, nil
		}
		ts := egressRolloutTenantStatus{
			Tenant:      org.TenantID.String(),
			Seeded:      rep.Seeded,
			Intact:      rep.Intact,
			SeedDigest:  rep.SeedDigest,
			Recorded:    len(rep.Authorities),
			StillNeeded: rep.StillNeeded,
			Unparsable:  rep.Unparsed,
			Integrity:   rep.IntegrityNote,
		}
		for _, a := range rep.Authorities {
			if !a.Covered {
				ts.Blocked = append(ts.Blocked, fmt.Sprintf("%s (%d subscription(s))", a.Authority, a.Subscriptions))
			}
		}
		out.Tenants = append(out.Tenants, ts)
	}
	// Coverage means "every tenant in this deployment was enumerated AND its
	// compatibility record is complete" — not merely that the enumeration finished. An
	// unseeded tenant contributes an EMPTY diff, so treating enumeration alone as
	// coverage would let a decision be approved against a report that says nothing,
	// which is exactly the absence-is-not-proof defect the per-tenant seed row exists
	// to prevent.
	// A compatibility record is only REQUIRED where compatibility is what is in force. On a
	// deployment classified `enforced` there is nothing to grandfather and nothing to seed —
	// demanding the marker there made the ceremony unreachable on a fresh install, which is
	// the majority case and the one the whole unit exists for.
	if store.RolloutMode(out.Mode) == store.RolloutLegacyCompat {
		var unproven []string
		for _, t := range out.Tenants {
			switch {
			case !t.Seeded:
				unproven = append(unproven, t.Tenant+" (no record yet)")
			case !t.Intact:
				unproven = append(unproven, t.Tenant+" (record does not match its own seed)")
			}
		}
		if len(unproven) > 0 {
			out.CoverageNote = fmt.Sprintf("%d tenant(s) cannot prove their compatibility record: %s. An empty diff there means nothing was recorded, not that nothing would break",
				len(unproven), strings.Join(unproven, "; "))
			return out, nil
		}
	}
	out.CoverageComplete = true
	return out, nil
}

func printEgressRolloutStatus(cmd *cobra.Command, st egressRolloutStatus) {
	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "control:      %s\n", st.Control)
	// Three states, not two: committed, decided-but-not-committed (which policy_optional
	// necessarily is), and never decided. Collapsing the middle one into "not yet decided" told
	// an operator who HAD decided that they had not.
	disposition := " (as classified; not yet decided)"
	switch {
	case st.EnforcementCommitted:
		disposition = " (committed to enforcement — one-way)"
	case st.DecidedAt != "":
		disposition = " (a recorded decision, not the classification)"
	}
	fmt.Fprintf(out, "mode:         %s%s\n", st.Mode, disposition)
	fmt.Fprintf(out, "classified:   %s\n", st.ClassifiedMode)
	fmt.Fprintf(out, "generation:   %d\n", st.Generation)
	fmt.Fprintf(out, "classified:   %s  [%s]\n", st.ClassifiedAt, st.Witness)
	if st.DecidedAt != "" {
		fmt.Fprintf(out, "decided:      %s by %s — %s\n", st.DecidedAt, st.DecidedBy, st.DecidedReason)
	}
	if st.PolicyFile == "" {
		fmt.Fprintf(out, "policy file:  (none configured — %s is unset)\n", envEventingEgressPolicy)
	} else {
		fmt.Fprintf(out, "policy file:  %s\n", st.PolicyFile)
	}
	if !st.CoverageComplete {
		fmt.Fprintf(out, "\nCOVERAGE INCOMPLETE: %s\n", st.CoverageNote)
	}
	blocked := 0
	for _, t := range st.Tenants {
		blocked += t.StillNeeded
	}
	fmt.Fprintf(out, "\ntenants inspected: %d   destinations that enforcing would block: %d\n", len(st.Tenants), blocked)
	for _, t := range st.Tenants {
		if t.StillNeeded == 0 && t.Unparsable == 0 && t.Seeded && t.Intact {
			continue
		}
		fmt.Fprintf(out, "\n  %s\n", t.Tenant)
		if !t.Seeded {
			fmt.Fprintf(out, "    compatibility record: NOT YET WRITTEN (nothing has needed it here)\n")
		} else if !t.Intact {
			fmt.Fprintf(out, "    compatibility record: DOES NOT MATCH ITS OWN SEED — %s\n", t.Integrity)
		}
		if t.Unparsable > 0 {
			fmt.Fprintf(out, "    %d endpoint(s) cannot be canonicalized and can never be grandfathered\n", t.Unparsable)
		}
		for _, b := range t.Blocked {
			fmt.Fprintf(out, "    BLOCKED: %s\n", b)
		}
	}
}

// cliEgressActor names the operator on the durable record when they did not say. The
// OS user is not an identity the control plane authenticated, and the recorded string
// says so rather than dressing it up as one.
func cliEgressActor() string {
	if v := strings.TrimSpace(os.Getenv("OLIVARES_ACTOR")); v != "" {
		return v
	}
	if u := strings.TrimSpace(os.Getenv("USER")); u != "" {
		return "cli:" + u
	}
	return "cli:unidentified-operator"
}

// Actions `egress actuate` reports. Both of its successful desenlaces exit 0, so
// this is the only field that tells a script whether the run MOVED the control or
// found it already there — the same reason `sources plan` carries an `action`.
const (
	egressActuateActionActuated = "actuated"
	egressActuateActionNoOp     = "no-op"
)

// egressActuateResult is what `egress actuate` reports.
//
// `mode`, `generation` and `decided_by` are egressRolloutStatus's own keys for
// the same facts.
//
// The commitment is spelled out rather than left implicit in the mode because the
// distinction is this control's whole subject: enforcement COMMITTED is one-way,
// and "enforced but not yet committed" is the state a fresh install sits in — the
// text pane spends three lines on that difference (printEgressRolloutStatus's
// three-state disposition) and a boolean is the machine form of it.
//
// AND IT IS SPELLED `enforcement_committed`, NOT `committed`. This lot wrote
// `committed` first, which reads better and was the wrong call: the same fact is
// already on the wire as `enforcement_committed` in three places — this file's own
// egressRolloutStatus (the `egress status` leaf), fenceStatus (`fence status`) and
// the module's HTTP surface (modules/eventing/egressapi.go). A second spelling
// would have meant a script reading the posture and the receipt of the change to
// that posture needs two names for one boolean, which is exactly the per-command
// parser this unit exists to delete. A prettier key is not worth a fork in the
// vocabulary, and the JSON pane is new here, so nothing depended on the old name.
//
// `decided_by` is present in BOTH desenlaces, including the no-op where the text
// does not print it, so the document's shape does not change with the outcome. A
// key that appears only sometimes forces every consumer to branch before it can
// read, which is the cost this whole unit exists to remove.
type egressActuateResult struct {
	Action               string `json:"action"`
	Mode                 string `json:"mode"`
	Generation           int64  `json:"generation"`
	EnforcementCommitted bool   `json:"enforcement_committed"`
	DecidedBy            string `json:"decided_by"`
}

func newEventingEgressActuateCmd() *cobra.Command {
	var dataDir, engine, dsn, adminDSN, ownerDSN, mode, reason, actor string
	var acceptBlocked, assertWritersUpgraded, acceptUnfenced bool
	cmd := &cobra.Command{
		Use:   "actuate",
		Short: "Apply a deliberate rollout decision for the egress destination control",
		Long: "Moves the deployment's durable disposition.\n\n" +
			"  --mode enforced         the authored policy becomes authoritative and every\n" +
			"                          grandfathered destination is retired. This is the\n" +
			"                          decision that COMMITS, and it is one-way.\n" +
			"  --mode policy_optional  records that this deployment does not require the\n" +
			"                          control to be configured: an ABSENT policy permits. An\n" +
			"                          AUTHORED policy is still authoritative — this is not a\n" +
			"                          bypass — and it is unreachable once enforcement has\n" +
			"                          been committed.\n\n" +
			"Compatibility mode is never a target: a deployment is classified into it and\n" +
			"leaves it once, because it honors every destination the deployment had before\n" +
			"the control existed and re-entering it would grant all of them back. An\n" +
			"emergency is answered with a new, scoped policy entry.",
		Example: "  olivares eventing egress actuate --mode enforced \\\n" +
			"    --reason \"CHG-4821: SIEM egress allow-list approved\" --assert-writers-upgraded",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			m := store.RolloutMode(strings.TrimSpace(mode))
			if !m.Valid() {
				return fmt.Errorf("--mode must be %q or %q (%q cannot be re-entered)",
					store.RolloutEnforced, store.RolloutPolicyOptional, store.RolloutLegacyCompat)
			}
			if m == store.RolloutLegacyCompat {
				return fmt.Errorf("--mode %q is not a destination for this command: compatibility mode is what a deployment is CLASSIFIED as, and it honors every destination the deployment already had — entering it deliberately would grant all of them at once", store.RolloutLegacyCompat)
			}
			if strings.TrimSpace(reason) == "" {
				return fmt.Errorf("--reason is required: a rollout decision with no recorded reason is the ownerless state this control exists to eliminate")
			}
			if !assertWritersUpgraded {
				// No node-capability registry exists in this tree, so nothing can PROVE that
				// every node that writes a subscription runs the authoring gate. Rather than
				// pretend, the operator asserts it and the assertion is recorded — an honest
				// gap named in the audit trail beats an automated check that does not exist.
				return fmt.Errorf("--assert-writers-upgraded is required: nothing in this deployment can prove that every node able to author a subscription runs a binary carrying this control, so the operator has to assert it and the assertion is recorded with the decision")
			}
			eng, err := egressBoot(cmd, dataDir, engine, dsn, adminDSN, ownerDSN)
			if err != nil {
				return err
			}
			defer func() { _ = eng.Close() }()

			st, err := readEgressRolloutStatus(cmd.Context(), eng.store)
			if err != nil {
				return err
			}
			// Being ALREADY in the target mode is not the same as having DECIDED it. A fresh
			// install is classified `enforced` with the commitment clear, and returning early
			// here meant it could never commit — so the one deployment shape this whole unit
			// exists for could not make the decision the unit is about. The transition is
			// meaningful whenever it would move the commitment.
			if store.RolloutMode(st.Mode) == m && (m != store.RolloutEnforced || st.EnforcementCommitted) {
				return renderOut(cmd, func(w io.Writer) error {
					_, werr := fmt.Fprintf(w, "already in mode %s (generation %d)%s; nothing to do\n", st.Mode, st.Generation,
						map[bool]string{true: ", committed", false: ""}[st.EnforcementCommitted])
					return werr
				}, egressActuateResult{
					Action: egressActuateActionNoOp, Mode: st.Mode, Generation: st.Generation,
					EnforcementCommitted: st.EnforcementCommitted, DecidedBy: st.DecidedBy,
				})
			}
			// PREFLIGHT. Every refusal below is a fact the operator would otherwise
			// discover from a dead letter.
			// NO override. Coverage is a safety precondition, and a precondition with a
			// documented bypass is a warning: recording that an operator accepted an unknown
			// does not turn the unknown into a fact, and the row this writes is durable.
			if !st.CoverageComplete {
				return fmt.Errorf("refusing: %s.\n\nThe diff you would be approving does not cover this deployment, so it cannot tell you what stops working. On Postgres, pass --admin-dsn with the dedicated BYPASSRLS role so every tenant can be enumerated; a tenant with no compatibility record yet gets one at its first delivery or authoring attempt on this binary", st.CoverageNote)
			}
			blocked := 0
			for _, t := range st.Tenants {
				blocked += t.StillNeeded
			}
			if m == store.RolloutEnforced && blocked > 0 && !acceptBlocked {
				printEgressRolloutStatus(cmd, st)
				return fmt.Errorf("refusing: enforcing would stop %d destination(s) from delivering (listed above). Add them to %s, or pass --accept-blocked to proceed knowing they break", blocked, envEventingEgressPolicy)
			}
			if m == store.RolloutEnforced && st.PolicyFile == "" && !acceptBlocked {
				return fmt.Errorf("refusing: no destination policy is configured (%s is unset), so enforcing would deny EVERY destination on this deployment. Author a policy first, or pass --accept-blocked if a deny-all is what you mean", envEventingEgressPolicy)
			}
			rs, ok := eng.store.(store.RolloutStater)
			if !ok {
				return fmt.Errorf("this store does not expose durable rollout state")
			}
			// Unit H — THE ORDER. --assert-writers-upgraded is the operator signing for
			// something nothing here can check. The writer fence is what makes that signature
			// enforceable, so enforcing destinations while the fence is dormant means signing the
			// assertion and leaving nothing to hold anyone to it.
			//
			// Arming FIRST is the safe sequence rather than a preference: if the two steps are
			// interrupted between, "fence armed, destinations still in compatibility" is more
			// restrictive on writers and authorizes nothing silently, whereas the reverse leaves
			// exactly the gap this unit exists to close.
			//
			// An UNREADABLE fence is treated as not armed for the purposes of this refusal, and
			// said so — the failure this campaign keeps finding is delivering "could not establish"
			// as "not required".
			// The fence's posture is read as what the DATABASE does, not as what the durable record
			// says. Equating `current_mode = enforced` with an effective fence was a finding of the
			// implementation contrast, and a sharp one: a fence armed but MISSING — a partial
			// restore, a dropped trigger, an arming whose verification failed — would have let this
			// command persist "a future violation fails visibly" about a deployment where it does
			// not. The full probe matrix is what makes the difference measurable.
			fencePosture := "unreadable"
			fenceArmed := false
			if m == store.RolloutEnforced {
				fst, ferr := readFenceStatus(cmd.Context(), eng.store)
				switch {
				case ferr != nil:
					fencePosture = "unreadable: " + ferr.Error()
				default:
					fenceArmed = fst.Armed && fst.Enforcement == fenceEnforcementLive
					fencePosture = fmt.Sprintf("%s at generation %d, enforcement %s", fst.Mode, fst.Generation, fst.Enforcement)
					if len(fst.Unenforced) > 0 {
						fencePosture += " (accepted with no proof: " + strings.Join(fst.Unenforced, "; ") + ")"
					}
				}
				if !fenceArmed && !acceptUnfenced {
					return fmt.Errorf("refusing: the egress writer fence is %s, so --assert-writers-upgraded would be recorded with nothing enforcing it.\n\nArm it first, and confirm the database implements it:\n  olivares eventing fence arm --reason %q --assert-writers-upgraded\n  olivares eventing fence verify\n\nThat order is the safe one: interrupted between the two steps, an armed fence with destinations still in compatibility is more restrictive, never a silent authorization. If this fleet cannot be converged yet, pass --accept-unfenced and the gap is recorded with the decision", fencePosture, reason)
				}
			}
			who := strings.TrimSpace(actor)
			if who == "" {
				who = cliEgressActor()
			}
			// The recorded reason carries the assertions too, because an assertion that is
			// not in the durable record is an assertion nobody made.
			recorded := reason
			if acceptBlocked && blocked > 0 {
				recorded += fmt.Sprintf(" [operator accepted %d blocked destination(s)]", blocked)
			}
			// Recorded as an ASSERTION, in those words, and now qualified by what actually holds
			// anyone to it. Nothing in this deployment can prove that every node able to author a
			// subscription carries this gate — there is no writer-capability registry — so the
			// record must not read as though something checked it. What the writer fence adds is
			// narrower and worth recording exactly: with it armed, a FUTURE violation fails
			// visibly. It still says nothing about the fleet's composition or about writes already
			// in the database.
			switch {
			case m != store.RolloutEnforced:
				recorded += " [UNVERIFIED operator assertion: every authoring node is upgraded]"
			case fenceArmed:
				recorded += fmt.Sprintf(" [operator assertion: every authoring node is upgraded; egress writer fence %s and MEASURED enforcing on every governed mutation, so a future violation fails visibly — the past is not proved]", fencePosture)
			default:
				recorded += fmt.Sprintf(" [UNVERIFIED operator assertion: every authoring node is upgraded; egress writer fence %s and the operator accepted proceeding unfenced, so NOTHING enforces it]", fencePosture)
			}

			next, aerr := rs.SetRolloutMode(cmd.Context(), store.RolloutTransition{
				Key:              eventing.EgressRolloutControlKey,
				Mode:             m,
				Actor:            who,
				Reason:           recorded,
				Evidence:         egressDecisionEvidence(st),
				ExpectGeneration: st.Generation,
			})
			if aerr != nil {
				return aerr
			}
			return renderOut(cmd, func(w io.Writer) error {
				if _, werr := fmt.Fprintf(w, "egress destination control is now %s (generation %d, decided by %s)\n",
					next.CurrentMode, next.Generation, next.DecidedBy); werr != nil {
					return werr
				}
				_, werr := fmt.Fprintln(w, "\n→ running nodes converge within seconds; no restart is needed.")
				return werr
			}, egressActuateResult{
				Action: egressActuateActionActuated, Mode: string(next.CurrentMode), Generation: next.Generation,
				EnforcementCommitted: next.EnforcementCommitted, DecidedBy: next.DecidedBy,
			})
		},
	}
	addStoreFlags(cmd, &dataDir, &engine, &dsn)
	cmd.Flags().StringVar(&adminDSN, "admin-dsn", "", "Postgres: the dedicated BYPASSRLS role, required to enumerate every tenant")
	cmd.Flags().StringVar(&ownerDSN, "owner-dsn", "", "Postgres: the owner role, required in a split-role deployment (the app role has no schema CREATE)")
	cmd.Flags().StringVar(&mode, "mode", "", "enforced | policy_optional")
	cmd.Flags().StringVar(&reason, "reason", "", "why (a change ticket reference belongs here) — required")
	cmd.Flags().StringVar(&actor, "actor", "", "who is deciding (default: $OLIVARES_ACTOR or the OS user)")
	cmd.Flags().BoolVar(&acceptBlocked, "accept-blocked", false, "proceed even though listed destinations stop delivering")
	cmd.Flags().BoolVar(&assertWritersUpgraded, "assert-writers-upgraded", false, "assert that every node able to author a subscription runs a binary carrying this control — required")
	cmd.Flags().BoolVar(&acceptUnfenced, "accept-unfenced", false, "proceed with the egress writer fence dormant, so nothing enforces --assert-writers-upgraded")
	return cmd
}

// egressDecisionEvidence fingerprints WHAT the operator was looking at when they
// decided, for the append-only transition log.
//
// It is a digest rather than the report itself because the report names hosts and the
// transition log is global bookkeeping with no tenant binding. What it proves is
// narrow and worth having: that a later audit can tell whether the state a decision
// was approved against is the state that was actually recorded, rather than having to
// take the reason string's word for it.
func egressDecisionEvidence(st egressRolloutStatus) string {
	h := sha256.New()
	fmt.Fprintf(h, "control=%s\ngeneration=%d\ncoverage=%t\npolicy_file=%s\npolicy_bytes=%s\ntenants=%d\n",
		st.Control, st.Generation, st.CoverageComplete, st.PolicyFile, egressPolicyBytesDigest(st.PolicyFile), len(st.Tenants))
	// Per tenant: the SEED DIGEST and the blocked authorities BY NAME, not just how many.
	// Counts alone were the defect: two reports that swap one blocked collector for another
	// produce the same number, so the digest could not tell an operator's approval of one diff
	// from their approval of a different one.
	for _, t := range st.Tenants {
		fmt.Fprintf(h, "tenant=%s seeded=%t intact=%t seed=%s recorded=%d unparsable=%d\n",
			t.Tenant, t.Seeded, t.Intact, t.SeedDigest, t.Recorded, t.Unparsable)
		blocked := append([]string(nil), t.Blocked...)
		sort.Strings(blocked)
		for _, b := range blocked {
			fmt.Fprintf(h, "  blocked=%s\n", b)
		}
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

// egressPolicyBytesDigest fingerprints the policy FILE CONTENT this host can see.
//
// The path alone was not enough and saying otherwise was overstating: replacing the bytes at the
// same path leaves a path-only digest unchanged, so an audit could not tell which policy the
// operator was actually shown. What this still cannot prove is that a RUNNING NODE loaded the same
// bytes — each node reads the file at its own boot — and that limit is real and stated rather than
// papered over.
func egressPolicyBytesDigest(path string) string {
	if strings.TrimSpace(path) == "" {
		return "none"
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "unreadable:" + err.Error()
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
