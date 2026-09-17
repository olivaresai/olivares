// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/store"
)

func ddaReadOnlyDSN(t *testing.T, dsn string) string {
	t.Helper()
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("default_transaction_read_only", "on")
	u.RawQuery = q.Encode()
	return u.String()
}

func TestDRRetainedAuthorityPosture(t *testing.T) {
	for _, variant := range []string{"split_read_only_admin", "schema_CREATE_without_schema_ownership", "read_only_DDL", "read_only_app", "identity_unreadable", "challenge_unavailable"} {
		t.Run(variant, func(t *testing.T) {
			pg := isolatedPGSplit(t)
			cfg := drPGConfig(pg)
			cfg.MaxConns = 1
			super := drOpenSuper(t, pg.Superuser)
			want := ""
			switch variant {
			case "split_read_only_admin":
				cfg.AdminDSN = ddaReadOnlyDSN(t, pg.Admin)
			case "schema_CREATE_without_schema_ownership":
				ownerRole := hcr1Role(t, drOpenSuper(t, pg.Owner))
				drExec(t, super, `ALTER SCHEMA public OWNER TO postgres; GRANT USAGE,CREATE ON SCHEMA public TO `+quoteIdent(ownerRole))
			case "read_only_DDL":
				cfg.OwnerDSN = ddaReadOnlyDSN(t, pg.Owner)
				want = "read-only"
			case "read_only_app":
				cfg.DSN = ddaReadOnlyDSN(t, pg.App)
				want = "read-only"
			case "identity_unreadable", "challenge_unavailable":
				fn := "pg_control_system()"
				want = "system identifier unavailable"
				if variant == "challenge_unavailable" {
					fn = "pg_try_advisory_xact_lock(bigint)"
					want = "live-server challenge refused"
				}
				drExec(t, super, "REVOKE EXECUTE ON FUNCTION pg_catalog."+fn+" FROM PUBLIC")
				t.Cleanup(func() { drExec(t, super, "GRANT EXECUTE ON FUNCTION pg_catalog."+fn+" TO PUBLIC") })
			}
			before := ddaSnapshot(t, super)
			ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
			defer cancel()
			report, err := ddaInstall(ctx, cfg)
			if want != "" {
				if err == nil || !strings.Contains(err.Error(), want) {
					t.Errorf("want %q refusal, got %v", want, err)
				}
				ddaUnchanged(t, super, before)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !report.Created || report.State != "pending" {
				t.Fatalf("wrong initial report: %+v", report)
			}
			if got := drRelationNames(t, super); !reflect.DeepEqual(got, []string{dialect.DRRestoreControlTable}) {
				t.Fatalf("nonminimal footprint: %v", got)
			}
			ddaNoHolders(t, super)
		})
	}
}

func ddaConn(t *testing.T, ctx context.Context, dsn string) *sql.Conn {
	t.Helper()
	db, err := openPGPinnedToEngineSchema(dsn, 1)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	c, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func TestDRRetainedChallengeAndMigrationLifetime(t *testing.T) {
	for _, variant := range []string{"healthy", "holder_lost", "witness_lost"} {
		t.Run(variant, func(t *testing.T) {
			pg := isolatedPG(t)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			super := drOpenSuper(t, pg.Superuser)
			before := ddaSnapshot(t, super)
			coord, err := openDRCoordinationExclusive(ctx, pg.App, drPGConfig(pg))
			if err != nil {
				t.Fatal(err)
			}
			defer coord.close()
			witness := ddaConn(t, ctx, pg.App)
			authority := probeRetainedConnAuthority(ctx, witness)
			if !authority.Posture.Reachable || authority.DatabaseOID == 0 || authority.SchemaOID == 0 || authority.RoleOID == 0 || authority.SessionRole != authority.CurrentRole {
				t.Fatalf("unmeasured retained identity: %+v", authority)
			}
			t.Logf("system=%s database=%s/%d schema=%s/%d session=%s current=%s roleOID=%d readonly=%t recovery=%t", authority.SystemIdentifier, authority.Database, authority.DatabaseOID, authority.Schema, authority.SchemaOID, authority.SessionRole, authority.CurrentRole, authority.RoleOID, authority.SessionReadOnly, authority.InRecovery)
			var holderPID int
			if err := coord.conn.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&holderPID); err != nil {
				t.Fatal(err)
			}
			challenge, err := beginRetainedServerChallenge(ctx, coord.conn)
			if err != nil {
				t.Fatal(err)
			}
			// The actual lock catalog proves key distinctness and that the challenge
			// stays held until every witness has answered.
			var reserved, held bool
			if err := super.QueryRowContext(ctx, `SELECT $1::bigint IN (
 pg_catalog.hashtextextended('olivares.migrate.v1',0),
 pg_catalog.hashtextextended('olivares.restore.v1:'||current_database()||':public',0)),
 EXISTS(SELECT 1 FROM pg_catalog.pg_locks WHERE pid=$2 AND locktype='advisory' AND granted
 AND classid=(($1::bigint>>32)&4294967295)::oid AND objid=($1::bigint&4294967295)::oid AND objsubid=1)`, challenge.key, holderPID).Scan(&reserved, &held); err != nil || reserved || !held {
				t.Fatalf("challenge key/posture: reserved=%t held=%t error=%v", reserved, held, err)
			}
			if variant != "healthy" {
				victim := holderPID
				if variant == "witness_lost" {
					if err := witness.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&victim); err != nil {
						t.Fatal(err)
					}
				}
				var stopped bool
				if err := super.QueryRowContext(ctx, `SELECT pg_terminate_backend($1,3000)`, victim).Scan(&stopped); err != nil || !stopped {
					t.Fatalf("terminate owned pin: %t %v", stopped, err)
				}
			}
			v := challenge.witness(ctx, witness, "actual application")
			if (variant == "healthy") != (v.SameServer && v.Err == "") {
				t.Errorf("wrong challenge verdict: %+v", v)
			}
			rerr := challenge.rollback()
			if variant == "holder_lost" && rerr == nil {
				t.Error("lost holder rollback was accepted")
			}
			if variant != "holder_lost" && rerr != nil {
				t.Fatal(rerr)
			}
			if variant == "healthy" {
				if err := withPinnedMigrationLock(ctx, coord.conn, func(db dialect.Execer) error {
					var pid int
					if err := db.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
						return err
					}
					if pid != holderPID {
						return fmt.Errorf("migration changed session: %d != %d", pid, holderPID)
					}
					var locks int
					if err := db.QueryRowContext(ctx, `SELECT count(*) FROM pg_locks WHERE pid=pg_backend_pid() AND locktype='advisory' AND granted`).Scan(&locks); err != nil {
						return err
					}
					if locks != 2 {
						return fmt.Errorf("want restore + migration, with challenge already rolled back; got %d locks", locks)
					}
					return nil
				}); err != nil {
					t.Fatal(err)
				}
				var pid, locks int
				if err := coord.conn.QueryRowContext(ctx, `SELECT pg_backend_pid(), (SELECT count(*) FROM pg_locks WHERE pid=pg_backend_pid() AND locktype='advisory' AND granted)`).Scan(&pid, &locks); err != nil || pid != holderPID || locks != 1 {
					t.Fatalf("migration helper retired outer holder or leaked a lock: pid=%d locks=%d error=%v", pid, locks, err)
				}
			}
			cerr := coord.close()
			if variant != "holder_lost" && cerr != nil {
				t.Fatal(cerr)
			}
			ddaUnchanged(t, super, before)
		})
	}
}

func TestDRPublicProbesDelegateToRetainedMeasurements(t *testing.T) {
	pg := isolatedPGSplit(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cfg := drPGConfig(pg)
	a, err := ProbeConnAuthority(ctx, cfg)
	if err != nil || !a.Posture.Reachable || a.CanCreate || a.SessionReadOnly || a.InRecovery || a.SystemIdentifier == "" {
		t.Fatalf("public app authority: %+v %v", a, err)
	}
	cfg.DSN = pg.Owner
	a, err = ProbeConnAuthority(ctx, cfg)
	if err != nil || !a.Posture.Reachable || !a.CanCreate {
		t.Fatalf("public DDL authority: %+v %v", a, err)
	}
	cfg.DSN = ddaReadOnlyDSN(t, pg.Owner)
	a, err = ProbeConnAuthority(ctx, cfg)
	if err != nil || !a.Posture.Reachable || !a.CanCreate || !a.SessionReadOnly {
		t.Fatalf("public read-only DDL observation: %+v %v", a, err)
	}
	cfg.DSN = pg.Owner
	r, err := ProbeSameLiveServer(ctx, cfg, "owner", []store.SameServerWitness{{Label: "app", DSN: pg.App}, {Label: "admin", DSN: ddaReadOnlyDSN(t, pg.Admin)}})
	if err != nil || r.HolderErr != "" || len(r.Witnesses) != 2 {
		t.Fatalf("public server probe: %+v %v", r, err)
	}
	for _, w := range r.Witnesses {
		if !w.SameServer || w.Err != "" {
			t.Fatalf("public witness: %+v", w)
		}
	}
	ddaNoHolders(t, drOpenSuper(t, pg.Superuser))
}
