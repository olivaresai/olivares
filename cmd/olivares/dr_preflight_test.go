// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"slices"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/store"
)

// These cells pin the pre-flight's POLICY, which is the half a single test
// server cannot reach: telling two CLUSTERS apart needs two of them, and the
// admin pool's deliberate exemption is a decision, not an observation. The
// fact-reading half — that these values come from the connection and not from
// the DSN — is measured live in cmd_dr_postgres_split_test.go.

func authority(role, database, sysID, instance string) store.ConnAuthority {
	return store.ConnAuthority{
		Posture:           store.RolePosture{Engine: store.EnginePostgres, Reachable: true, Role: role},
		Schema:            "public",
		SchemaExists:      true,
		CanCreate:         true,
		Database:          database,
		SystemIdentifier:  sysID,
		InstanceStartedAt: instance,
	}
}

func usablePool(label string, kind drPoolKind, auth store.ConnAuthority) drPool {
	return drPool{label: label, dsn: "unused-by-this-check", kind: kind, auth: auth, usable: true}
}

// TestDRTargetCoherenceComparesTheESTATENotTheDSN is the reason this check reads
// what the server answered. In the owner/app split the app and owner DSNs are
// ALWAYS textually different — different userinfo at minimum, and in practice
// often a different host or sslmode through a pooler — so a check that compared
// DSN strings would refuse every legitimate split deployment. What must be
// compared is where the connections landed, and by the identity PostgreSQL itself
// assigns to a cluster.
func TestDRTargetCoherenceComparesTheESTATENotTheDSN(t *testing.T) {
	const (
		sysA    = "7682585743393632555"
		sysB    = "7100000000000000001"
		primary = "2026-09-07T00:36:24.109067Z"
		standby = "2026-09-07T01:02:03.400500Z"
	)
	for _, tc := range []struct {
		name  string
		pools []drPool
		want  string // "" = accepted
	}{
		{
			name: "app and owner on the same estate by different routes",
			pools: []drPool{
				usablePool("--dsn", drPoolApp, authority("olivares_app", "olivares", sysA, primary)),
				usablePool("--owner-dsn", drPoolOwner, authority("olivares_owner", "olivares", sysA, primary)),
				usablePool("--admin-dsn", drPoolAdmin, authority("olivares_admin", "olivares", sysA, primary)),
			},
			want: "",
		},
		{
			name: "the owner is on ANOTHER database of the same cluster",
			pools: []drPool{
				usablePool("--dsn", drPoolApp, authority("olivares_app", "olivares", sysA, primary)),
				usablePool("--owner-dsn", drPoolOwner, authority("olivares_owner", "other_estate", sysA, primary)),
			},
			want: "the owner role must run its DDL on the same database the application serves",
		},
		{
			// THE CELL THE PREVIOUS VERSION GOT WRONG. A same-named database on an
			// unrelated cluster used to pass through the branch labelled "replica",
			// because that branch compared only the NAME. Cluster identity is what
			// separates them.
			name: "the ADMIN reader is on an unrelated cluster whose database has the same name",
			pools: []drPool{
				usablePool("--dsn", drPoolApp, authority("olivares_app", "olivares", sysA, primary)),
				usablePool("--admin-dsn", drPoolAdmin, authority("olivares_admin", "olivares", sysB, standby)),
			},
			want: "reached a DIFFERENT PostgreSQL cluster than --dsn",
		},
		{
			// ⛔ A PHYSICAL REPLICA PASSES *THIS* CHECK AND IS STILL REFUSED, and the
			// distinction is the whole R3 correction.
			//
			// A replica carries its PRIMARY's system identifier — pg_basebackup copies
			// it — and serves a database of the same name, so cluster identity cannot
			// tell the two apart and correctly says nothing here. R2 read that silence
			// as ADMISSION and advertised replica support the product does not have:
			// the engine's directory activation refuses such a pool at Open, after a
			// restore would already have written.
			//
			// What refuses it now is the live-server challenge, which needs two
			// concurrent real connections and therefore lives in the PostgreSQL leg
			// (TestDRRefusesAnAdminReaderOnAPhysicalReplica). This cell exists to pin
			// that drTargetCoherence is NOT the thing that catches it — so that a
			// future change cannot quietly move the responsibility and leave neither
			// check doing it.
			name: "a PHYSICAL REPLICA is invisible to cluster identity (the challenge catches it, not this)",
			pools: []drPool{
				usablePool("--dsn", drPoolApp, authority("olivares_app", "olivares", sysA, primary)),
				usablePool("--admin-dsn", drPoolAdmin, authority("olivares_admin", "olivares", sysA, standby)),
			},
			want: "",
		},
		{
			// Same reasoning for the owner: identity is equal, so this check says
			// nothing, and the challenge is what refuses it. The postmaster-start-time
			// predicate that used to fire here was retired — it was a coincidence away
			// from useless and it never covered the admin pool at all.
			name: "an owner on a standby of the same cluster is likewise invisible to identity",
			pools: []drPool{
				usablePool("--dsn", drPoolApp, authority("olivares_app", "olivares", sysA, primary)),
				usablePool("--owner-dsn", drPoolOwner, authority("olivares_owner", "olivares", sysA, standby)),
			},
			want: "",
		},
		{
			name: "the admin pool on ANOTHER database of the same cluster is refused",
			pools: []drPool{
				usablePool("--dsn", drPoolApp, authority("olivares_app", "olivares", sysA, primary)),
				usablePool("--admin-dsn", drPoolAdmin, authority("olivares_admin", "other_estate", sysA, primary)),
			},
			want: "would enumerate ANOTHER estate's tenants",
		},
		{
			// AN UNREADABLE IDENTITY IS A REFUSAL. It must never be defaulted to a
			// match, and the refusal must name the grant an operator can restore —
			// without the tool granting anything.
			name: "an identity that could not be read is refused, naming the grant",
			pools: []drPool{
				usablePool("--dsn", drPoolApp, authority("olivares_app", "olivares", sysA, primary)),
				func() drPool {
					a := authority("olivares_admin", "olivares", "", primary)
					a.SystemIdentifierErr = "ERROR: permission denied for function pg_control_system (SQLSTATE 42501)"
					return usablePool("--admin-dsn", drPoolAdmin, a)
				}(),
			},
			want: "GRANT EXECUTE ON FUNCTION pg_catalog.pg_control_system()",
		},
		{
			// An unusable pool has already been reported once. Complaining about its
			// database too would bury the reason it actually failed.
			name: "an unreachable owner is not ALSO reported as incoherent",
			pools: []drPool{
				usablePool("--dsn", drPoolApp, authority("olivares_app", "olivares", sysA, primary)),
				{label: "--owner-dsn", kind: drPoolOwner, usable: false},
			},
			want: "",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := drTargetCoherence(tc.pools)
			if tc.want == "" {
				if len(got) != 0 {
					t.Fatalf("a coherent invocation was refused: %v", got)
				}
				return
			}
			if len(got) == 0 {
				t.Fatalf("an incoherent invocation was accepted; expected a problem naming %q", tc.want)
			}
			if !strings.Contains(strings.Join(got, " | "), tc.want) {
				t.Fatalf("the problem must name %q, got: %v", tc.want, got)
			}
		})
	}
}

// TestDRUnreadableIdentityNeverGrantsAnything pins the half of the refusal that is
// a policy and not a message: the tool tells an operator the minimum grant and
// does not apply it. A pre-flight that gave itself catalogue privileges to finish
// a check would be a worse control than none.
func TestDRUnreadableIdentityNeverGrantsAnything(t *testing.T) {
	a := authority("olivares_owner", "olivares", "", "2026-09-07T00:36:24.109067Z")
	a.SystemIdentifierErr = "ERROR: permission denied for function pg_control_system (SQLSTATE 42501)"
	p := usablePool("--owner-dsn", drPoolOwner, a)
	msg := drUnreadableIdentityProblem(&p)
	for _, want := range []string{
		"CANNOT be compared",
		"GRANT EXECUTE ON FUNCTION pg_catalog.pg_control_system() TO olivares_owner",
		"This command will not grant it",
	} {
		if !strings.Contains(msg, want) {
			t.Fatalf("the refusal must contain %q, got: %s", want, msg)
		}
	}
}

// TestDRMustWriteLabelsCoverTheRestoreDeclarationSeal pins root's R3 correction to
// the write-pool set. R2 required only the DDL pool to be writable, which misses
// that a restore does not end at pg_restore: the engine boots and, when the
// restore REPLACES an estate, seals the operator's declaration into the restored
// ledger — a write on the APPLICATION connection. A split deployment whose app
// role carries default_transaction_read_only would restore the estate and then
// fail to record who replaced it, with the target already overwritten.
//
// A BACKUP is deliberately not restricted for it: backup writes nothing to the
// target, so demanding a writable app pool there would refuse a working
// backup-from-a-hardened-role deployment for a reason that cannot arise. And the
// admin reader is in neither set — it is read-only by design.
func TestDRMustWriteLabelsCoverTheRestoreDeclarationSeal(t *testing.T) {
	split := drFlags{dsn: "dsn://app", ownerDSN: "dsn://owner", adminDSN: "dsn://admin"}
	single := drFlags{dsn: "dsn://app", adminDSN: "dsn://admin"}
	for _, tc := range []struct {
		name string
		f    drFlags
		op   string
		want []string
	}{
		{"split restore: the owner writes the schema AND the app seals the declaration",
			split, "restore", []string{"--owner-dsn", "--dsn"}},
		{"split backup: only the DDL pool, because backup writes nothing to the target",
			split, "backup", []string{"--owner-dsn"}},
		{"single-role restore: one pool is both, and it is named once",
			single, "restore", []string{"--dsn"}},
		{"single-role backup: the same one pool",
			single, "backup", []string{"--dsn"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := drMustWriteLabels(tc.f, tc.op)
			if !slices.Equal(got, tc.want) {
				t.Fatalf("write pools = %v, want %v", got, tc.want)
			}
			if slices.Contains(got, "--admin-dsn") {
				t.Fatal("the read-only cross-tenant reader was required to be writable")
			}
		})
	}
}

// TestDRDDLPoolIsTheOwnerOrTheAppNEVERTheAdmin pins the one selection the whole
// fix turns on. The DDL pool is pg_restore's target and the boot's migration
// connection; pointing it at the read-only cross-tenant reader would be a
// restore aimed at the backup role, which is one of the wrong-role cases this
// work exists to refuse. The fallback is written ONCE, here, so the connection
// the pre-flight judges and the one runPgRestore receives are the same by
// construction.
func TestDRDDLPoolIsTheOwnerOrTheAppNEVERTheAdmin(t *testing.T) {
	const app, owner, admin = "dsn://app", "dsn://owner", "dsn://admin"
	for _, tc := range []struct {
		name             string
		f                drFlags
		wantDSN, wantLbl string
	}{
		{"single-role: the app role is its own DDL connection",
			drFlags{dsn: app, adminDSN: admin}, app, "--dsn"},
		{"split: the owner runs the DDL",
			drFlags{dsn: app, ownerDSN: owner, adminDSN: admin}, owner, "--owner-dsn"},
		{"a whitespace-only owner flag is not an owner",
			drFlags{dsn: app, ownerDSN: "   ", adminDSN: admin}, app, "--dsn"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := drDDLDSN(tc.f); got != tc.wantDSN {
				t.Fatalf("DDL DSN = %q, want %q", got, tc.wantDSN)
			}
			if got := drDDLLabel(tc.f); got != tc.wantLbl {
				t.Fatalf("DDL label = %q, want %q", got, tc.wantLbl)
			}
			if drDDLDSN(tc.f) == admin {
				t.Fatal("the DDL pool resolved to the read-only admin DSN")
			}
		})
	}
}

// TestPreflightIsSkippedForSQLite keeps the control from becoming a Postgres tax
// on the engine that has no roles at all. SQLite reaches no server, so a probe
// there would be a connection attempt against a file path.
func TestPreflightIsSkippedForSQLite(t *testing.T) {
	if err := preflightPostgresDR(t.Context(),
		drFlags{engineKind: "sqlite", dsn: "/tmp/does-not-exist.db"}, "restore"); err != nil {
		t.Fatalf("the sqlite engine must not be pre-flighted: %v", err)
	}
	// And an empty Postgres --dsn is named as the missing flag it is, not probed.
	err := preflightPostgresDR(t.Context(), drFlags{engineKind: "postgres"}, "restore")
	if err == nil || !strings.Contains(err.Error(), "--dsn is required") {
		t.Fatalf("an empty Postgres --dsn must name the flag, got: %v", err)
	}
}
