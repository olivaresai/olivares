// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package inventory

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/url"
	"reflect"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/engine/enginetest"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/sdk/event"
)

func c1PostgresFixture(t *testing.T) (store.Config, enginetest.DSNs) {
	t.Helper()
	if !enginetest.PostgresAvailable(t) {
		t.Fatalf("%s unset: the required PostgreSQL C1 leg is NOT RUN", enginetest.EnvSuperuserDSN)
	}
	pg := enginetest.IsolatedPostgres(t)
	t.Logf("C1 PostgreSQL fixture database=%s engine=postgres app_role=NOSUPERUSER/NOBYPASSRLS", pg.Database)
	return store.Config{
		Engine: store.EnginePostgres, DSN: pg.App, OwnerDSN: pg.Owner, AdminDSN: pg.Admin,
		MaxConns: 8, Debug: true,
	}, pg
}

func c1PostgresConfig(t *testing.T) store.Config {
	t.Helper()
	if !enginetest.PostgresAvailable(t) {
		t.Fatalf("%s unset: the required PostgreSQL C1 leg is NOT RUN", enginetest.EnvSuperuserDSN)
	}
	pg := enginetest.IsolatedPostgres(t)
	t.Logf("C1 PostgreSQL fixture database=%s engine=postgres app_role=NOSUPERUSER/NOBYPASSRLS", pg.Database)
	return store.Config{
		Engine: store.EnginePostgres, DSN: pg.App, OwnerDSN: pg.Owner, AdminDSN: pg.Admin,
		MaxConns: 8, Debug: true,
	}
}

func c1PostgresOpen(t *testing.T, cfg store.Config, legacy bool) (*Module, store.Store) {
	t.Helper()
	m := New(WithClock(pinnedClock{at: baseTime.Add(time.Hour)}))
	register := m.RegisterSchema
	if legacy {
		register = func(reg store.ExtensionRegistry) error { return m.RegisterSchema(c1CatalogRegistry{reg}) }
	}
	st, err := engine.Open(context.Background(), cfg, register)
	if err != nil {
		t.Fatalf("open PostgreSQL inventory store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	m.UseData(api.NewModuleData(st))
	return m, st
}

type c1PostgresMember struct {
	ID, ReceiptID, ObservationKey, SourceID, EntityKind, EntityID, Facts string
	Ordinal                                                              int64
}

func c1PostgresMembers(t *testing.T, st store.Store, tenant model.TenantID) []c1PostgresMember {
	t.Helper()
	rows := c1Rows(t, st, tenant, observationMemberKind)
	out := make([]c1PostgresMember, 0, len(rows))
	for _, row := range rows {
		out = append(out, c1PostgresMember{
			ID: row.String(model.ColID), ReceiptID: row.String(colReceiptID),
			ObservationKey: row.String(colObservationKey), SourceID: row.String(colSourceID),
			EntityKind: row.String(colEntityKind), EntityID: row.String(colEntityID),
			Facts: row.String(colFacts), Ordinal: row.Int(colMemberOrdinal),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ReceiptID != out[j].ReceiptID {
			return out[i].ReceiptID < out[j].ReceiptID
		}
		return out[i].Ordinal < out[j].Ordinal
	})
	return out
}

type c1PostgresReceipt struct {
	ID, Key, EventID, FactsHash, Facts string
	Deliveries, MemberCount            int64
}

func c1PostgresReceiptState(t *testing.T, st store.Store, tenant model.TenantID, eventID string) c1PostgresReceipt {
	t.Helper()
	r := c1Receipt(t, st, tenant, eventID)
	return c1PostgresReceipt{
		ID: r.String(model.ColID), Key: r.String(colReceiptKey), EventID: r.String(colEventID),
		FactsHash: r.String(colFactsHash), Facts: r.String(colFacts),
		Deliveries: r.Int(colDeliveries), MemberCount: r.Int(colMemberCount),
	}
}

func c1PostgresCoreIDs(t *testing.T, st store.Store, tenant model.TenantID) (agents, resources []string) {
	t.Helper()
	if err := st.View(context.Background(), tenant, func(sc store.Scope) error {
		a, _, err := sc.Agents().List(context.Background(), model.Query{Limit: 16})
		if err != nil {
			return err
		}
		r, _, err := sc.Resources().List(context.Background(), model.Query{Limit: 16})
		if err != nil {
			return err
		}
		for _, row := range a {
			agents = append(agents, row.ID.String())
		}
		for _, row := range r {
			resources = append(resources, row.ID.String())
		}
		return nil
	}); err != nil {
		t.Fatalf("read PostgreSQL core aliases: %v", err)
	}
	sort.Strings(agents)
	sort.Strings(resources)
	return agents, resources
}

func c1PostgresCatalogRow(t *testing.T, st store.Store, tenant model.TenantID, kind string) model.Record {
	t.Helper()
	var found []model.Record
	for _, row := range c1Rows(t, st, tenant, catalogEntryKind) {
		if row.String(colEntityKind) == kind {
			found = append(found, row)
		}
	}
	if len(found) != 1 {
		t.Fatalf("PostgreSQL catalog kind %s rows=%d, want 1", kind, len(found))
	}
	return found[0]
}

func c1PostgresAssertCompleteReceipt(t *testing.T, st store.Store, tenant model.TenantID, eventID string, wantMembers int) model.Record {
	t.Helper()
	r := c1Receipt(t, st, tenant, eventID)
	if got := r.Int(colMemberCount); got != int64(wantMembers) {
		t.Fatalf("receipt %s member_count=%d, want %d", eventID, got, wantMembers)
	}
	seen := map[int64]bool{}
	for _, member := range c1Rows(t, st, tenant, observationMemberKind) {
		if member.String(colReceiptID) != r.String(model.ColID) {
			continue
		}
		seen[member.Int(colMemberOrdinal)] = true
	}
	if len(seen) != wantMembers {
		t.Fatalf("receipt %s has ordinals %v, want %d complete members", eventID, seen, wantMembers)
	}
	for i := range wantMembers {
		if !seen[int64(i)] {
			t.Fatalf("receipt %s lacks member ordinal %d", eventID, i)
		}
	}
	return r
}

func c1PostgresWriteLegacyAgent(t *testing.T, m *Module, st store.Store, tenant model.TenantID) model.ID {
	t.Helper()
	var alias model.ID
	if err := st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		var err error
		alias, err = foAgent(context.Background(), sc, "shared-external")
		if err != nil {
			return err
		}
		return m.upsertCatalogEntry(context.Background(), sc, kindAgent, alias,
			"shared-external", "shared-external", "legacy-signal", "", baseTime, time.Time{})
	}); err != nil {
		t.Fatalf("legacy PostgreSQL catalog write: %v", err)
	}
	return alias
}

func TestC1PostgresTenantSourceIdentityUpgradeAndReopen(t *testing.T) {
	cfg := c1PostgresConfig(t)
	legacy, st := c1PostgresOpen(t, cfg, true)
	tenant1 := c1Tenant(t, st, "c1-pg-tenant-one")
	legacyAlias := c1PostgresWriteLegacyAgent(t, legacy, st, tenant1)
	legacyCatalog := c1PostgresCatalogRow(t, st, tenant1, kindAgent)
	var legacyDescriptor model.EntityDescriptor
	if err := st.View(context.Background(), tenant1, func(sc store.Scope) error {
		repo, err := sc.Ext(catalogEntryKind)
		if err == nil {
			legacyDescriptor = repo.Descriptor()
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	m, st := c1PostgresOpen(t, cfg, false)
	if len(c1Rows(t, st, tenant1, observationReceiptKind)) != 0 {
		t.Fatal("schema expansion invented historical provenance")
	}
	upgradedCatalog := c1PostgresCatalogRow(t, st, tenant1, kindAgent)
	if upgradedCatalog.String(model.ColID) != legacyCatalog.String(model.ColID) ||
		upgradedCatalog.String(colEntityID) != legacyAlias.String() {
		t.Fatal("schema expansion changed the legacy catalog identity")
	}
	if err := st.View(context.Background(), tenant1, func(sc store.Scope) error {
		repo, err := sc.Ext(catalogEntryKind)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(repo.Descriptor(), legacyDescriptor) {
			t.Fatal("C1 changed the catalog descriptor seen by the legacy scope")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	tenant2 := c1Tenant(t, st, "c1-pg-tenant-two")
	eventA := c1Event(tenant1, "shared-event", "source-a")
	eventB := c1Event(tenant1, "tenant-one-source-b", "source-b")
	eventOther := c1Event(tenant2, "shared-event", "source-a")
	for _, e := range []event.Event{eventA, eventB, eventOther} {
		if err := m.onEvent(context.Background(), e); err != nil {
			t.Fatal(err)
		}
	}
	if len(c1Rows(t, st, tenant1, observationReceiptKind)) != 2 || len(c1Rows(t, st, tenant2, observationReceiptKind)) != 1 {
		t.Fatal("tenant-scoped receipt counts diverged")
	}
	c1PostgresAssertCompleteReceipt(t, st, tenant1, eventA.ID, 2)
	c1PostgresAssertCompleteReceipt(t, st, tenant1, eventB.ID, 2)
	c1PostgresAssertCompleteReceipt(t, st, tenant2, eventOther.ID, 2)

	tenant1AgentKeys := map[string]string{}
	for _, row := range c1Rows(t, st, tenant1, observationMemberKind) {
		if row.String(colEntityKind) != kindAgent {
			continue
		}
		var member observationMember
		if err := json.Unmarshal([]byte(row.String(colFacts)), &member); err != nil {
			t.Fatal(err)
		}
		if member.Native.Namespace != "agent" || member.Native.Ref != "shared-external" || row.String(colObservationKey) == "" {
			t.Fatalf("tenant-one agent provenance lost: row=%v native=%+v", row, member.Native)
		}
		tenant1AgentKeys[row.String(colSourceID)] = row.String(colObservationKey)
		if row.String(colEntityID) != legacyAlias.String() {
			t.Fatal("C1 forked the existing legacy alias")
		}
	}
	if len(tenant1AgentKeys) != 2 || tenant1AgentKeys["source-a"] == tenant1AgentKeys["source-b"] {
		t.Fatalf("source-qualified identities=%v", tenant1AgentKeys)
	}
	otherMembers := c1PostgresMembers(t, st, tenant2)
	if len(otherMembers) != 2 {
		t.Fatalf("tenant-two members=%d, want 2", len(otherMembers))
	}
	for _, member := range otherMembers {
		if member.SourceID != "source-a" || member.ObservationKey == "" {
			t.Fatalf("tenant-two provenance=%+v", member)
		}
		if member.EntityKind == kindAgent && member.ObservationKey == tenant1AgentKeys["source-a"] {
			t.Fatal("observation identity crossed tenants")
		}
	}
	agents1, _ := c1PostgresCoreIDs(t, st, tenant1)
	agents2, _ := c1PostgresCoreIDs(t, st, tenant2)
	if len(agents1) != 1 || len(agents2) != 1 || agents1[0] == agents2[0] {
		t.Fatalf("tenant core aliases tenant1=%v tenant2=%v", agents1, agents2)
	}
	members1, members2 := c1PostgresMembers(t, st, tenant1), c1PostgresMembers(t, st, tenant2)
	receiptA := c1PostgresReceiptState(t, st, tenant1, eventA.ID)
	receiptB := c1PostgresReceiptState(t, st, tenant1, eventB.ID)
	receiptOther := c1PostgresReceiptState(t, st, tenant2, eventOther.ID)
	if receiptA.ID == receiptOther.ID || receiptA.Key != receiptOther.Key {
		t.Fatal("same event ID should have a tenant-local receipt ID and stable event key")
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	legacy, st = c1PostgresOpen(t, cfg, true)
	if err := st.View(context.Background(), tenant1, func(sc store.Scope) error {
		_, err := sc.Ext(observationReceiptKind)
		if !errors.Is(err, store.ErrUnknownEntity) {
			t.Fatalf("legacy descriptor exposed C1 receipt scope: %v", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if got := c1PostgresWriteLegacyAgent(t, legacy, st, tenant1); got != legacyAlias {
		t.Fatal("legacy writer changed alias after C1 expansion")
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	_, st = c1PostgresOpen(t, cfg, false)
	if !reflect.DeepEqual(c1PostgresMembers(t, st, tenant1), members1) ||
		!reflect.DeepEqual(c1PostgresMembers(t, st, tenant2), members2) ||
		c1PostgresReceiptState(t, st, tenant1, eventA.ID) != receiptA ||
		c1PostgresReceiptState(t, st, tenant1, eventB.ID) != receiptB ||
		c1PostgresReceiptState(t, st, tenant2, eventOther.ID) != receiptOther {
		t.Fatal("reopen or legacy writer changed committed receipt/member provenance")
	}
	if got := c1PostgresCatalogRow(t, st, tenant1, kindAgent); got.String(model.ColID) != legacyCatalog.String(model.ColID) || got.Int(colOccurrence) != 4 {
		t.Fatalf("legacy catalog after reopen=%v, want same ID and four deliveries", got)
	}
}

func TestC1PostgresAtomicRollbackReplayAndConflict(t *testing.T) {
	cfg := c1PostgresConfig(t)
	m, st := c1PostgresOpen(t, cfg, false)
	tenant := c1Tenant(t, st, "c1-pg-atomic")
	data := api.NewModuleData(st)
	failing := &c1FailData{ModuleData: data}
	m.UseData(failing)
	e := c1Event(tenant, "atomic-pg", "source-a")
	if err := m.onEvent(context.Background(), e); !errors.Is(err, c1WriteFailure) {
		t.Fatalf("injected second-member failure=%v, want %v", err, c1WriteFailure)
	}
	if failing.creates != 2 {
		t.Fatalf("member creates=%d, want failure at second Create", failing.creates)
	}
	for _, kind := range []model.Kind{catalogEntryKind, observationReceiptKind, observationMemberKind, observationConflictKind} {
		if rows := c1Rows(t, st, tenant, kind); len(rows) != 0 {
			t.Fatalf("%s left %d rows after rollback", kind, len(rows))
		}
	}
	if agents, resources := c1PostgresCoreIDs(t, st, tenant); len(agents) != 0 || len(resources) != 0 {
		t.Fatalf("core aliases escaped rollback: agents=%v resources=%v", agents, resources)
	}

	m.UseData(data)
	if err := m.onEvent(context.Background(), e); err != nil {
		t.Fatalf("manual redelivery after rollback: %v", err)
	}
	c1PostgresAssertCompleteReceipt(t, st, tenant, e.ID, 2)
	originalReceipt := c1PostgresReceiptState(t, st, tenant, e.ID)
	originalMembers := c1PostgresMembers(t, st, tenant)
	for _, member := range originalMembers {
		if member.SourceID != "source-a" || member.ObservationKey == "" {
			t.Fatalf("redelivery lost source identity: %+v", member)
		}
	}
	if err := m.onEvent(context.Background(), e); err != nil {
		t.Fatalf("exact replay: %v", err)
	}
	replayed := c1PostgresReceiptState(t, st, tenant, e.ID)
	if replayed.ID != originalReceipt.ID || replayed.Key != originalReceipt.Key ||
		replayed.FactsHash != originalReceipt.FactsHash || replayed.Facts != originalReceipt.Facts ||
		replayed.MemberCount != originalReceipt.MemberCount || replayed.Deliveries != 2 {
		t.Fatalf("exact replay changed receipt: before=%+v after=%+v", originalReceipt, replayed)
	}
	if !reflect.DeepEqual(c1PostgresMembers(t, st, tenant), originalMembers) {
		t.Fatal("exact replay changed source/member identity")
	}
	for _, kind := range []string{kindAgent, kindResource} {
		if got := c1PostgresCatalogRow(t, st, tenant, kind).Int(colOccurrence); got != 2 {
			t.Fatalf("legacy catalog %s deliveries=%d, want 2", kind, got)
		}
	}

	conflicting := e
	edge, ok := event.EdgeOf(e)
	if !ok {
		t.Fatal("fixture event has no edge")
	}
	edge.OriginRef = "conflicting-agent"
	conflicting.Payload = edge
	if err := m.onEvent(context.Background(), conflicting); !errors.Is(err, ErrObservationReceiptConflict) {
		t.Fatalf("conflicting payload=%v, want ErrObservationReceiptConflict", err)
	}
	if c1PostgresReceiptState(t, st, tenant, e.ID) != replayed ||
		!reflect.DeepEqual(c1PostgresMembers(t, st, tenant), originalMembers) {
		t.Fatal("conflict replaced the original receipt or members")
	}
	if len(c1Rows(t, st, tenant, observationConflictKind)) != 1 {
		t.Fatal("conflicting facts were not retained separately")
	}
	if agents, resources := c1PostgresCoreIDs(t, st, tenant); len(agents) != 1 || len(resources) != 1 {
		t.Fatalf("conflict created an alias: agents=%v resources=%v", agents, resources)
	}
}

type c1FirstInsertHold struct {
	api.ModuleData
	once     sync.Once
	observed chan struct{}
	release  chan struct{}
}

type c1FirstInsertHoldScope struct {
	store.Scope
	hold *c1FirstInsertHold
}

type c1FirstInsertHoldRepo struct {
	store.GenericRepo
	hold *c1FirstInsertHold
}

func (d *c1FirstInsertHold) Mutate(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	return d.ModuleData.Mutate(ctx, tenant, func(sc store.Scope) error {
		return fn(c1FirstInsertHoldScope{Scope: sc, hold: d})
	})
}

func (s c1FirstInsertHoldScope) Ext(kind model.Kind) (store.GenericRepo, error) {
	repo, err := s.Scope.Ext(kind)
	if err == nil && kind == observationReceiptKind {
		return c1FirstInsertHoldRepo{GenericRepo: repo, hold: s.hold}, nil
	}
	return repo, err
}

func (r c1FirstInsertHoldRepo) List(ctx context.Context, q model.Query) ([]model.Record, model.Page, error) {
	rows, page, err := r.GenericRepo.List(ctx, q)
	if err != nil || len(rows) != 0 {
		return rows, page, err
	}
	r.hold.once.Do(func() { close(r.hold.observed) })
	select {
	case <-r.hold.release:
		return rows, page, nil
	case <-ctx.Done():
		return nil, model.Page{}, ctx.Err()
	}
}

type c1MutateProbe struct {
	api.ModuleData
	once    sync.Once
	started chan struct{}
	mu      sync.Mutex
	entries int
}

func (d *c1MutateProbe) Mutate(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	d.once.Do(func() { close(d.started) })
	return d.ModuleData.Mutate(ctx, tenant, func(sc store.Scope) error {
		d.mu.Lock()
		d.entries++
		d.mu.Unlock()
		return fn(sc)
	})
}

func (d *c1MutateProbe) entryCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.entries
}

func c1PostgresNamedDSN(t *testing.T, dsn, applicationName string) string {
	t.Helper()
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("parse PostgreSQL fixture DSN: %v", err)
	}
	q := u.Query()
	q.Set("application_name", applicationName)
	u.RawQuery = q.Encode()
	return u.String()
}

func c1PostgresWaitForBlockedWriter(t *testing.T, ctx context.Context, observer *sql.DB, database, waiterName, holderName string) (int, int) {
	t.Helper()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var waiterPID, holderPID int
		err := observer.QueryRowContext(ctx, `
			SELECT waiter.pid, holder.pid
			FROM pg_catalog.pg_stat_activity waiter
			JOIN pg_catalog.pg_stat_activity holder
			  ON holder.pid = ANY(pg_catalog.pg_blocking_pids(waiter.pid))
			WHERE waiter.datname = $1
			  AND waiter.application_name = $2
			  AND holder.application_name = $3
			  AND waiter.state = 'active'
			  AND waiter.wait_event_type = 'Lock'
			  AND EXISTS (
				SELECT 1 FROM pg_catalog.pg_locks waiting_lock
				WHERE waiting_lock.pid = waiter.pid
				  AND waiting_lock.locktype = 'advisory'
				  AND NOT waiting_lock.granted
			  )
			LIMIT 1`, database, waiterName, holderName).Scan(&waiterPID, &holderPID)
		if err == nil {
			return waiterPID, holderPID
		}
		if !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("observe PostgreSQL advisory-lock waiter: %v", err)
		}
		select {
		case <-ctx.Done():
			t.Fatalf("PostgreSQL never exposed the second writer waiting on the first: %v", ctx.Err())
		case <-ticker.C:
		}
	}
}

func TestC1PostgresConcurrentFirstInsertionRequiresManualReplay(t *testing.T) {
	cfg, pg := c1PostgresFixture(t)
	const firstName = "inventory-c1-first"
	const secondName = "inventory-c1-second"
	firstCfg, secondCfg := cfg, cfg
	firstCfg.DSN = c1PostgresNamedDSN(t, cfg.DSN, firstName)
	secondCfg.DSN = c1PostgresNamedDSN(t, cfg.DSN, secondName)
	_, firstStore := c1PostgresOpen(t, firstCfg, false)
	tenant := c1Tenant(t, firstStore, "c1-pg-first-insert")
	_, secondStore := c1PostgresOpen(t, secondCfg, false)
	observer, err := sql.Open("pgx", pg.Superuser)
	if err != nil {
		t.Fatalf("open PostgreSQL lock observer: %v", err)
	}
	t.Cleanup(func() { _ = observer.Close() })

	hold := &c1FirstInsertHold{
		ModuleData: api.NewModuleData(firstStore), observed: make(chan struct{}), release: make(chan struct{}),
	}
	probe := &c1MutateProbe{ModuleData: api.NewModuleData(secondStore), started: make(chan struct{})}
	first := New(WithClock(pinnedClock{at: baseTime.Add(time.Hour)}))
	first.UseData(hold)
	second := New(WithClock(pinnedClock{at: baseTime.Add(time.Hour)}))
	second.UseData(probe)
	e := c1Event(tenant, "concurrent-first-pg", "source-a")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	firstErr := make(chan error, 1)
	go func() { firstErr <- first.onEvent(ctx, e) }()
	select {
	case <-hold.observed:
	case <-ctx.Done():
		t.Fatalf("first PostgreSQL insertion never observed the absent receipt: %v", ctx.Err())
	}

	secondCtx, cancelSecond := context.WithCancel(ctx)
	secondErr := make(chan error, 1)
	go func() { secondErr <- second.onEvent(secondCtx, e) }()
	select {
	case <-probe.started:
	case <-ctx.Done():
		t.Fatalf("second PostgreSQL insertion never entered ModuleData.Mutate: %v", ctx.Err())
	}
	observeCtx, stopObserve := context.WithTimeout(ctx, 10*time.Second)
	waiterPID, holderPID := c1PostgresWaitForBlockedWriter(t, observeCtx, observer, pg.Database, secondName, firstName)
	stopObserve()
	t.Logf("observed independent PostgreSQL writer pid=%d waiting on holder pid=%d advisory lock", waiterPID, holderPID)
	cancelSecond()
	var blockedErr error
	select {
	case blockedErr = <-secondErr:
	case <-ctx.Done():
		t.Fatalf("blocked PostgreSQL insertion did not terminate after cancellation: %v", ctx.Err())
	}
	if blockedErr == nil {
		t.Fatal("blocked first insertion reported success after its caller canceled the confirmed lock wait")
	}
	if probe.entryCount() != 0 {
		t.Fatal("blocked second writer entered the receipt callback before the first transaction committed")
	}
	close(hold.release)
	select {
	case err := <-firstErr:
		if err != nil {
			t.Fatalf("first PostgreSQL insertion failed after releasing its observed hold: %v", err)
		}
	case <-ctx.Done():
		t.Fatalf("first PostgreSQL insertion did not terminate: %v", ctx.Err())
	}

	beforeReplay := c1PostgresReceiptState(t, firstStore, tenant, e.ID)
	if beforeReplay.Deliveries != 1 || beforeReplay.MemberCount != 2 {
		t.Fatalf("winning receipt is incomplete: %+v", beforeReplay)
	}
	c1PostgresAssertCompleteReceipt(t, firstStore, tenant, e.ID, 2)
	beforeMembers := c1PostgresMembers(t, firstStore, tenant)
	if len(c1Rows(t, firstStore, tenant, observationReceiptKind)) != 1 ||
		len(c1Rows(t, firstStore, tenant, observationMemberKind)) != 2 ||
		len(c1Rows(t, firstStore, tenant, observationConflictKind)) != 0 {
		t.Fatal("losing transaction left a receipt prefix, orphan member, or conflict")
	}
	if agents, resources := c1PostgresCoreIDs(t, firstStore, tenant); len(agents) != 1 || len(resources) != 1 {
		t.Fatalf("losing transaction left a catalog ghost: agents=%v resources=%v", agents, resources)
	}
	for _, kind := range []string{kindAgent, kindResource} {
		if got := c1PostgresCatalogRow(t, firstStore, tenant, kind).Int(colOccurrence); got != 1 {
			t.Fatalf("catalog %s count=%d before replay, want 1", kind, got)
		}
	}

	replay := New(WithClock(pinnedClock{at: baseTime.Add(time.Hour)}))
	replay.UseData(api.NewModuleData(secondStore))
	if err := replay.onEvent(context.Background(), e); err != nil {
		t.Fatalf("explicit replay after terminal contention failure: %v", err)
	}
	afterReplay := c1PostgresReceiptState(t, secondStore, tenant, e.ID)
	if afterReplay.ID != beforeReplay.ID || afterReplay.Key != beforeReplay.Key ||
		afterReplay.FactsHash != beforeReplay.FactsHash || afterReplay.Facts != beforeReplay.Facts ||
		afterReplay.MemberCount != 2 || afterReplay.Deliveries != 2 ||
		!reflect.DeepEqual(c1PostgresMembers(t, secondStore, tenant), beforeMembers) {
		t.Fatalf("manual replay was not exact: before=%+v after=%+v", beforeReplay, afterReplay)
	}
	for _, kind := range []string{kindAgent, kindResource} {
		if got := c1PostgresCatalogRow(t, secondStore, tenant, kind).Int(colOccurrence); got != 2 {
			t.Fatalf("catalog %s count=%d after replay, want 2", kind, got)
		}
	}
}
