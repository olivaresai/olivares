// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"database/sql"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/internal/pgtest"
	"github.com/olivaresai/olivares/core/store"
)

// The red/green adapter changes only when the pending-only interface replaces
// its unaccepted predecessor. All causal expectations below stay the same.
func ddaInstall(ctx context.Context, cfg store.Config) (RestoreControlReport, error) {
	return InstallPendingRestoreControl(ctx, cfg, PendingRestoreSpec{OpID: drTestOpA, PlanSHA256: drTestPlanA})
}

func ddaSnapshot(t *testing.T, db *sql.DB) string {
	t.Helper()
	return hcr1StateSnapshot(t, &hcr1Fixture{super: db})
}

func ddaNoHolders(t *testing.T, db *sql.DB) {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM pg_catalog.pg_locks WHERE locktype='advisory'
 AND database=(SELECT oid FROM pg_catalog.pg_database WHERE datname=current_database())`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("%d advisory holders/waiters leaked", n)
	}
}

func ddaUnchanged(t *testing.T, db *sql.DB, before string) {
	t.Helper()
	if after := ddaSnapshot(t, db); before != after {
		t.Errorf("destination catalogs/rows/ACL changed\nbefore:\n%s\nafter:\n%s", before, after)
	}
	ddaNoHolders(t, db)
}

func TestDRDestinationAgreementBeforeEffects(t *testing.T) {
	for _, variant := range []string{"app_A_owner_B", "supplied_admin_B", "coordination_B_work_A"} {
		t.Run(variant, func(t *testing.T) {
			a, b := isolatedPG(t), isolatedPG(t)
			as, bs := drOpenSuper(t, a.Superuser), drOpenSuper(t, b.Superuser)
			// A nonempty marker and distinct grants make the snapshot about rows and
			// ACLs as well as absence of a control table.
			for i, db := range []*sql.DB{as, bs} {
				drExec(t, db, `CREATE TABLE public.dda_marker(value integer); INSERT INTO public.dda_marker VALUES (`+fmt.Sprint(i+1)+`)`)
			}
			beforeA, beforeB := ddaSnapshot(t, as), ddaSnapshot(t, bs)
			cfg := drPGConfig(a)
			cfg.MaxConns = 1
			switch variant {
			case "app_A_owner_B":
				cfg.OwnerDSN = b.App
			case "supplied_admin_B":
				cfg.AdminDSN = b.Admin
				foreignAdmin := hcr1Role(t, drOpenSuper(t, b.Admin))
				// The defective installer can grant A's control to B's admin. Remove
				// that cross-database fixture dependency only AFTER the assertions,
				// before B's isolation cleanup tries to drop its private role.
				t.Cleanup(func() { drExec(t, as, "DROP OWNED BY "+quoteIdent(foreignAdmin)) })
			case "coordination_B_work_A":
				// Same configured URL, different actual first connection. A later
				// independent DSN reprobe sees A and cannot certify the retained B.
				cfg.DSN = ddaFirstDatabaseRoute(t, a.App, b.Database)
				cfg.OwnerDSN = ""
			}
			ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
			defer cancel()
			_, err := ddaInstall(ctx, cfg)
			if err == nil || !strings.Contains(err.Error(), "destination agreement") {
				t.Errorf("want pre-effect destination agreement refusal, got %v", err)
			}
			ddaUnchanged(t, as, beforeA)
			ddaUnchanged(t, bs, beforeB)
		})
	}
}

// This routes one real startup packet to B; all later sessions use the original
// A. Credentials stay in memory and are neither logged nor passed in argv.
// It is an actual-session routing causal, not a metadata double or replica.
func ddaFirstDatabaseRoute(t *testing.T, dsn, firstDatabase string) string {
	t.Helper()
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		first := true
		for {
			client, err := listener.Accept()
			if err != nil {
				return
			}
			rewrite := first
			first = false
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer client.Close()
				_ = client.SetDeadline(time.Now().Add(20 * time.Second))
				server, err := net.DialTimeout("tcp", u.Host, 3*time.Second)
				if err != nil {
					t.Errorf("route dial: %v", err)
					return
				}
				defer server.Close()
				_ = server.SetDeadline(time.Now().Add(20 * time.Second))
				var header [4]byte
				if _, err := io.ReadFull(client, header[:]); err != nil {
					return
				}
				n := binary.BigEndian.Uint32(header[:])
				if n < 8 || n > 65536 {
					t.Error("invalid fixture startup length")
					return
				}
				body := make([]byte, n-4)
				if _, err := io.ReadFull(client, body); err != nil {
					return
				}
				if binary.BigEndian.Uint32(body[:4]) != 196608 {
					t.Error("fixture requires plaintext PG startup")
					return
				}
				if rewrite {
					fields := strings.Split(string(body[4:]), "\x00")
					found := false
					for i := 0; i+1 < len(fields); i += 2 {
						if fields[i] == "database" {
							fields[i+1] = firstDatabase
							found = true
						}
					}
					if !found {
						t.Error("fixture startup has no database")
						return
					}
					body = append(body[:4], []byte(strings.Join(fields, "\x00"))...)
					binary.BigEndian.PutUint32(header[:], uint32(len(body)+4))
				}
				if _, err := server.Write(append(header[:], body...)); err != nil {
					return
				}
				done := make(chan struct{})
				go func() { _, _ = io.Copy(server, client); _ = server.Close(); close(done) }()
				_, _ = io.Copy(client, server)
				_ = client.Close()
				<-done
			}()
		}
	}()
	t.Cleanup(func() { _ = listener.Close(); wg.Wait() })
	v := *u
	v.Host = listener.Addr().String()
	return v.String()
}

func TestDRDestinationUnrelatedCluster(t *testing.T) {
	other := os.Getenv("OLIVARES_TEST_POSTGRES_OTHER_DSN")
	if other == "" {
		t.Fatal("a second owned real cluster is required")
	}
	a := isolatedPG(t)
	t.Setenv(pgtest.EnvSuperuserDSN, other)
	b := isolatedPG(t)
	as, bs := drOpenSuper(t, a.Superuser), drOpenSuper(t, b.Superuser)
	var aid, bid string
	for db, dest := range map[*sql.DB]*string{as: &aid, bs: &bid} {
		if err := db.QueryRow(`SELECT system_identifier::text FROM pg_catalog.pg_control_system()`).Scan(dest); err != nil {
			t.Fatal(err)
		}
	}
	if aid == bid {
		t.Fatal("fixture did not create unrelated clusters")
	}
	t.Logf("actual unrelated clusters: A=%s B=%s databases=%s/%s", aid, bid, a.Database, b.Database)
	probeCtx, probeCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer probeCancel()
	preflight, perr := ProbeSameLiveServer(probeCtx, drPGConfig(b), "B", []store.SameServerWitness{{Label: "A", DSN: a.App}})
	if perr != nil || preflight.HolderErr != "" || len(preflight.Witnesses) != 1 || preflight.Witnesses[0].SameServer || !preflight.Witnesses[0].AcquiredHoldersLock || preflight.Witnesses[0].Err != "" {
		t.Fatalf("actual unrelated-cluster preflight challenge: %+v %v", preflight, perr)
	}
	beforeA, beforeB := ddaSnapshot(t, as), ddaSnapshot(t, bs)
	cfg := drPGConfig(a)
	cfg.OwnerDSN = b.App
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	_, err := ddaInstall(ctx, cfg)
	if err == nil || !strings.Contains(err.Error(), "destination agreement") {
		t.Errorf("want unrelated-cluster pre-effect refusal, got %v", err)
	}
	ddaUnchanged(t, as, beforeA)
	ddaUnchanged(t, bs, beforeB)
}

func ddaWait(t *testing.T, ctx context.Context, db *sql.DB, query string) int {
	t.Helper()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		var pid int
		if err := db.QueryRowContext(ctx, query).Scan(&pid); err == nil && pid != 0 {
			return pid
		}
		select {
		case <-ctx.Done():
			t.Fatalf("fixture synchronization timed out: %v", ctx.Err())
			return 0
		case <-tick.C:
		}
	}
}

func TestDRDestinationHolderLossCannotCommit(t *testing.T) {
	for _, phase := range []string{"before_DDL", "after_DDL"} {
		t.Run(phase, func(t *testing.T) {
			pg := isolatedPG(t)
			cfg := drPGConfig(pg)
			cfg.MaxConns = 1
			super := drOpenSuper(t, pg.Superuser)
			// The barrier connection, the event-trigger DDL of the after_DDL phase, the
			// advisory lock and the catalogue snapshot below are fixture; the 15 s budget is
			// for the holder-loss race. Starting it here charged the DDL and the snapshot to
			// the deadline the race depends on, which under -race on a contended runner is
			// how this class fails at the wait instead of at the behaviour (01f81b8e81 /
			// 4859cc43f3 / 346bce0c8a).
			setupCtx := context.Background()
			barrier, err := super.Conn(setupCtx)
			if err != nil {
				t.Fatal(err)
			}
			defer barrier.Close()
			lock := `pg_catalog.hashtextextended('olivares.migrate.v1',0)`
			if phase == "after_DDL" {
				lock = "81923746321::bigint"
				drExec(t, super, `CREATE FUNCTION public.dda_ddl_barrier() RETURNS event_trigger LANGUAGE plpgsql AS $$
 BEGIN PERFORM pg_catalog.pg_advisory_xact_lock(81923746321::bigint); END $$;
 CREATE EVENT TRIGGER dda_ddl_barrier ON ddl_command_end WHEN TAG IN ('CREATE TABLE')
 EXECUTE FUNCTION public.dda_ddl_barrier()`)
			}
			if _, err := barrier.ExecContext(setupCtx, `SELECT pg_catalog.pg_advisory_lock(`+lock+`)`); err != nil {
				t.Fatal(err)
			}
			defer func() { _, _ = barrier.ExecContext(context.Background(), `SELECT pg_catalog.pg_advisory_unlock_all()`) }()
			before := ddaSnapshot(t, super)
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			waiting, resume := make(chan struct{}), make(chan struct{})
			if phase == "before_DDL" {
				previous := newCoordinationBudget
				var once sync.Once
				newCoordinationBudget = func() *lockBudget {
					return newLockBudget(10*time.Second, time.Now, func(ctx context.Context, _ time.Duration) error {
						once.Do(func() { close(waiting) })
						select {
						case <-resume:
							return nil
						case <-ctx.Done():
							return ctx.Err()
						}
					}, jitterFloat)
				}
				t.Cleanup(func() { newCoordinationBudget = previous })
			}
			result := make(chan error, 1)
			go func() { _, err := ddaInstall(ctx, cfg); result <- err }()
			defer func() {
				cancel()
				select {
				case <-result:
				case <-time.After(3 * time.Second):
				}
			}()
			waiterQuery := `SELECT pid FROM pg_catalog.pg_locks WHERE locktype='advisory'
 AND database=(SELECT oid FROM pg_catalog.pg_database WHERE datname=current_database())
 AND NOT granted LIMIT 1`
			if phase == "before_DDL" {
				select {
				case <-waiting:
				case <-ctx.Done():
					t.Fatal("migration poll did not reach backoff")
				}
				waiterQuery = `SELECT pid FROM pg_catalog.pg_stat_activity WHERE datname=current_database()
 AND usename='olivares_app' AND query LIKE '%pg_catalog.pg_locks%' LIMIT 1`
			}
			waiter := ddaWait(t, ctx, super, waiterQuery)
			holder := ddaWait(t, ctx, super, `SELECT pid FROM pg_catalog.pg_locks WHERE locktype='advisory' AND granted
 AND database=(SELECT oid FROM pg_catalog.pg_database WHERE datname=current_database())
 AND classid=((pg_catalog.hashtextextended('olivares.restore.v1:'||current_database()||':public',0)>>32)&4294967295)::oid
 AND objid=(pg_catalog.hashtextextended('olivares.restore.v1:'||current_database()||':public',0)&4294967295)::oid
 AND objsubid=1 AND mode='ExclusiveLock'`)
			t.Logf("%s: actual exclusive holder=%d DDL/migration waiter=%d", phase, holder, waiter)
			var terminated bool
			if err := super.QueryRowContext(ctx, `SELECT pg_catalog.pg_terminate_backend($1)`, holder).Scan(&terminated); err != nil || !terminated {
				t.Fatalf("terminate exact owned restore backend: %t %v", terminated, err)
			}
			if _, err := barrier.ExecContext(ctx, `SELECT pg_catalog.pg_advisory_unlock_all()`); err != nil {
				t.Fatal(err)
			}
			close(resume)
			select {
			case err := <-result:
				if err == nil {
					t.Error("lost exclusive holder returned success")
				}
				// Leave an already-drained value for deferred cancellation cleanup.
				result <- err
			case <-ctx.Done():
				t.Fatal("installer failed bounded cleanup")
			}
			ddaUnchanged(t, super, before)
		})
	}
}
