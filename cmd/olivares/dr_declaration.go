// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/olivaresai/olivares/core/api"
	coreengine "github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// The declared-operator rule for a destructive CLI restore (corrected in
//).
//
// The console offers dual-control restore: one admin requests, a DIFFERENT human
// approves. `dr restore` sat entirely outside that gate — no identity anywhere on
// its path — and for a Postgres estate the console REFUSES the restore and sends
// the operator here ("use CLI for postgres", core/api/dr_handler.go). So on the
// default production engine the two-person control was not bypassable: it did not
// exist. Measured: --in-place replaced a live estate that had the gate armed, rc
// 0, and the console work done after the backup was gone.
//
// WHY THE CLI DOES NOT AUTHENTICATE — a DECISION, not an impossibility. This
// header used to say the authenticating closure was impossible. It is not, and
// saying so closed a door that is open: the CLI already boots the restored store
// to verify it (cmd_dr.go), and under --in-place it boots it in STAGING before the
// live state is touched, so restored accounts and tokens do exist at a point where
// the command could still refuse. What that would authenticate against is a
// HISTORICAL identity — the one inside the copy — and three measured facts say it
// is not adequate authority for the act it would authorize:
//
//   - THE STORE THAT WOULD VOUCH IS SUPPLIED BY THE ACTOR. dr.ExtractBundle
//     (core/dr/bundle.go) verifies no signature over the bundle: the manifest is
//     plain JSON, the snapshot digest it is checked against sits in that same
//     manifest, and the public key that later verifies the chain is restored from
//     the bundle under the operator's own KEK. Authenticating there proves
//     possession of the bundle and the KEK — which the command already required.
//   - IT RESURRECTS REVOKED AUTHORITY. Authentication reads Revoked/ExpiresAt out
//     of the restored partition (core/auth/authenticator.go), so a credential
//     revoked AFTER the copy authenticates again. An offboarded admin would come
//     back as a valid principal, and the ledger would call the act authenticated.
//   - IT REFUSES PRESENT AUTHORITY. Whoever was onboarded after the copy does not
//     exist inside it. That is a NEW lockout, in exactly the disaster the command
//     exists for.
//
// So the CLI carries a DECLARATION, and this file is careful never to call it
// anything else: the recorded event says identity=declared, and the flag help and
// the operator note say the name was checked by nobody. The CLI's real trust
// boundary is filesystem + KEK access, and whoever holds both can destroy the
// estate without this command. What the declaration buys is bounded and real: the
// honest operator following the runbook can no longer replace an estate that opted
// into a two-person control WITHOUT KNOWING, the act stops being silent, and it
// still works with the engine down.
//
// WHAT IS NOT CLOSED, said here so no reader takes this file for more than it is:
// there is no second human anywhere on this path, and a declaration is not an
// attribution anyone can check. An estate that needs two humans on EVERY restore
// must control host access as well; see core/api/dr_handler.go (RequireDualControl)
// for the console half and what it does and does not reach.
//
// AND ONE OPTION THAT STAYS OPEN. This header also used to present "make the API
// restore Postgres" as a choice between abandoning distroless and reimplementing
// pg_dump's custom format. That is a false dilemma. The immediate fact is true —
// runPgRestore executes the pg_restore BINARY (cmd_dr.go) and the release image is
// distroless (Dockerfile.release), which has neither shell nor pg_restore — but it
// is a limitation of the current IN-PROCESS implementation, not of the design.
// This repository already resolves exactly that limit for pg_dump WITHOUT leaving
// distroless, with a separate PostgreSQL container over a shared volume
// (deploy/helm/olivares/templates/backup-cronjob.yaml). An equivalent restore
// helper is a third family of options; it is not built here, and it is not
// impossible either.

// drRestoreDeclarationAction is the audit action recorded for a declared,
// single-operator CLI restore over an existing estate.
const drRestoreDeclarationAction = "dr.restore.cli"

// restoreDeclaration is the operator's declared authority for replacing an estate
// from the command line.
type restoreDeclaration struct {
	operator string
	reason   string
}

func addRestoreDeclarationFlags(cmd *cobra.Command, d *restoreDeclaration) {
	cmd.Flags().StringVar(&d.operator, "operator", "", "who is performing this restore (required when the restore REPLACES an existing estate; recorded in the restored ledger). A declaration, not an authentication: the console's dual-control gate does not reach this path")
	cmd.Flags().StringVar(&d.reason, "reason", "", "why this restore is being performed — an incident id or change reference (required when the restore REPLACES an existing estate; recorded in the restored ledger)")
}

func (d restoreDeclaration) trimmed() restoreDeclaration {
	return restoreDeclaration{
		operator: strings.TrimSpace(d.operator),
		reason:   strings.TrimSpace(d.reason),
	}
}

// require refuses a destructive restore that names neither an operator nor a
// reason. Whitespace is not an identity: a blank value would put an empty actor
// on the ledger, which reads as a recorded restore by nobody.
//
// why is the classifier's reason for calling this target an estate. It is in the
// message because the answer is no longer obvious from where the operator stands:
// for Postgres it can be "the target database already holds relations" or "the
// target could not be read", and an operator told only "this REPLACES an estate"
// about a database they believe is empty would reasonably think the command wrong.
func (d restoreDeclaration) require(why string) error {
	t := d.trimmed()
	var missing []string
	if t.operator == "" {
		missing = append(missing, "--operator")
	}
	if t.reason == "" {
		missing = append(missing, "--reason")
	}
	if len(missing) == 0 {
		return nil
	}
	if why == "" {
		why = "the target already holds an estate"
	}
	return fmt.Errorf(
		"this restore REPLACES an existing estate (%s), and the console's dual-control restore gate does not reach the command line: "+
			"supply %s so the act is recorded in the restored ledger. "+
			"If this estate requires two people to restore, get the second one — this flag records a claim, it does not check one. "+
			"Restoring into a CLEAN target destroys nothing and needs neither flag",
		why, strings.Join(missing, " and "))
}

// restoreTarget names what a restore is about to write over.
type restoreTarget struct {
	engineKind string
	dsn        string
	dataDir    string
}

// replacesAnEstate reports whether the target already holds an estate — whether
// this restore DESTROYS something rather than building something new — and the
// reason, which the operator is shown and the ledger records.
//
// The local signals are decided from the filesystem, on purpose: the disaster this
// command serves is the one where the engine will not start, so a check that
// needed a working store would be absent exactly when it is needed. Signing keys
// are the estate's custody and live on disk for BOTH engines (a Postgres estate's
// data dir carries them too, measured), and the store file is the SQLite estate
// itself.
//
// The filesystem is NOT enough for Postgres, and believing it was is what let a
// live Postgres estate be modified with nothing required and nothing sealed
//. A Postgres estate lives at the far end of a DSN, and under external key
// custody (BYOK/CMEK) the data dir is legitimately empty — so every live Postgres
// database classified as a clean target. That was defended with "pg_restore into a
// database that already holds the schema FAILS", which is a claim about pg_restore
// and not one it honors: MEASURED against PostgreSQL 16.14 with the shipped argv,
// it exited 1 with four errors and INSERTED the backup's rows into both
// pre-existing tables. Exiting non-zero makes it loud, not harmless. runPgRestore
// now asks for --single-transaction so that a failed restore really does leave
// nothing behind, and the target itself is asked here rather than guessed at.
//
// AN UNREADABLE TARGET IS NOT AN EMPTY ONE. A probe that cannot connect, or cannot
// read, answers "replaces": collapsing "I could not look" into "it is clean" is
// precisely how the filesystem classifier failed open. The cost of being wrong
// that way is two flags on a restore that destroys nothing; the cost of the other
// way is an unrecorded destruction.
func (t restoreTarget) replacesAnEstate(ctx context.Context) (bool, string) {
	if keys, err := signingKeyFiles(t.dataDir); err == nil && len(keys) > 0 {
		return true, "the data dir holds this estate's signing keys"
	}
	if p := restoreStorePath(t.engineKind, t.dsn, t.dataDir); p != "" {
		if info, err := os.Stat(p); err == nil && !info.IsDir() {
			return true, "the SQLite store file " + p + " already exists"
		}
	}
	if store.Engine(t.engineKind) != store.EnginePostgres || strings.TrimSpace(t.dsn) == "" {
		return false, ""
	}
	occupied, err := coreengine.ProbeTargetOccupied(ctx, store.Config{
		Engine: store.EnginePostgres, DSN: t.dsn,
	})
	switch {
	case err != nil:
		return true, "the target database could NOT be read (" + err.Error() +
			"), and an unreadable target is not an empty one"
	case occupied:
		return true, "the target database already holds relations of its own"
	default:
		return false, ""
	}
}

// restoreStorePath is the SQLite store file a restore would overwrite, or "" when
// the target is not a local file (Postgres restores into a DSN).
func restoreStorePath(engineKind, dsn, dataDir string) string {
	if store.Engine(engineKind) != store.EngineSQLite {
		return ""
	}
	if dsn != "" {
		return dsn
	}
	return filepath.Join(dataDir, "olivares.db")
}

// recordRestoreDeclaration seals the declaration into the RESTORED estate's audit
// ledger, under the system tenant (a restore is estate-wide, not a tenant's act).
//
// It runs against the restored store because that is the only ledger that survives
// the restore: anything written before promotion is overwritten by the very bytes
// being promoted. It runs AFTER the continuity verification, so the event never
// perturbs the tip the verifier compares against the manifest.
func recordRestoreDeclaration(ctx context.Context, st store.Store, d restoreDeclaration, m restoreEvidence) error {
	t := d.trimmed()
	meta := map[string]any{
		// identity=declared is the honest label. The operator string was typed on
		// the command line and checked by nobody; a reader of this ledger must not
		// mistake it for an authenticated principal. There is no second party on
		// this path either, and the event says that too rather than leaving a reader
		// to infer a two-person control from the presence of a record.
		"identity":       "declared",
		"second_party":   "none",
		"reason":         t.reason,
		"engine":         m.engine,
		"bundle":         m.bundle,
		"bundle_taken":   m.takenAt,
		"in_place":       m.inPlace,
		"target_verdict": m.targetVerdict,
		// The gate as the RESTORED copy carries it — which is NOT the same question
		// as "was a two-person control in force when one person acted", and the field
		// used to be named as though it were. What this reads is the policy
		// inside the bundle: a copy taken while the gate was off, restored over an
		// estate that armed it afterwards, would have signed "off" about a control
		// that was on. It is named for the copy because the copy is what the command
		// can read; see sealRestoreDeclaration for the estate it cannot.
		"bundle_dual_control_restore": m.dualControlNote,
	}
	return st.Mutate(ctx, model.SystemTenantID, func(sc store.Scope) error {
		ev, err := sc.Audit().Append(ctx, model.AuditDraft{
			Actor:      "operator:" + t.operator,
			ActorKind:  "operator",
			Action:     drRestoreDeclarationAction,
			TargetKind: "core.estate",
			Meta:       meta,
		})
		if err != nil {
			return err
		}
		if ev.Seq == 0 {
			// An explicit evidence drop (the degrade spool policy) is still a
			// restore nobody can account for. Say so rather than exit 0 in silence.
			return fmt.Errorf("the audit spool DROPPED the record instead of sealing it")
		}
		return nil
	})
}

// restoreEvidence is the non-secret context recorded alongside a declaration.
type restoreEvidence struct {
	engine  string
	bundle  string
	takenAt string
	inPlace bool
	// targetVerdict is why the classifier decided this restore replaces an estate,
	// so a reader can tell a measured verdict ("the target database already holds
	// relations") from a fail-closed one ("the target could not be read").
	targetVerdict   string
	dualControlNote string
}

// declaredRestore carries the declaration into the in-place path, which decides
// whether to promote and therefore has to seal the record itself.
type declaredRestore struct {
	required bool
	decl     restoreDeclaration
	bundle   string
	// verdict is the classifier's reason for calling this target an estate, carried
	// through so the sealed event distinguishes a measured verdict from a
	// fail-closed one on this path too.
	verdict string
}

// sealRestoreDeclaration reads what the RESTORED copy's console gate says, seals
// the declaration into its ledger, and tells the operator both facts.
//
// A failure to record FAILS the command. The bytes are on disk either way, so
// this is not about undoing the restore — it is about refusing to end in the one
// state this whole change exists to remove: a destructive restore that happened,
// outside the two-person gate, and left nothing behind. Silence plus exit 0 is the
// purest form of "clean".
//
// WHAT IT CANNOT READ, named rather than papered over: the dual-control policy of
// the estate being DESTROYED. Every store this function is ever handed is the
// restored one — staging under --in-place, the already-overwritten target
// otherwise — so the only gate it can read is the one inside the copy. Reading the
// live estate's would mean opening it first, and opening it means migrating it:
// under --in-place that would write to a data dir the command promises to leave
// UNTOUCHED when the staged verification fails. So the field is named for the copy
// (bundle_dual_control_restore) instead of being named for a question it cannot
// answer, and the operator note says the same out loud.
func sealRestoreDeclaration(ctx context.Context, cmd *cobra.Command, st store.Store, d restoreDeclaration, m restoreEvidence) error {
	m.dualControlNote = consoleDualControlNote(ctx, st)
	if err := recordRestoreDeclaration(ctx, st, d, m); err != nil {
		return fmt.Errorf("restore completed but could NOT be recorded in the restored ledger (%w): "+
			"a destructive restore outside the console's two-person gate must not be unaccountable — "+
			"fix the ledger and record it before resuming writes", err)
	}
	t := d.trimmed()
	drNote(cmd, fmt.Sprintf(
		"recorded in the restored ledger: %s by DECLARED operator %q, reason %q "+
			"(dual-control restore AS RECORDED IN THE BUNDLE: %s — not the setting of the estate this replaced, which is gone). "+
			"The operator name was declared on the command line and authenticated by nothing, and no second person took part.",
		drRestoreDeclarationAction, t.operator, t.reason, m.dualControlNote))
	return nil
}

// consoleDualControlNote reports what the RESTORED copy's console gate says. An
// unreadable answer is reported as unknown rather than as "off", because a restore
// that cannot tell is not a restore that may assume the gate was down.
func consoleDualControlNote(ctx context.Context, st store.Store) string {
	armed, err := readEstateDualControlRestore(ctx, st)
	switch {
	case err != nil:
		return "unknown"
	case armed:
		return "armed — this estate requires TWO people to restore from the console"
	default:
		return "off"
	}
}

// readEstateDualControlRestore reads the persisted console restore gate from the
// estate. It reuses the API's own reader so the key, the JSON shape and above all
// the FAIL-CLOSED default for a legacy record live in exactly one place. An estate
// with no schedule configured never armed the gate, and says so.
//
// SHARING THAT READER MEANS SHARING ITS CHANGES, which is the point and is worth
// stating once: since a record whose disarm has ELAPSED but names no requester
// reads ARMED rather than off (a disarm with no provenance is not a disarm,
// core/api/dr_handler.go). So a bundle captured before the provenance existed now
// gets "armed" in its note where it used to get "off". That is the same answer the
// running engine would give about the same bytes, which is the only property this
// note can honestly have — the alternative is a second interpretation of the same
// field, drifting from the first.
func readEstateDualControlRestore(ctx context.Context, st store.Store) (bool, error) {
	armed, found, err := api.ReadDualControlRestorePolicy(ctx, st, time.Now().UTC())
	if err != nil {
		return false, err
	}
	if !found {
		return false, nil
	}
	return armed, nil
}
