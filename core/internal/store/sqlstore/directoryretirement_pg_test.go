// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/internal/pgtest"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// These tests are intentionally split-owner only. B4's PostgreSQL claim is
// about an application role constrained by FORCE RLS, a distinct DDL owner and
// a separately attested BYPASSRLS/read-only administrator. A single-role
// fixture would exercise SQL syntax while silently deleting that authority
// boundary from the test.
type directoryRetirementPostgresHarness struct {
	pg  pgtest.DSNs
	cfg store.Config
	raw store.Store
	sql *sqlStore
}

func newDirectoryRetirementPostgresHarness(
	t *testing.T,
) directoryRetirementPostgresHarness {
	t.Helper()
	pg := isolatedPGSplit(t)
	cfg := store.Config{
		Engine: store.EnginePostgres,
		DSN:    pg.App, OwnerDSN: pg.Owner, AdminDSN: pg.Admin,
		MaxConns: 12,
	}
	raw := retirementOpenPostgres(t, cfg)
	return directoryRetirementPostgresHarness{
		pg: pg, cfg: cfg, raw: raw, sql: raw.(*sqlStore),
	}
}

func retirementOpenPostgres(t *testing.T, cfg store.Config) store.Store {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	raw, err := Open(ctx, cfg, nil)
	if err != nil {
		t.Fatalf("open retirement PostgreSQL store: %v", err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	return raw
}

func (h directoryRetirementPostgresHarness) enforce(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, after, changed, err := ActivateDirectoryWriter(ctx, h.raw, h.cfg, 1)
	if err != nil {
		t.Fatalf("activate split-owner directory writer: %v", err)
	}
	if !changed || after.ControlMode != store.DirectoryControlEnforced ||
		after.ExpectedGeneration != 2 ||
		after.WriterPosture != store.DirectoryWriterSplitOwner {
		t.Fatalf("split-owner activation result = %+v changed=%t", after, changed)
	}
}

func retirementPostgresRowCount(
	t *testing.T,
	ss *sqlStore,
	table string,
	predicate string,
	args ...any,
) int64 {
	t.Helper()
	query := "SELECT COUNT(*) FROM public." + quoteIdent(table)
	if predicate != "" {
		query += " WHERE " + predicate
	}
	if ss.adminDB == nil {
		t.Fatalf("count PostgreSQL %s: split-owner AdminDSN is unavailable", table)
	}
	var count int64
	if err := ss.adminDB.QueryRowContext(
		context.Background(), ss.dia.Rebind(query), args...,
	).Scan(&count); err != nil {
		t.Fatalf("count PostgreSQL %s: %v", table, err)
	}
	return count
}

func retirementPostgresUserRequest(user model.User) UserRetirementRequest {
	return UserRetirementRequest{
		UserID: user.ID, ExpectedVersion: user.Version,
		Actor: retirementTestActor, ActorKind: model.ActorUser,
	}
}

func retirementPostgresPrincipalRequest(
	tenant model.TenantID,
	kind model.DirectoryPrincipalKind,
	source model.ID,
	version int64,
) DirectoryPrincipalRetirementRequest {
	return DirectoryPrincipalRetirementRequest{
		TenantID: tenant, PrincipalKind: kind,
		SourceID: source, ExpectedVersion: version,
		Actor: retirementTestActor, ActorKind: model.ActorUser,
	}
}

func retirementPostgresWaitForAdvisoryWaiter(
	ctx context.Context,
	dsn string,
	database string,
) error {
	observer, err := sql.Open("pgx", dsn)
	if err != nil {
		return fmt.Errorf("open PostgreSQL advisory-wait observer: %w", err)
	}
	defer observer.Close() //nolint:errcheck
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var waiting int
		if err := observer.QueryRowContext(ctx, `
SELECT COUNT(*)
FROM pg_catalog.pg_stat_activity
WHERE datname = $1
  AND wait_event_type = 'Lock'
  AND wait_event = 'advisory'`, database).Scan(&waiting); err != nil {
			return fmt.Errorf("observe PostgreSQL advisory waiter: %w", err)
		}
		if waiting > 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("observe PostgreSQL advisory waiter: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}

const retirementIdentityProbeDriverName = "olivares-retirement-identity-probe"

var retirementIdentityProbeDriverRegistration sync.Once

type retirementIdentityProbeDriver struct{}

func (retirementIdentityProbeDriver) Open(name string) (driver.Conn, error) {
	parts := strings.Split(name, "|")
	if len(parts) != 2 || (parts[1] != "true" && parts[1] != "false") {
		return nil, fmt.Errorf("invalid retirement identity probe DSN %q", name)
	}
	return &retirementIdentityProbeConn{
		database: parts[0], acquired: parts[1] == "true",
	}, nil
}

type retirementIdentityProbeConn struct {
	database string
	acquired bool
}

func (*retirementIdentityProbeConn) Prepare(string) (driver.Stmt, error) {
	return nil, driver.ErrSkip
}

func (*retirementIdentityProbeConn) Close() error { return nil }

func (*retirementIdentityProbeConn) Begin() (driver.Tx, error) {
	return retirementIdentityProbeTx{}, nil
}

func (*retirementIdentityProbeConn) BeginTx(
	context.Context,
	driver.TxOptions,
) (driver.Tx, error) {
	return retirementIdentityProbeTx{}, nil
}

func (c *retirementIdentityProbeConn) QueryContext(
	_ context.Context,
	query string,
	_ []driver.NamedValue,
) (driver.Rows, error) {
	switch {
	case strings.Contains(query, "current_database"):
		return &retirementIdentityProbeRows{
			columns: []string{"current_database"}, values: []driver.Value{c.database},
		}, nil
	case strings.Contains(query, "pg_try_advisory_xact_lock"):
		return &retirementIdentityProbeRows{
			columns: []string{"pg_try_advisory_xact_lock"}, values: []driver.Value{c.acquired},
		}, nil
	default:
		return nil, fmt.Errorf("unexpected retirement identity probe query %q", query)
	}
}

type retirementIdentityProbeTx struct{}

func (retirementIdentityProbeTx) Commit() error   { return nil }
func (retirementIdentityProbeTx) Rollback() error { return nil }

type retirementIdentityProbeRows struct {
	columns   []string
	values    []driver.Value
	delivered bool
}

func (r *retirementIdentityProbeRows) Columns() []string { return r.columns }
func (*retirementIdentityProbeRows) Close() error        { return nil }

func (r *retirementIdentityProbeRows) Next(dest []driver.Value) error {
	if r.delivered {
		return io.EOF
	}
	copy(dest, r.values)
	r.delivered = true
	return nil
}

func TestRetireUserAdminIdentityChallengeRejectsSameDatabaseOnIndependentCluster(t *testing.T) {
	retirementIdentityProbeDriverRegistration.Do(func() {
		sql.Register(retirementIdentityProbeDriverName, retirementIdentityProbeDriver{})
	})
	for _, tc := range []struct {
		name              string
		gotDatabase       string
		challengeAcquired bool
		wantError         bool
		wantFragment      string
	}{
		{
			name:        "same database name but independent advisory namespace",
			gotDatabase: "same_name", challengeAcquired: true,
			wantError: true, wantFragment: "challenge_acquired=true",
		},
		{
			name:        "same database and contended challenge",
			gotDatabase: "same_name", challengeAcquired: false,
		},
		{
			name:        "different database name",
			gotDatabase: "foreign_name", challengeAcquired: false,
			wantError: true, wantFragment: "database=\"foreign_name\" want=\"same_name\"",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, err := sql.Open(
				retirementIdentityProbeDriverName,
				fmt.Sprintf("%s|%t", tc.gotDatabase, tc.challengeAcquired),
			)
			if err != nil {
				t.Fatalf("open retirement identity probe: %v", err)
			}
			defer db.Close() //nolint:errcheck
			tx, err := db.BeginTx(context.Background(), nil)
			if err != nil {
				t.Fatalf("begin retirement identity probe: %v", err)
			}
			defer tx.Rollback() //nolint:errcheck
			err = probeDirectoryActivationDatabase(
				context.Background(), tx, "same_name", 42, "admin",
			)
			if tc.wantError {
				if err == nil || !strings.Contains(err.Error(), tc.wantFragment) {
					t.Fatalf("retirement AdminDSN identity probe = %v, want %q", err, tc.wantFragment)
				}
				return
			}
			if err != nil {
				t.Fatalf("contended same-database AdminDSN identity probe: %v", err)
			}
		})
	}
}

func TestDirectoryRetirementPostgresSplitOwnerPrincipalMatrixAndReopen(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h := newDirectoryRetirementPostgresHarness(t)
	tenantA := provisionTenant(t, h.raw, "retirement-pg-principals-a")
	tenantB := provisionTenant(t, h.raw, "retirement-pg-principals-b")

	identity := retirementCreateIdentity(t, h.raw, tenantA, "pg-retire-identity")
	agentIdentity := retirementCreateIdentity(t, h.raw, tenantA, "pg-retire-agent-identity")
	agents := []model.Agent{
		retirementCreateAgent(
			t, h.raw, tenantA, agentIdentity.ID, "", "pg-retire-agent-a", model.StatusActive,
		),
		retirementCreateAgent(
			t, h.raw, tenantA, agentIdentity.ID, "", "pg-retire-agent-b", model.StatusInactive,
		),
	}
	workspace := retirementDefaultWorkspace(t, h.raw, tenantA)
	victim, err := directoryEpochTestSeedRetainedAuth(ctx, h.raw, "pg-retire-user")
	if err != nil {
		t.Fatalf("seed PostgreSQL User authority: %v", err)
	}
	estate, err := directoryEpochTestSeedAuthEstate(
		ctx, h.raw, tenantA, "pg-retire-user-estate", victim.user.ID,
	)
	if err != nil {
		t.Fatalf("seed PostgreSQL User tenant authority: %v", err)
	}
	h.enforce(t)

	identityReq := retirementPostgresPrincipalRequest(
		tenantA, model.DirectoryPrincipalIdentity, identity.ID, identity.Version,
	)
	beforeIdentity := directoryWriterTestEpoch(t, h.raw, tenantA).Version
	identityResult, err := RetireDirectoryPrincipal(ctx, h.raw, identityReq)
	if err != nil {
		t.Fatalf("retire PostgreSQL Identity: %v", err)
	}
	if !identityResult.Definitive || identityResult.Tombstone == nil ||
		identityResult.Code != DirectoryRetirementDefinitive ||
		identityResult.Principal.PrincipalRef != identity.ID ||
		identityResult.ResultingEpoch != beforeIdentity+1 ||
		identityResult.AuditSeq < 1 || len(identityResult.AuditHash) != 32 {
		t.Fatalf("PostgreSQL Identity retirement result = %+v", identityResult)
	}

	type agentOutcome struct {
		req    DirectoryPrincipalRetirementRequest
		result DirectoryPrincipalRetirementResult
		err    error
	}
	beforeAgents := directoryWriterTestEpoch(t, h.raw, tenantA).Version
	start := make(chan struct{})
	done := make(chan agentOutcome, len(agents))
	for _, agent := range agents {
		req := retirementPostgresPrincipalRequest(
			tenantA, model.DirectoryPrincipalAgent, agent.ID, agent.Version,
		)
		go func() {
			<-start
			result, retireErr := RetireDirectoryPrincipal(ctx, h.raw, req)
			done <- agentOutcome{req: req, result: result, err: retireErr}
		}()
	}
	close(start)
	agentOutcomes := make([]agentOutcome, 0, len(agents))
	definitive, bindingOnly := 0, 0
	for range agents {
		select {
		case outcome := <-done:
			if outcome.err != nil {
				t.Fatalf("concurrent PostgreSQL Agent retirement: %v", outcome.err)
			}
			agentOutcomes = append(agentOutcomes, outcome)
			switch {
			case outcome.result.Definitive &&
				outcome.result.Code == DirectoryRetirementDefinitive &&
				outcome.result.Tombstone != nil:
				definitive++
			case !outcome.result.Definitive &&
				outcome.result.Code == DirectoryRetirementAgentBindingRemains &&
				outcome.result.Tombstone == nil:
				bindingOnly++
			default:
				t.Fatalf("unexpected PostgreSQL Agent retirement result: %+v", outcome.result)
			}
			if outcome.result.Principal.PrincipalRef != agentIdentity.ID ||
				outcome.result.Principal.WorkspaceRef != workspace.ID ||
				outcome.result.AuditSeq < 1 || len(outcome.result.AuditHash) != 32 {
				t.Fatalf("incomplete PostgreSQL Agent receipt: %+v", outcome.result)
			}
		case <-ctx.Done():
			t.Fatalf("concurrent PostgreSQL Agent retirement deadlocked: %v", ctx.Err())
		}
	}
	if definitive != 1 || bindingOnly != 1 {
		t.Fatalf("PostgreSQL Agent results definitive=%d binding=%d, want 1/1",
			definitive, bindingOnly)
	}
	if got := directoryWriterTestEpoch(t, h.raw, tenantA).Version; got != beforeAgents+2 {
		t.Fatalf("PostgreSQL Agent epoch = %d, want %d", got, beforeAgents+2)
	}
	if got := retirementPostgresRowCount(
		t, h.sql, agentDescriptor.Table,
		"tenant_id = ? AND identity_id = ?", tenantA.String(), agentIdentity.ID.String(),
	); got != 0 {
		t.Fatalf("PostgreSQL retired Agent source rows = %d, want 0", got)
	}
	if got := retirementPostgresRowCount(
		t, h.sql, directoryTombstoneDescriptor.Table,
		"tenant_id = ? AND principal_kind = ? AND principal_ref = ? AND workspace_ref = ?",
		tenantA.String(), string(model.DirectoryPrincipalAgent),
		agentIdentity.ID.String(), workspace.ID.String(),
	); got != 1 {
		t.Fatalf("PostgreSQL Agent tombstones = %d, want 1", got)
	}
	for action, want := range map[string]int64{
		model.AuditActionAgentBindingRetire:       1,
		model.AuditActionDirectoryPrincipalRetire: 2, // Identity plus definitive Agent.
	} {
		if got := retirementPostgresRowCount(
			t, h.sql, auditTable, "tenant_id = ? AND action = ?", tenantA.String(), action,
		); got != want {
			t.Fatalf("PostgreSQL audit action %q rows = %d, want %d", action, got, want)
		}
	}

	beforeUserA := directoryWriterTestEpoch(t, h.raw, tenantA).Version
	beforeUserB := directoryWriterTestEpoch(t, h.raw, tenantB).Version
	userReq := retirementPostgresUserRequest(victim.user)
	userTombstone, err := RetireUser(ctx, h.raw, userReq)
	if err != nil {
		t.Fatalf("retire PostgreSQL User: %v", err)
	}
	if err := userTombstone.Validate(); err != nil {
		t.Fatalf("PostgreSQL User tombstone Validate: %v", err)
	}
	if userTombstone.AuditAnchor.Seq < 1 || len(userTombstone.AuditAnchor.Hash) != 32 ||
		len(userTombstone.ResultingEpochs) != 2 {
		t.Fatalf("PostgreSQL User retirement evidence = %+v", userTombstone)
	}
	for tenant, want := range map[model.TenantID]int64{
		tenantA: beforeUserA + 1,
		tenantB: beforeUserB + 1,
	} {
		got, carried := userTombstone.ResultingEpochs.EpochFor(tenant)
		if !carried || got != want {
			t.Fatalf("PostgreSQL User epoch tenant=%s got=%d carried=%t want=%d",
				tenant, got, carried, want)
		}
		witness, found, readErr := retirementReadWitness(t, h.raw, tenant,
			store.DirectoryPrincipalRef{
				PrincipalKind: model.DirectoryPrincipalUser,
				PrincipalRef:  victim.user.ID,
			})
		if readErr != nil || !found || witness.TombstoneID != userTombstone.ID ||
			witness.RetirementEpoch != want {
			t.Fatalf("PostgreSQL User witness tenant=%s got=%+v found=%t err=%v",
				tenant, witness, found, readErr)
		}
	}
	if err := h.raw.AuthView(ctx, func(auth store.AuthScope) error {
		for name, get := range map[string]func() error{
			"User": func() error {
				_, getErr := auth.Users().Get(ctx, victim.user.ID)
				return getErr
			},
			"session": func() error {
				_, getErr := auth.Sessions().Get(ctx, victim.session.ID)
				return getErr
			},
			"WebAuthn": func() error {
				_, getErr := auth.WebAuthnCredentials().Get(ctx, victim.credential.ID)
				return getErr
			},
			"membership": func() error {
				_, getErr := auth.Memberships().Get(ctx, estate.membership.ID)
				return getErr
			},
			"token": func() error {
				_, getErr := auth.Tokens().Get(ctx, estate.token.ID)
				return getErr
			},
			"handle": func() error {
				_, getErr := auth.DelegationHandles().Get(ctx, estate.handle.ID)
				return getErr
			},
			"PEP credential": func() error {
				_, getErr := auth.PEPServiceCredentials().Get(ctx, estate.credential.ID)
				return getErr
			},
		} {
			if getErr := get(); !errors.Is(getErr, store.ErrNotFound) {
				return fmt.Errorf("%s survived PostgreSQL User retirement: %w", name, getErr)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// A process can lose the commit ACK and restart before retrying. Reopen the
	// same split-role database, then require every source-specific receipt to
	// replay exactly without another epoch bump (including the non-final Agent
	// receipt after its sibling has since emitted the definitive tombstone).
	if err := h.raw.Close(); err != nil {
		t.Fatalf("close PostgreSQL retirement store before replay: %v", err)
	}
	reopened := retirementOpenPostgres(t, h.cfg)
	if replayed, replayErr := RetireDirectoryPrincipal(ctx, reopened, identityReq); replayErr != nil ||
		!reflect.DeepEqual(replayed, identityResult) {
		t.Fatalf("reopened Identity replay = %+v err=%v, want %+v",
			replayed, replayErr, identityResult)
	}
	for _, outcome := range agentOutcomes {
		replayed, replayErr := RetireDirectoryPrincipal(ctx, reopened, outcome.req)
		if replayErr != nil || !reflect.DeepEqual(replayed, outcome.result) {
			t.Fatalf("reopened Agent replay source=%s got=%+v err=%v want=%+v",
				outcome.req.SourceID, replayed, replayErr, outcome.result)
		}
	}
	if replayed, replayErr := RetireUser(ctx, reopened, userReq); replayErr != nil ||
		!reflect.DeepEqual(replayed, userTombstone) {
		t.Fatalf("reopened User replay = %+v err=%v, want %+v",
			replayed, replayErr, userTombstone)
	}
	if got := directoryWriterTestEpoch(t, reopened, tenantA).Version; got != beforeUserA+1 {
		t.Fatalf("reopened replay changed tenant A epoch to %d, want %d", got, beforeUserA+1)
	}
	if got := directoryWriterTestEpoch(t, reopened, tenantB).Version; got != beforeUserB+1 {
		t.Fatalf("reopened replay changed tenant B epoch to %d, want %d", got, beforeUserB+1)
	}
}

func TestRetireUserPostgresSplitOwnerAdminAttestationOnReopen(t *testing.T) {
	t.Run("exact boot role succeeds", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		h := newDirectoryRetirementPostgresHarness(t)
		provisionTenant(t, h.raw, "retirement-pg-admin-exact")
		user := retirementCreateUser(t, h.raw, "retirement-pg-admin-exact")
		h.enforce(t)
		if h.sql.directoryAdminRole.Role != h.pg.Result.AdminPosture.Role ||
			!h.sql.directoryAdminRole.Known {
			t.Fatalf("boot AdminDSN role = %+v, want exact %q",
				h.sql.directoryAdminRole, h.pg.Result.AdminPosture.Role)
		}
		if err := h.raw.Close(); err != nil {
			t.Fatalf("close before exact-role reopen: %v", err)
		}
		reopened := retirementOpenPostgres(t, h.cfg)
		if _, err := RetireUser(ctx, reopened, retirementPostgresUserRequest(user)); err != nil {
			t.Fatalf("RetireUser with exact live boot role: %v", err)
		}
	})

	t.Run("live role drift is refused before mutation", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		primary := newDirectoryRetirementPostgresHarness(t)
		foreign := newDirectoryRetirementPostgresHarness(t)
		tenant := provisionTenant(t, primary.raw, "retirement-pg-admin-role-drift")
		user := retirementCreateUser(t, primary.raw, "retirement-pg-admin-role-drift")
		primary.enforce(t)
		beforeEpoch := directoryWriterTestEpoch(t, primary.raw, tenant).Version

		foreignRole := foreign.pg.Result.AdminPosture.Role
		super, err := sql.Open("pgx", primary.pg.Superuser)
		if err != nil {
			t.Fatalf("open superuser for alternate AdminDSN role: %v", err)
		}
		defer super.Close() //nolint:errcheck
		// This role belongs to the foreign isolated harness, but the live-role
		// mutant grants it privileges in the primary database. Register this
		// cleanup after both harnesses so testing's LIFO order removes the
		// cross-database dependencies before pgtest tries to drop the foreign
		// role. Open a fresh connection because ordinary defers close the setup
		// handle before t.Cleanup runs.
		t.Cleanup(func() {
			cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cleanupCancel()
			cleanupDB, openErr := sql.Open("pgx", primary.pg.Superuser)
			if openErr != nil {
				t.Errorf("open primary cleanup connection for alternate AdminDSN role: %v", openErr)
				return
			}
			defer cleanupDB.Close() //nolint:errcheck
			if _, cleanupErr := cleanupDB.ExecContext(
				cleanupCtx, "DROP OWNED BY "+quoteIdent(foreignRole),
			); cleanupErr != nil {
				t.Errorf("drop primary grants owned by alternate AdminDSN role: %v", cleanupErr)
			}
		})
		for _, statement := range []string{
			"GRANT CONNECT ON DATABASE " + quoteIdent(primary.pg.Database) + " TO " + quoteIdent(foreignRole),
			"GRANT USAGE ON SCHEMA public TO " + quoteIdent(foreignRole),
			"GRANT SELECT ON ALL TABLES IN SCHEMA public TO " + quoteIdent(foreignRole),
		} {
			if _, err := super.ExecContext(ctx, statement); err != nil {
				t.Fatalf("grant alternate AdminDSN role %q: %v", statement, err)
			}
		}
		alternateDSN := directoryActivationTestDSNForDatabase(
			t, foreign.pg.Admin, primary.pg.Database,
		)
		alternate, err := openPGPinnedToEngineSchema(alternateDSN, 2)
		if err != nil {
			t.Fatalf("open alternate same-database AdminDSN: %v", err)
		}
		defer alternate.Close() //nolint:errcheck
		goodAdmin := primary.sql.adminDB
		primary.sql.adminDB = alternate
		_, retireErr := RetireUser(ctx, primary.raw, retirementPostgresUserRequest(user))
		primary.sql.adminDB = goodAdmin
		if !errors.Is(retireErr, store.ErrEnumerationNotAuthoritative) ||
			!strings.Contains(retireErr.Error(), "role changed since boot") {
			t.Fatalf("RetireUser after live AdminDSN role drift = %v", retireErr)
		}
		if got := directoryWriterTestEpoch(t, primary.raw, tenant).Version; got != beforeEpoch {
			t.Fatalf("role-drift refusal epoch = %d, want %d", got, beforeEpoch)
		}
		if got := retirementPostgresRowCount(
			t, primary.sql, userDescriptor.Table,
			"tenant_id = ? AND id = ?", model.SystemTenantID.String(), user.ID.String(),
		); got != 1 {
			t.Fatalf("role-drift refusal User rows = %d, want 1", got)
		}
	})

	// ⛔ ESTE SUBTEST EXISTE PORQUE ELEGIR EL CONTRATO A ABRE UN PUNTO CIEGO, y el
	// contraste que adjudicó A lo nombró antes de que se reescribiera nada.
	//
	// Con A, un AdminDSN cuya identidad de base ha sido refutada hace fallar Open, así
	// que el subtest de abajo deja de llegar a RetireUser — y con ello deja de ejercitar
	// la RE-ATESTACIÓN EN VIVO de identidad que RetireUser hace antes de mutar. El
	// subtest de deriva de al lado no lo cubre: prueba un cambio de ROL, no un pool
	// AJENO conservando el rol fijado, que es una combinación distinta.
	//
	// Adelantar una negativa es barato; perder el caso que la negativa ya no alcanza no
	// lo es. Este test cubre exactamente ese hueco: el pool vivo se reencamina a OTRA
	// base MANTENIENDO el rol fijado, de modo que la postura de rol sigue siendo
	// correcta y lo único falso es la identidad de la base.
	t.Run("live foreign database with the pinned role is refused before mutation", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		primary := newDirectoryRetirementPostgresHarness(t)
		foreign := newDirectoryRetirementPostgresHarness(t)
		tenant := provisionTenant(t, primary.raw, "retirement-pg-live-foreign-db")
		user := retirementCreateUser(t, primary.raw, "retirement-pg-live-foreign-db")
		primary.enforce(t)
		beforeEpoch := directoryWriterTestEpoch(t, primary.raw, tenant).Version

		// The PINNED role must be able to reach the foreign database, or the refusal
		// would come from connectivity instead of from the identity challenge — which
		// would make this test pass for the wrong reason.
		pinnedRole := primary.pg.Result.AdminPosture.Role
		super, err := sql.Open("pgx", foreign.pg.Superuser)
		if err != nil {
			t.Fatalf("open foreign superuser to grant the pinned role: %v", err)
		}
		defer super.Close() //nolint:errcheck
		t.Cleanup(func() {
			cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cleanupCancel()
			cleanupDB, openErr := sql.Open("pgx", foreign.pg.Superuser)
			if openErr != nil {
				t.Errorf("open foreign cleanup connection for the pinned role: %v", openErr)
				return
			}
			defer cleanupDB.Close() //nolint:errcheck
			if _, cleanupErr := cleanupDB.ExecContext(
				cleanupCtx, "DROP OWNED BY "+quoteIdent(pinnedRole),
			); cleanupErr != nil {
				t.Errorf("drop foreign grants owned by the pinned role: %v", cleanupErr)
			}
		})
		for _, statement := range []string{
			"GRANT CONNECT ON DATABASE " + quoteIdent(foreign.pg.Database) + " TO " + quoteIdent(pinnedRole),
			"GRANT USAGE ON SCHEMA public TO " + quoteIdent(pinnedRole),
			"GRANT SELECT ON ALL TABLES IN SCHEMA public TO " + quoteIdent(pinnedRole),
		} {
			if _, err := super.ExecContext(ctx, statement); err != nil {
				t.Fatalf("grant the pinned role on the foreign database %q: %v", statement, err)
			}
		}

		// Same credential, same pinned role, DIFFERENT database: the only thing that
		// changes is the identity the challenge measures.
		foreignDSN := directoryActivationTestDSNForDatabase(
			t, primary.pg.Admin, foreign.pg.Database,
		)
		alternate, err := openPGPinnedToEngineSchema(foreignDSN, 2)
		if err != nil {
			t.Fatalf("open pinned-role foreign-database AdminDSN: %v", err)
		}
		defer alternate.Close() //nolint:errcheck

		goodAdmin := primary.sql.adminDB
		primary.sql.adminDB = alternate
		_, retireErr := RetireUser(ctx, primary.raw, retirementPostgresUserRequest(user))
		primary.sql.adminDB = goodAdmin

		if !errors.Is(retireErr, store.ErrEnumerationNotAuthoritative) ||
			!strings.Contains(retireErr.Error(), "does not address the owner database") {
			t.Fatalf(
				"RetireUser with a live foreign database under the pinned role = %v, "+
					"want the typed identity refusal", retireErr,
			)
		}
		// A refusal that still mutated would be worse than no refusal, and only the
		// stored rows separate the two.
		if got := directoryWriterTestEpoch(t, primary.raw, tenant).Version; got != beforeEpoch {
			t.Fatalf("live foreign-database refusal epoch = %d, want %d", got, beforeEpoch)
		}
		if got := retirementPostgresRowCount(
			t, primary.sql, userDescriptor.Table,
			"tenant_id = ? AND id = ?", model.SystemTenantID.String(), user.ID.String(),
		); got != 1 {
			t.Fatalf("live foreign-database refusal User rows = %d, want 1", got)
		}
	})

	// ⛔ ACTA — ESTE SUBTEST CAMBIÓ DE PUNTO, NO DE PROPIEDAD.
	//
	// Antes exigía que el reopen con un AdminDSN ajeno TUVIERA ÉXITO y que el refuso
	// tipado llegara en RetireUser. Contrastado con Codex sol max el 2026-08-20
	// (an internal design note (not shipped)), gana el
	// contrato contrario, y no por recuento de tests:
	//
	//   · La spec decidida concede el testigo incompleto SÓLO a "sin AdminDSN" y exige
	//     rollback total ante cualquier fallo del reconciliador
	//     (an internal design note (not shipped):184-193).
	//   · Un AdminDSN cuya identidad de base ha sido REFUTADA no es evidencia ausente:
	//     es configuración demostrada falsa. El reto compara nombre de base y exclusión
	//     mutua de un advisory lock, así que hay prueba, no duda.
	//   · Y degradar un pool ya probado ajeno CONSERVÁNDOLO dentro del store deja que
	//     otros consumidores lo usen sin repetir la prueba de identidad. Eso es el
	//     defecto, y es de seguridad.
	//
	// LA PROPIEDAD QUE ESTE SUBTEST FIJABA SE CONSERVA ENTERA: que un AdminDSN ajeno no
	// mute nada. Sólo cambia dónde se observa el refuso, y la no-mutación se comprueba
	// reabriendo después con el admin correcto — que además prueba que la base quedó
	// utilizable, cosa que la versión anterior no comprobaba.
	//
	// Y EL PUNTO CIEGO QUE ESTE CAMBIO ABRE YA ESTÁ CUBIERTO, deliberadamente antes de
	// tocar esta línea: al no llegar a RetireUser, este caso deja de ejercitar la
	// re-atestación EN VIVO de identidad. La cubre el subtest
	// "live foreign database with the pinned role is refused before mutation" de más
	// arriba, escrito primero y verificado por mutación.
	t.Run("foreign database is refused at reopen after enforcement", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		primary := newDirectoryRetirementPostgresHarness(t)
		foreign := newDirectoryRetirementPostgresHarness(t)
		tenant := provisionTenant(t, primary.raw, "retirement-pg-admin-foreign")
		user := retirementCreateUser(t, primary.raw, "retirement-pg-admin-foreign")
		primary.enforce(t)
		beforeEpoch := directoryWriterTestEpoch(t, primary.raw, tenant).Version
		if err := primary.raw.Close(); err != nil {
			t.Fatalf("close before foreign AdminDSN reopen: %v", err)
		}

		badCfg := primary.cfg
		badCfg.AdminDSN = foreign.pg.Admin
		openCtx, openCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer openCancel()
		reopened, openErr := Open(openCtx, badCfg, nil)
		if openErr == nil {
			_ = reopened.Close()
			t.Fatal("reopen accepted an AdminDSN addressing a foreign database")
		}
		if !errors.Is(openErr, store.ErrEnumerationNotAuthoritative) {
			t.Fatalf(
				"foreign AdminDSN reopen = %v, want the typed refusal "+
					"store.ErrEnumerationNotAuthoritative — a boot that refuses without "+
					"its type cannot be told from any other startup failure", openErr,
			)
		}
		if !strings.Contains(openErr.Error(), "does not address the owner database") {
			t.Fatalf("the refusal does not name the measured cause: %v", openErr)
		}

		// Non-mutation, checked through a store opened with the CORRECT admin: the
		// refused boot must have left both the epoch and the User untouched, and the
		// database must still be usable.
		good := retirementOpenPostgres(t, primary.cfg)
		if got := directoryWriterTestEpoch(t, good, tenant).Version; got != beforeEpoch {
			t.Fatalf("foreign AdminDSN refusal epoch = %d, want %d", got, beforeEpoch)
		}
		if err := good.AuthView(ctx, func(auth store.AuthScope) error {
			got, getErr := auth.Users().Get(ctx, user.ID)
			if getErr != nil {
				return getErr
			}
			if got.ID != user.ID || got.Version != user.Version {
				return fmt.Errorf("foreign AdminDSN refusal User = %+v, want %+v", got, user)
			}
			return nil
		}); err != nil {
			t.Fatalf("foreign AdminDSN refusal rewrote User: %v", err)
		}
	})

	t.Run("write capable admin closure is refused live", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		h := newDirectoryRetirementPostgresHarness(t)
		tenant := provisionTenant(t, h.raw, "retirement-pg-admin-writable")
		user := retirementCreateUser(t, h.raw, "retirement-pg-admin-writable")
		h.enforce(t)
		beforeEpoch := directoryWriterTestEpoch(t, h.raw, tenant).Version
		if err := h.raw.Close(); err != nil {
			t.Fatalf("close before writable AdminDSN reopen: %v", err)
		}
		owner, err := sql.Open("pgx", h.pg.Owner)
		if err != nil {
			t.Fatalf("open owner for writable AdminDSN grant: %v", err)
		}
		defer owner.Close() //nolint:errcheck
		if _, err := owner.ExecContext(ctx,
			"GRANT INSERT ON TABLE public."+quoteIdent(directoryTombstoneDescriptor.Table)+
				" TO "+quoteIdent(h.pg.Result.AdminPosture.Role)); err != nil {
			t.Fatalf("grant AdminDSN write authority: %v", err)
		}
		reopened := retirementOpenPostgres(t, h.cfg)
		_, retireErr := RetireUser(ctx, reopened, retirementPostgresUserRequest(user))
		if !errors.Is(retireErr, store.ErrEnumerationNotAuthoritative) ||
			!strings.Contains(retireErr.Error(), "not read-only") {
			t.Fatalf("RetireUser with writable AdminDSN closure = %v", retireErr)
		}
		if got := directoryWriterTestEpoch(t, reopened, tenant).Version; got != beforeEpoch {
			t.Fatalf("writable AdminDSN refusal epoch = %d, want %d", got, beforeEpoch)
		}
		if got := retirementPostgresRowCount(
			t, reopened.(*sqlStore), userDescriptor.Table,
			"tenant_id = ? AND id = ?", model.SystemTenantID.String(), user.ID.String(),
		); got != 1 {
			t.Fatalf("writable AdminDSN refusal User rows = %d, want 1", got)
		}
	})
}

func TestRetireUserPostgresSplitOwnerRLSReplayRejectsResidualAuthority(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	h := newDirectoryRetirementPostgresHarness(t)
	provisionTenant(t, h.raw, "retirement-pg-replay-rls-a")
	provisionTenant(t, h.raw, "retirement-pg-replay-rls-b")
	user := retirementCreateUser(t, h.raw, "retirement-pg-replay-rls")
	h.enforce(t)
	req := retirementPostgresUserRequest(user)
	if _, err := RetireUser(ctx, h.raw, req); err != nil {
		t.Fatalf("initial PostgreSQL User retirement: %v", err)
	}

	// Deliberately bypass the public authority wrapper while retaining the real
	// System tenant pin and FORCE-RLS path. This models a legacy/corrupt residual
	// after the commit. Replay must rebind System after reading every tenant
	// witness; otherwise the last business-tenant RLS binding hides this row.
	var residual model.AuthSession
	if err := h.raw.AuthMutate(ctx, func(auth store.AuthScope) error {
		raw := auth.(*authScope).ts
		var createErr error
		residual, createErr = newTypedRepo(
			raw.repo(authSessionDescriptor), authSessionCodec,
		).Create(ctx, model.AuthSession{
			UserID: user.ID, Selector: "pg-retirement-residual-session",
			SecretHash: []byte("residual-hash"),
			ExpiresAt:  model.NewTimestamp(time.Now().UTC().Add(time.Hour)),
		})
		return createErr
	}); err != nil {
		t.Fatalf("seed raw PostgreSQL residual authority: %v", err)
	}

	_, err := RetireUser(ctx, h.raw, req)
	if !errors.Is(err, store.ErrDirectoryUnavailable) ||
		!errors.Is(err, store.ErrDirectoryRetirementResidualAuthority) {
		t.Fatalf("PostgreSQL User replay with RLS-hidden residual = %v", err)
	}
	if got := retirementPostgresRowCount(
		t, h.sql, authSessionDescriptor.Table,
		"tenant_id = ? AND id = ?", model.SystemTenantID.String(), residual.ID.String(),
	); got != 1 {
		t.Fatalf("failed replay rewrote residual authority rows = %d, want 1", got)
	}
}

func TestRetireUserPostgresSplitOwnerGlobalInterleavings(t *testing.T) {
	t.Run("retirement wins and stale authority writer is refused", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		h := newDirectoryRetirementPostgresHarness(t)
		provisionTenant(t, h.raw, "retirement-pg-global-retire-first")
		user := retirementCreateUser(t, h.raw, "retirement-pg-global-retire-first")
		h.enforce(t)
		auditReached := make(chan struct{})
		releaseRetirement := make(chan struct{})
		directoryRetirementAfterAuditTestHook = func(*model.AuditEvent) {
			close(auditReached)
			<-releaseRetirement
		}
		t.Cleanup(func() { directoryRetirementAfterAuditTestHook = nil })
		retireDone := make(chan error, 1)
		go func() {
			_, retireErr := RetireUser(ctx, h.raw, retirementPostgresUserRequest(user))
			retireDone <- retireErr
		}()
		select {
		case <-auditReached:
		case <-ctx.Done():
			t.Fatalf("PostgreSQL retirement did not reach held audit: %v", ctx.Err())
		}
		issuerDone := make(chan error, 1)
		go func() {
			issuerDone <- h.raw.AuthMutate(ctx, func(auth store.AuthScope) error {
				_, createErr := auth.Sessions().Create(ctx, model.AuthSession{
					UserID: user.ID, Selector: "pg-stale-authority-session",
					SecretHash: []byte("hash"),
					ExpiresAt:  model.NewTimestamp(time.Now().UTC().Add(time.Hour)),
				})
				return createErr
			})
		}()
		waitCtx, waitCancel := context.WithTimeout(ctx, 5*time.Second)
		waitErr := retirementPostgresWaitForAdvisoryWaiter(
			waitCtx, h.pg.Superuser, h.pg.Database,
		)
		waitCancel()
		if waitErr != nil {
			// Release and drain both goroutines before failing so the package-global
			// retirement hook cannot strand an isolated database during cleanup.
			close(releaseRetirement)
			retireErr := <-retireDone
			issuerErr := <-issuerDone
			t.Fatalf(
				"PostgreSQL authority writer never became an advisory-lock waiter: %v (retire=%v issuer=%v)",
				waitErr, retireErr, issuerErr,
			)
		}
		close(releaseRetirement)
		if err := <-retireDone; err != nil {
			t.Fatalf("PostgreSQL retirement winner: %v", err)
		}
		select {
		case err := <-issuerDone:
			if !errors.Is(err, store.ErrDirectoryPrincipalRetired) {
				t.Fatalf("stale PostgreSQL authority writer = %v, want retired", err)
			}
		case <-ctx.Done():
			t.Fatalf("stale PostgreSQL authority writer deadlocked: %v", ctx.Err())
		}
		if got := retirementPostgresRowCount(
			t, h.sql, authSessionDescriptor.Table,
			"tenant_id = ? AND user_id = ?", model.SystemTenantID.String(), user.ID.String(),
		); got != 0 {
			t.Fatalf("retirement-first PostgreSQL sessions = %d, want 0", got)
		}
	})

	t.Run("membership wins and retirement removes committed authority", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		h := newDirectoryRetirementPostgresHarness(t)
		tenant := provisionTenant(t, h.raw, "retirement-pg-global-writer-first")
		user := retirementCreateUser(t, h.raw, "retirement-pg-global-writer-first")
		h.enforce(t)
		beforeEpoch := directoryWriterTestEpoch(t, h.raw, tenant).Version
		writerLocked := make(chan struct{})
		releaseWriter := make(chan struct{})
		var once sync.Once
		directoryWriterBeforeSourceTestHook = func(
			context.Context,
			*directoryWriteTracker,
		) error {
			once.Do(func() {
				close(writerLocked)
				<-releaseWriter
			})
			return nil
		}
		t.Cleanup(func() { directoryWriterBeforeSourceTestHook = nil })
		writerDone := make(chan error, 1)
		go func() {
			writerDone <- h.raw.AuthMutate(ctx, func(auth store.AuthScope) error {
				_, createErr := auth.Memberships().Create(ctx, model.Membership{
					UserID: user.ID, TargetTenantID: tenant, Role: "viewer",
				})
				return createErr
			})
		}()
		select {
		case <-writerLocked:
		case <-ctx.Done():
			t.Fatalf("PostgreSQL Membership writer did not acquire global lock: %v", ctx.Err())
		}
		retireDone := make(chan error, 1)
		go func() {
			_, retireErr := RetireUser(ctx, h.raw, retirementPostgresUserRequest(user))
			retireDone <- retireErr
		}()
		select {
		case early := <-retireDone:
			t.Fatalf("PostgreSQL retirement crossed held Membership writer: %v", early)
		case <-time.After(150 * time.Millisecond):
		}
		close(releaseWriter)
		select {
		case err := <-writerDone:
			if err != nil {
				t.Fatalf("PostgreSQL Membership winner: %v", err)
			}
		case <-ctx.Done():
			t.Fatalf("PostgreSQL Membership writer deadlocked: %v", ctx.Err())
		}
		select {
		case err := <-retireDone:
			if err != nil {
				t.Fatalf("PostgreSQL retirement after Membership commit: %v", err)
			}
		case <-ctx.Done():
			t.Fatalf("PostgreSQL retirement after Membership deadlocked: %v", ctx.Err())
		}
		if got := retirementPostgresRowCount(
			t, h.sql, membershipDescriptor.Table,
			"tenant_id = ? AND user_id = ?", model.SystemTenantID.String(), user.ID.String(),
		); got != 0 {
			t.Fatalf("Membership-first PostgreSQL residual rows = %d, want 0", got)
		}
		if got := directoryWriterTestEpoch(t, h.raw, tenant).Version; got != beforeEpoch+2 {
			t.Fatalf("Membership-first PostgreSQL epoch = %d, want %d", got, beforeEpoch+2)
		}
	})
}

func TestDirectoryRetirementPostgresSplitOwnerAuditFailureAndRollback(t *testing.T) {
	t.Run("forced User rollback restores complete tuple", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		h := newDirectoryRetirementPostgresHarness(t)
		tenantA := provisionTenant(t, h.raw, "retirement-pg-rollback-a")
		tenantB := provisionTenant(t, h.raw, "retirement-pg-rollback-b")
		victim, err := directoryEpochTestSeedRetainedAuth(ctx, h.raw, "retirement-pg-rollback")
		if err != nil {
			t.Fatalf("seed PostgreSQL rollback User: %v", err)
		}
		estate, err := directoryEpochTestSeedAuthEstate(
			ctx, h.raw, tenantA, "retirement-pg-rollback-estate", victim.user.ID,
		)
		if err != nil {
			t.Fatalf("seed PostgreSQL rollback authority: %v", err)
		}
		h.enforce(t)
		beforeA := directoryWriterTestEpoch(t, h.raw, tenantA).Version
		beforeB := directoryWriterTestEpoch(t, h.raw, tenantB).Version
		beforeAudit := retirementPostgresRowCount(
			t, h.sql, auditTable,
			"tenant_id = ?", model.SystemTenantID.String(),
		)
		forced := errors.New("forced PostgreSQL rollback after complete retirement tuple")
		directoryRetirementBeforeFinishTestHook = func(
			kind model.DirectoryPrincipalKind,
			id model.ID,
		) error {
			if kind != model.DirectoryPrincipalUser || id != victim.user.ID {
				return fmt.Errorf("unexpected PostgreSQL rollback hook target %s/%s", kind, id)
			}
			return forced
		}
		t.Cleanup(func() { directoryRetirementBeforeFinishTestHook = nil })
		_, err = RetireUser(ctx, h.raw, retirementPostgresUserRequest(victim.user))
		if !errors.Is(err, forced) {
			t.Fatalf("forced PostgreSQL User rollback = %v", err)
		}
		if got := directoryWriterTestEpoch(t, h.raw, tenantA).Version; got != beforeA {
			t.Fatalf("PostgreSQL rollback tenant A epoch = %d, want %d", got, beforeA)
		}
		if got := directoryWriterTestEpoch(t, h.raw, tenantB).Version; got != beforeB {
			t.Fatalf("PostgreSQL rollback tenant B epoch = %d, want %d", got, beforeB)
		}
		for table, id := range map[string]model.ID{
			userDescriptor.Table:             victim.user.ID,
			authSessionDescriptor.Table:      victim.session.ID,
			apiTokenDescriptor.Table:         estate.token.ID,
			membershipDescriptor.Table:       estate.membership.ID,
			delegationHandleDescriptor.Table: estate.handle.ID,
		} {
			if got := retirementPostgresRowCount(
				t, h.sql, table, "tenant_id = ? AND id = ?",
				model.SystemTenantID.String(), id.String(),
			); got != 1 {
				t.Fatalf("PostgreSQL rollback %s rows = %d, want 1", table, got)
			}
		}
		if got := retirementPostgresRowCount(
			t, h.sql, userTombstoneDescriptor.Table,
			"tenant_id = ? AND source_id = ?",
			model.SystemTenantID.String(), victim.user.ID.String(),
		); got != 0 {
			t.Fatalf("PostgreSQL rollback User tombstones = %d, want 0", got)
		}
		if got := retirementPostgresRowCount(
			t, h.sql, auditTable,
			"tenant_id = ?", model.SystemTenantID.String(),
		); got != beforeAudit {
			t.Fatalf("PostgreSQL rollback audit rows = %d, want %d", got, beforeAudit)
		}
	})

	t.Run("degraded audit cannot commit Identity retirement", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		h := newDirectoryRetirementPostgresHarness(t)
		tenant := provisionTenant(t, h.raw, "retirement-pg-audit-degrade")
		identity := retirementCreateIdentity(t, h.raw, tenant, "retirement-pg-audit-degrade")
		h.enforce(t)
		beforeEpoch := directoryWriterTestEpoch(t, h.raw, tenant).Version
		beforeAudit := retirementPostgresRowCount(
			t, h.sql, auditTable, "tenant_id = ?", tenant.String(),
		)
		if err := h.raw.Close(); err != nil {
			t.Fatalf("close before degraded PostgreSQL reopen: %v", err)
		}
		degradedCfg := h.cfg
		degradedCfg.AuditSpoolMaxBytes = 1
		degradedCfg.AuditSpoolOnFull = store.AuditSpoolDegrade
		degraded := retirementOpenPostgres(t, degradedCfg)
		_, err := RetireDirectoryPrincipal(ctx, degraded,
			retirementPostgresPrincipalRequest(
				tenant, model.DirectoryPrincipalIdentity, identity.ID, identity.Version,
			))
		if !errors.Is(err, store.ErrAuditSpoolFull) {
			t.Fatalf("degraded PostgreSQL Identity retirement = %v", err)
		}
		degradedSQL := degraded.(*sqlStore)
		if got := directoryWriterTestEpoch(t, degraded, tenant).Version; got != beforeEpoch {
			t.Fatalf("degraded PostgreSQL epoch = %d, want %d", got, beforeEpoch)
		}
		if got := retirementPostgresRowCount(
			t, degradedSQL, identityDescriptor.Table,
			"tenant_id = ? AND id = ?", tenant.String(), identity.ID.String(),
		); got != 1 {
			t.Fatalf("degraded PostgreSQL Identity rows = %d, want 1", got)
		}
		if got := retirementPostgresRowCount(
			t, degradedSQL, directoryTombstoneDescriptor.Table,
			"tenant_id = ? AND source_id = ?", tenant.String(), identity.ID.String(),
		); got != 0 {
			t.Fatalf("degraded PostgreSQL tombstones = %d, want 0", got)
		}
		if got := retirementPostgresRowCount(
			t, degradedSQL, auditTable, "tenant_id = ?", tenant.String(),
		); got != beforeAudit {
			t.Fatalf("degraded PostgreSQL audit rows = %d, want %d", got, beforeAudit)
		}
	})
}
