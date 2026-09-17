// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"fmt"
	"slices"
	"strings"

	coreengine "github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/store"
)

// THE ROLE GUARD USED TO RUN AFTER THE DESTRUCTIVE STEP, and that is what this
// file moves.
//
// `dr restore` already asked the target one question before writing —
// ProbeTargetOccupied, "is there an estate here" — and then wrote. It never asked
// the other one: "may this connection legitimately do this". The store's boot
// guard asks it, correctly, and refuses; but boot runs AFTER pg_restore, so the
// refusal arrives with the estate already on disk. MEASURED on PostgreSQL 16.15
// against an EMPTY target with --dsn pointed at a superuser: pg_restore exited 0
// and wrote 294 tables owned by `postgres`, the boot then refused, and what was
// left was an estate that is restored, unverified, unusable by the application
// role (0 privileges on all 294 tables) and never reconciled. A control that
// fires after the act it controls is a report, not a control.
//
// The second half is the split-owner posture the product itself provisions
// (`olivares db init --owner-role`). There the application role holds NO CREATE on
// the engine schema by design, so pg_restore and the boot's own DDL preflight
// both die with a bare `permission denied for schema public (SQLSTATE 42501)`
// pointing at a contract probe — a message that names neither the cause nor the
// flag that fixes it.
//
// WHAT THIS FILE MAY AND MAY NOT DO. It may not invent policy: every privilege
// verdict below is checkVerdict's, the same one `olivares db check` prints and
// the same one the boot guard enforces, called rather than restated — being
// stricter than boot is as wrong as being laxer, because a preflight is believed.
// What it adds is only what boot cannot say in time: the CREATE authority the DDL
// path needs, and whether the pools were pointed at the same estate.
//
// It refuses EXACTLY the set boot already refuses, one step earlier, plus the two
// facts above — both of which are today a 42501 or a silently wrong manifest, not
// a working deployment. So no posture that works today stops working here.

// drPoolKind names what a DR connection is FOR, because that — not the flag's
// spelling — is what decides the posture it must have. The admin pool is the one
// deliberate exception in the product: it is SUPPOSED to bypass RLS.
type drPoolKind int

const (
	// drPoolApp is the runtime pool (--dsn): NOSUPERUSER NOBYPASSRLS, or FORCE row
	// level security is inert and boot refuses.
	drPoolApp drPoolKind = iota
	// drPoolOwner is the DDL pool (--owner-dsn): the same RLS-safe posture as the
	// app pool, and additionally CREATE on the engine schema.
	drPoolOwner
	// drPoolAdmin is the cross-tenant reader (--admin-dsn): BYPASSRLS but NOT a
	// superuser, and never a target of writes.
	drPoolAdmin
)

// drPool is one connection a DR run will open, named by the flag that supplied it.
type drPool struct {
	label string
	dsn   string
	kind  drPoolKind
	auth  store.ConnAuthority
	// usable is false once this pool has already been reported as a problem, so
	// the coherence checks below do not pile a second complaint onto a connection
	// that never opened.
	usable bool
}

// drPostgresPools lists the pools a Postgres DR run will actually open, in the
// order an operator reads them. The DDL pool is the owner when one is configured
// and the app pool otherwise — the SAME fallback the store's Open makes, so the
// preflight and the boot cannot disagree about which connection runs DDL.
func drPostgresPools(f drFlags) []drPool {
	pools := []drPool{{label: "--dsn", dsn: f.dsn, kind: drPoolApp}}
	if strings.TrimSpace(f.ownerDSN) != "" {
		pools = append(pools, drPool{label: "--owner-dsn", dsn: f.ownerDSN, kind: drPoolOwner})
	}
	if strings.TrimSpace(f.adminDSN) != "" {
		pools = append(pools, drPool{label: "--admin-dsn", dsn: f.adminDSN, kind: drPoolAdmin})
	}
	return pools
}

// drDDLLabel is the flag naming the pool that will run DDL — pg_restore's target
// and the boot's migration/preflight connection.
func drDDLLabel(f drFlags) string {
	if strings.TrimSpace(f.ownerDSN) != "" {
		return "--owner-dsn"
	}
	return "--dsn"
}

// drDDLDSN is the DSN of that pool. It is the ONE place the owner/app fallback is
// written for the restore path, so `runPgRestore`'s target and the pre-flight's
// subject are THE SAME RESOLVED DSN by construction.
//
// ⛔ THE SAME DSN IS NOT THE SAME CONNECTION, and an earlier version of this
// comment claimed the two "cannot drift apart". They can. The probe closes its
// pool; `runPgRestore` starts a process that dials again; `drBoot` opens pools
// again after that. Everything the pre-flight certified was certified of a
// session that no longer exists.
//
// What this centralisation buys is real and bounded: one place decides WHICH DSN,
// so a static misconfiguration cannot be vetted on one credential and used on
// another. What it does NOT buy is a guarantee about a balancer that answers a
// later dial with a different backend. Gating the write on facts read in the
// session that performs it is a different design — see the routing precondition
// on preflightPostgresDR — and is deliberately not attempted here.
//
// It never falls back to --admin-dsn: that role is the read-only cross-tenant
// reader, and pointing a restore at it is one of the wrong-role cases this
// pre-flight exists to refuse.
func drDDLDSN(f drFlags) string {
	if strings.TrimSpace(f.ownerDSN) != "" {
		return f.ownerDSN
	}
	return f.dsn
}

// preflightPostgresDR proves, before the command writes anything, that the
// connections this run will use are the ones it is allowed to use, and that they
// point at one estate.
//
// op is the operation named in the errors ("restore" / "backup"). Every problem
// found is reported in ONE error: an operator fixing a DR invocation during an
// outage should learn about all three pools in one run, not one flag per attempt.
//
// NOTHING here prints a DSN. The pools are named by their FLAG, the roles by the
// name the server reported, and the targets by database name — a DR error is
// read out of a terminal, pasted into a ticket and shipped to a log, and a DSN
// carries a password.
//
// ⛔ THE PRECONDITION THIS CONTROL RUNS UNDER, stated because a control believed
// beyond its reach is worse than none.
//
// Each fact below is read inside ONE transaction on ONE pinned connection, so
// every verdict is true of a real session. That session is then CLOSED. pg_restore
// dials again in another process; drBoot opens its pools again after that. This
// pre-flight therefore certifies the invocation, not the later writes, and it is
// sound as a gate on them only where every backend reachable by a given DSN is
// UNIFORM in role authority, cluster identity, database and writability — the
// ordinary case of a direct route or a pooler in front of one estate.
//
// Where that does not hold — a balancer that may answer the restore's dial with a
// different backend than the probe's — this catches static misconfiguration and
// nothing more. Closing that gap means reading the facts in the session that
// performs the first write, which is a different design; it is named here as
// future work rather than implied by silence.
//
// Every pool must address the same live server. A physical replica shares its
// primary's cluster identifier but fails the concurrent advisory-lock challenge.
// Reading through a replica requires a separate snapshot/LSN contract and is
// not supported by this preflight.
func preflightPostgresDR(ctx context.Context, f drFlags, op string) error {
	if store.Engine(f.engineKind) != store.EnginePostgres {
		return nil
	}
	if strings.TrimSpace(f.dsn) == "" {
		return fmt.Errorf("--dsn is required for a Postgres %s", op)
	}

	pools := drPostgresPools(f)
	var problems []string
	for i := range pools {
		auth, err := coreengine.ProbeConnAuthority(ctx, store.Config{
			Engine: store.EnginePostgres, DSN: pools[i].dsn,
		})
		if err != nil {
			return err
		}
		pools[i].auth = auth
		// The privilege verdict is checkVerdict's, unchanged: unreachable, a
		// disabled-trigger session, a superuser or a BYPASSRLS app/owner role, an
		// admin role that is a superuser or is not BYPASSRLS. Restating any of that
		// here would be a second copy of the boot guard, free to drift.
		verdict, ok := checkVerdict(auth.Posture, pools[i].kind == drPoolAdmin)
		if !ok {
			problem := pools[i].label + " " + verdict
			// checkVerdict's RLS-unsafe wording offers `--allow-privileged-db-role`
			// as an alternative remedy, and that flag exists on `serve`/`config` but
			// NOT on `dr`. Saying so closes the loop instead of sending an operator
			// hunting for it mid-outage — and the reason it is absent is worth one
			// clause: a disaster restore is not the moment to disable the tenant
			// backstop on the estate being rebuilt.
			if pools[i].kind != drPoolAdmin && auth.Posture.Reachable && auth.Posture.RLSUnsafe() {
				problem += ". `dr` has NO --allow-privileged-db-role: point this flag at the NOSUPERUSER NOBYPASSRLS role instead"
			}
			problems = append(problems, problem)
			continue
		}
		pools[i].usable = true
	}

	ddlLabel := drDDLLabel(f)
	writable := drMustWriteLabels(f, op)
	for i := range pools {
		p := pools[i]
		if !p.usable {
			continue
		}
		// THE DDL POOL ANSWERS FOR THE SCHEMA; THE WRITE POOLS ANSWER FOR WRITING,
		// and the two sets are not the same. Only the DDL connection needs the
		// engine schema and CREATE in it — in the owner/app split the application
		// role is denied that grant BY DESIGN, so demanding it of every pool would
		// refuse the posture the product recommends.
		//
		// Questions are asked in the order an operator can act on and are never
		// collapsed into one "can it do DDL" boolean: the schema may be absent, the
		// ACL may be missing, the session may be read-only, or the server may be a
		// standby. Four remedies, four sentences.
		if p.label == ddlLabel {
			switch {
			case !p.auth.SchemaExists:
				problems = append(problems, fmt.Sprintf(
					"%s reached database %q, which has no %q schema: this is not a database `olivares db init` prepared, and a %s cannot create the engine's relations in a schema that does not exist",
					p.label, p.auth.Database, p.auth.Schema, op))
				continue
			case !p.auth.CanCreate:
				problems = append(problems, drNoCreateProblem(f, p, op))
				continue
			}
		}
		// Writability is the half an ACL check cannot see. `CanCreate` is a GRANT; a
		// session under default_transaction_read_only, or any session on a hot
		// standby, holds that grant and still cannot execute one statement
		// (measured: `can_create_acl=true transaction_read_only=on` then
		// `ERROR: cannot execute CREATE TABLE in a read-only transaction`).
		if !slices.Contains(writable, p.label) {
			continue
		}
		switch {
		case p.auth.InRecovery:
			problems = append(problems, fmt.Sprintf(
				"%s authenticates as %q on a server that is IN RECOVERY (a physical standby), so no session on it can write, whatever the ACL says: %s. Point %s at the PRIMARY",
				p.label, p.auth.Posture.Role, drWriteNeed(p.label, ddlLabel, op), p.label))
		case p.auth.SessionReadOnly:
			problems = append(problems, fmt.Sprintf(
				"%s authenticates as %q in a READ-ONLY session (transaction_read_only=on) on database %q, so it cannot write whatever the ACL says: %s. "+
					"The setting is inherited from default_transaction_read_only at cluster, database or ROLE scope — check `ALTER ROLE %s RESET default_transaction_read_only`, the database default, and postgresql.conf",
				p.label, p.auth.Posture.Role, p.auth.Database, drWriteNeed(p.label, ddlLabel, op), p.auth.Posture.Role))
		}
	}

	if coherence := drTargetCoherence(pools); len(coherence) > 0 {
		problems = append(problems, coherence...)
	}

	// THE LIVE-SERVER CHALLENGE GOES LAST, and only when everything cheaper agrees.
	// It costs a concurrent connection per pool, and every problem above already
	// implies a refusal — running it as well would add a second sentence about a
	// connection the operator has already been told to change.
	if len(problems) == 0 {
		problems = append(problems, drSameLiveServerProblems(ctx, f, pools, op)...)
	}

	if len(problems) == 0 {
		return nil
	}
	return fmt.Errorf("dr %s pre-flight refused BEFORE anything was written (the target database and the data dir are UNCHANGED):\n  - %s",
		op, strings.Join(problems, "\n  - "))
}

// drMustWriteLabels names the pools that must be ABLE TO WRITE for this operation.
//
// The DDL pool always: it creates the schema and runs the migrations.
//
// And on a RESTORE the application pool too, which R2 left out and root corrected.
// The restore does not end at pg_restore: the engine boots and, when the restore
// REPLACES an estate, seals the operator's declaration into the restored ledger —
// a write, on the application connection. A split deployment whose app role
// carries `default_transaction_read_only` therefore restores an estate and then
// fails to record who replaced it, after the target has already been overwritten.
// It is the same defect as F4 one pool over.
//
// A BACKUP is not restricted for it. Backup writes nothing to the target, so
// demanding a writable application pool there would refuse a working
// backup-from-a-hardened-role deployment for a reason that never arises. And the
// admin reader is in neither list: it is read-only by design and exists to read
// across tenants.
func drMustWriteLabels(f drFlags, op string) []string {
	labels := []string{drDDLLabel(f)}
	if op == "restore" && !slices.Contains(labels, "--dsn") {
		labels = append(labels, "--dsn")
	}
	return labels
}

// drWriteNeed says what THIS pool's write is for, so the refusal explains itself
// rather than repeating one sentence for two different jobs.
func drWriteNeed(label, ddlLabel, op string) string {
	if label == ddlLabel {
		return "it is this " + op + "'s DDL connection, which creates the engine's relations"
	}
	return "the application connection seals the restore declaration into the restored ledger after the data lands, so a read-only one replaces the estate and then cannot record who did it"
}

// drNoCreateProblem renders the missing-CREATE refusal, and it is two different
// operator problems that must not share one sentence.
//
// Without --owner-dsn the deployment is very probably the least-privilege split
// (`olivares db init --owner-role`, deploy/postgres/01-app-role.sql), where the
// app role is DENIED schema CREATE on purpose: the fix is to supply the owner
// role, not to grant the app one. Saying "grant CREATE" there would talk an
// operator out of the posture the product recommends, which is the opposite of
// the remedy.
//
// With --owner-dsn already supplied the owner itself is under-provisioned, and
// the fix is that role's grant.
func drNoCreateProblem(f drFlags, p drPool, op string) string {
	if strings.TrimSpace(f.ownerDSN) == "" {
		return fmt.Sprintf(
			"--dsn authenticates as %q, which has no CREATE on schema %q of database %q, and no --owner-dsn was given. "+
				"In the least-privilege owner/app split the application role is denied schema CREATE BY DESIGN, so a %s cannot run as it: "+
				"pass --owner-dsn with the owner role (the one `olivares db init --owner-role` provisions). "+
				"Do NOT grant the application role CREATE — that collapses the split. "+
				"Confirm the posture of both roles with `olivares db check --dsn … --owner-dsn …`",
			p.auth.Posture.Role, p.auth.Schema, p.auth.Database, op)
	}
	return fmt.Sprintf(
		"--owner-dsn authenticates as %q, which has no CREATE on schema %q of database %q: the owner role runs every migration and is the %s's DDL connection. "+
			"Grant it with `GRANT USAGE, CREATE ON SCHEMA %s TO %s` (run in that database), or point --owner-dsn at the role `olivares db init --owner-role` provisioned",
		p.auth.Posture.Role, p.auth.Schema, p.auth.Database, op, p.auth.Schema, p.auth.Posture.Role)
}

// drTargetCoherence checks that the pools were pointed at ONE estate, comparing
// the facts the SERVER answered rather than the DSNs the operator typed. Two DSNs
// differing in host, port or sslmode may reach the same database through a
// service alias or a pooler, and two that look alike may reach different ones;
// only the connection knows.
//
// ⚠ IT DOES NOT DECIDE THE REPLICA CASE, AND MUST NOT SOUND AS IF IT DOES. A
// physical replica carries its primary's system identifier, so this check is silent
// about one — and R2 read that silence as admission and advertised replica support
// the product does not have. drSameLiveServerProblems is what separates them, and
// it refuses a replica. Nothing here should be read as accepting one.
//
// ⛔ IDENTITY IS THE SYSTEM IDENTIFIER, AND THE PREVIOUS VERSION DID NOT HAVE ONE.
//
// It compared pg_postmaster_start_time() and called the field `Cluster`. That is a
// token for a RUNNING SERVER, not an identity for a cluster: it separates two
// servers that started at different instants and nothing else. Worse, the admin
// pool was exempted from it entirely on the reasoning that a physical replica's
// postmaster starts at a different instant than its primary's — so the exemption
// compared only current_database(), and ANY unrelated cluster holding a database
// of the same name passed through the branch labelled "replica". In backup that
// lets the cross-tenant reader enumerate estate B into a manifest built from
// estate A; in restore it means the extra-tenant check certifies the wrong estate.
//
// pg_control_system().system_identifier is PostgreSQL's own cluster identity,
// generated at initdb and carried by every physical copy. So: app, owner and admin
// must all report the SAME SystemIdentifier and the SAME database. That closes the
// hole the exemption opened — a stranger cluster no longer passes on a name.
//
// ⛔ AND IT DOES NOT DECIDE THE REPLICA CASE. A physical replica carries its
// PRIMARY's identifier, so this function is SILENT about one; it neither admits nor
// refuses it. R2 read that silence as admission, exempted the admin pool from the
// weaker same-server check as well, and so advertised replica support the product
// does not have — the engine refuses such a pool at Open, after a restore has
// already written.
//
// Root's R3 decision keeps the engine guard and moves the pre-flight to it:
// drSameLiveServerProblems runs the engine's own concurrent advisory-lock challenge
// on EVERY pool, and a replica is refused there, before any effect. The
// postmaster-start-time predicate that used to sit in this function is retired with
// it — a coincidence away from useless, and it never covered the admin pool anyway.
//
// Reading through a replica is not closed by that refusal, only stopped from being
// claimed: it needs an authoritative snapshot/LSN contract for what the enumerated
// inventory is a snapshot OF and how stale it may be. Separate work.
//
// AN UNREADABLE IDENTITY IS A REFUSAL, NEVER AN INFERRED MATCH. Collapsing "I
// could not look" into "they agree" is precisely the failure this function exists
// to stop, one level up.
func drTargetCoherence(pools []drPool) []string {
	var app, owner, admin *drPool
	for i := range pools {
		if !pools[i].usable {
			continue
		}
		switch pools[i].kind {
		case drPoolApp:
			app = &pools[i]
		case drPoolOwner:
			owner = &pools[i]
		case drPoolAdmin:
			admin = &pools[i]
		}
	}
	if app == nil {
		return nil
	}
	var problems []string

	// Identity must be READABLE on every pool before any of it can be compared.
	for _, p := range []*drPool{app, owner, admin} {
		if p == nil {
			continue
		}
		if p.auth.SystemIdentifier == "" {
			problems = append(problems, drUnreadableIdentityProblem(p))
		}
	}
	if len(problems) > 0 {
		// Comparing what is left would be comparing database NAMES and calling it
		// estate identity — the exact reading being retired here.
		return problems
	}

	for _, other := range []*drPool{owner, admin} {
		if other == nil {
			continue
		}
		switch {
		case other.auth.SystemIdentifier != app.auth.SystemIdentifier:
			problems = append(problems, fmt.Sprintf(
				"%s reached a DIFFERENT PostgreSQL cluster than --dsn: system identifier %s vs %s (both databases are named %q, which is why the name alone cannot decide this). "+
					"Note this check cannot separate a cluster from its own physical REPLICA — a replica carries its primary's identifier — so it is not what admits or refuses one; "+
					"the live-server challenge is, and it refuses a replica too. Every DR pool must be on the same live server",
				other.label, other.auth.SystemIdentifier, app.auth.SystemIdentifier, app.auth.Database))
		case other.auth.Database != app.auth.Database:
			problems = append(problems, fmt.Sprintf(
				"%s reached database %q but --dsn reached %q on the same cluster: %s",
				other.label, other.auth.Database, app.auth.Database, drWrongDatabaseConsequence(other.kind)))
		}
	}

	// ⛔ THE POSTMASTER-START-TIME COMPARISON THAT USED TO LIVE HERE IS GONE, and
	// its job is done properly now. It asked whether two pools were on the same
	// RUNNING SERVER by comparing pg_postmaster_start_time() — a coincidence away
	// from useless, and applied to app+owner only, so an admin reader on a replica
	// slipped past it. drSameLiveServerProblems now asks that question of EVERY
	// pool with the engine's own concurrent advisory-lock challenge, which is a
	// fact about two live sessions rather than a value either of them reports.
	// InstanceStartedAt is retained as a reported fact; it is no longer a predicate,
	// because two predicates for one property are two verdicts that can disagree.
	return problems
}

// drUnreadableIdentityProblem renders the refusal for a pool whose cluster
// identity could not be read, and names the one grant that fixes it WITHOUT
// suggesting the tool should apply it. Provisioning is the operator's act: a DR
// pre-flight that granted itself catalogue privileges to complete a check would be
// a worse control than none.
func drUnreadableIdentityProblem(p *drPool) string {
	return fmt.Sprintf(
		"%s could not read this cluster's identity, so it CANNOT be compared with the others and this run is refused rather than assumed to match (%s). "+
			"EXECUTE on pg_control_system() is granted to PUBLIC by initdb — PostgreSQL 16's own system_functions.sql revokes 56 functions from PUBLIC and this is not one of them — so a hardened catalogue is the likely cause. "+
			"The minimum an operator can restore is `GRANT EXECUTE ON FUNCTION pg_catalog.pg_control_system() TO %s` (metadata only: it returns the cluster identifier and its initdb-time settings, and confers no data or superuser privilege). "+
			"This command will not grant it",
		p.label, p.auth.SystemIdentifierErr, p.auth.Posture.Role)
}

// drWrongDatabaseConsequence says what the wrong database actually costs, per
// pool. The refusal is the same; the sentence that makes an operator believe it is
// not.
func drWrongDatabaseConsequence(kind drPoolKind) string {
	if kind == drPoolAdmin {
		return "the cross-tenant reader would enumerate ANOTHER estate's tenants into this bundle's manifest"
	}
	return "the owner role must run its DDL on the same database the application serves"
}

// drSameLiveServerProblems establishes the prerequisite the ENGINE already
// enforces, before this command writes anything.
//
// ⛔ THIS IS THE CORRECTION ROOT MADE TO R2, AND IT IS WORTH STATING PLAINLY.
//
// R2 compared pg_control_system().system_identifier and, finding that a physical
// replica shares its primary's, ADMITTED an --admin-dsn on a replica as a
// supported topology. Measured against a real pg_basebackup standby, that
// topology does not work: the store refuses to open, because directory activation
// runs a concurrent advisory-lock challenge that proves SAME LIVE SERVER, and a
// replica is a different server. The pre-flight was advertising support the
// product does not have — and doing it in the one place whose job is to be
// believed.
//
// The engine guard is preserved and is not weakened to fit. The pre-flight moves
// to it: same challenge, same factored predicate (sqlstore.askAdvisoryLockWitness),
// asked before custody, extraction and pg_restore instead of after them.
//
// Supporting a replica reader for real needs an authoritative snapshot/LSN
// contract — what the inventory it enumerates is a snapshot OF, and how stale it
// may be. That is separate work. It is not closed, and it is not removed, by
// refusing it here.
//
// SystemIdentifier is kept and still runs first (drTargetCoherence): it is cheap,
// it needs no concurrent connection, and it names a foreign CLUSTER far more
// clearly than "your witness took the lock". This challenge is what catches the
// case identity cannot — a replica, which carries the right identifier and is
// still the wrong server.
func drSameLiveServerProblems(ctx context.Context, f drFlags, pools []drPool, op string) []string {
	ddlLabel := drDDLLabel(f)
	var holder *drPool
	var witnesses []store.SameServerWitness
	for i := range pools {
		p := pools[i]
		if !p.usable {
			// Already reported; it cannot be a witness to anything.
			continue
		}
		if p.label == ddlLabel {
			holder = &pools[i]
			continue
		}
		witnesses = append(witnesses, store.SameServerWitness{Label: p.label, DSN: p.dsn})
	}
	// The holder is the DDL pool: it is the connection whose estate the others must
	// belong to. With no other pool there is nothing to prove — one connection
	// cannot be on a different server from itself — which is also why the
	// single-role posture with no --admin-dsn pays nothing for this.
	if holder == nil || len(witnesses) == 0 {
		return nil
	}

	report, err := coreengine.ProbeSameLiveServer(ctx,
		store.Config{Engine: store.EnginePostgres, DSN: holder.dsn}, holder.label, witnesses)
	if err != nil {
		return []string{fmt.Sprintf("could not run the live-server challenge: %v", err)}
	}
	if report.HolderErr != "" {
		return []string{fmt.Sprintf(
			"the live-server challenge could not be MOUNTED on %s, so the other pools cannot be proved to address the same server and this run is refused rather than assumed coherent (%s)",
			holder.label, report.HolderErr)}
	}

	var problems []string
	for _, w := range report.Witnesses {
		switch {
		case w.Err != "":
			problems = append(problems, fmt.Sprintf(
				"%s could not answer the live-server challenge, so it CANNOT be proved to address the same server as %s and this run is refused rather than assumed coherent (%s)",
				w.Label, holder.label, w.Err))
		case w.AcquiredHoldersLock:
			problems = append(problems, fmt.Sprintf(
				"%s is NOT on the same live server as %s: while %s held a random advisory key, %s was able to take the same key, which only a different server can do. "+
					"A physical replica is the usual cause and is the usual mistake — it reports its primary's cluster identifier and the same database name, so nothing cheaper than this tells them apart. "+
					"The engine refuses this at Open for the same reason (directory activation's identity challenge), so a %s wired this way cannot complete; it is refused here instead, before anything is written. "+
					"Point %s at the SAME SERVER as %s. Reading through a replica is not supported today: it needs an authoritative snapshot/LSN contract that does not exist yet",
				w.Label, holder.label, holder.label, w.Label, op, w.Label, holder.label))
		case w.Database != report.HolderDatabase:
			problems = append(problems, fmt.Sprintf(
				"%s reached database %q on the challenge but %s is on %q",
				w.Label, w.Database, holder.label, report.HolderDatabase))
		}
	}
	return problems
}
