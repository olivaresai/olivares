// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package audit_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/audit"
	"github.com/olivaresai/olivares/core/internal/pgtest"
	"github.com/olivaresai/olivares/core/internal/store/sqlstore"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// This file exists because the hole it covers DOES NOT EXIST ON SQLITE, and every
// other checkpoint test in this package runs on SQLite in-memory. SQLite has no
// roles and one connection, so its System transaction always sees the whole estate
// and ListOrgs is always authoritative there. The blind enumeration is a Postgres
// RLS behavior, so only a real Postgres server can witness it — a SQLite double
// would report green over the exact configuration that fails in production.

// TestCheckpointAllFailsClosedOnBlindEnumeration is the contract for H-02: on a
// Postgres store opened with ONLY an application DSN, CheckpointAll must FAIL rather
// than return nil having checkpointed nothing but the system chain.
//
// Before this returned nil. The scheduler read that nil as success, moved
// lastSuccess and logged "checkpoint written for all tenants"
// (cmd/olivares/checkpoint.go:120-128) — certifying coverage over tenants it had
// never enumerated. Note what is NOT asserted here: no coverage check was added
// inside CheckpointAll. The error is raised at the read (store.SystemScope.ListOrgs)
// and CheckpointAll merely propagates what it already propagated, so this test
// passes against an UNCHANGED core/audit/checkpoint.go.
func TestCheckpointAllFailsClosedOnBlindEnumeration(t *testing.T) {
	pg := pgtest.Isolate(t, sqlstore.ProvisionPostgres, pgtest.SingleRole)
	ctx := context.Background()

	// App DSN only: no --admin-dsn, so cross-tenant System reads are RLS-limited.
	st, err := sqlstore.Open(ctx, store.Config{Engine: store.EnginePostgres, DSN: pg.App, MaxConns: 4}, nil)
	if err != nil {
		t.Fatalf("open app-only store: %v", err)
	}
	defer func() { _ = st.Close() }()

	// A tenant with a real event: the material the ceremony must not silently skip.
	tenant := createTenant(t, st, "cpcov-"+uniqueSlug())
	appendEvent(t, st, tenant)

	head0 := headSeq(t, st, tenant)
	if head0 < 1 {
		t.Fatalf("setup: tenant head = %d, want >= 1 (the tenant must carry an event to be worth covering)", head0)
	}

	signer := newSigner(t)
	err = signer.CheckpointAll(ctx, st)
	if err == nil {
		t.Fatal("CheckpointAll returned nil on a store that CANNOT enumerate tenants: it certified coverage over material it never listed (H-02)")
	}
	if !errors.Is(err, store.ErrEnumerationNotAuthoritative) {
		t.Fatalf("CheckpointAll failed, but not for the enumeration reason: %v", err)
	}

	// Fail CLOSED, not fail-dirty: the tenant it could not enumerate must be
	// untouched, so a later run with an admin pool still anchors the true tip.
	if got := headSeq(t, st, tenant); got != head0 {
		t.Fatalf("tenant head moved from %d to %d during a failed CheckpointAll", head0, got)
	}
}

// TestCheckpointAllCoversEveryTenantWithAdminPool is the other half, and it is what
// stops the fix from being "make it always fail": with the BYPASSRLS admin pool the
// ceremony must succeed AND actually anchor the tenant it enumerated.
func TestCheckpointAllCoversEveryTenantWithAdminPool(t *testing.T) {
	pg := pgtest.Isolate(t, sqlstore.ProvisionPostgres, pgtest.SingleRole)
	ctx := context.Background()

	st, err := sqlstore.Open(ctx, store.Config{
		Engine: store.EnginePostgres, DSN: pg.App, AdminDSN: pg.Admin, MaxConns: 4,
	}, nil)
	if err != nil {
		t.Fatalf("open admin-pool store: %v", err)
	}
	defer func() { _ = st.Close() }()

	tenant := createTenant(t, st, "cpcov-ok-"+uniqueSlug())
	appendEvent(t, st, tenant)
	head0 := headSeq(t, st, tenant)

	signer := newSigner(t)
	if err := signer.CheckpointAll(ctx, st); err != nil {
		t.Fatalf("CheckpointAll with a BYPASSRLS admin pool: %v", err)
	}

	// The anchor must exist for THIS tenant, not merely for the system chain: a
	// checkpoint that only ever covers the system tenant would satisfy a nil-error
	// assertion while leaving every real tenant unanchored.
	head1 := headSeq(t, st, tenant)
	if head1 <= head0 {
		t.Fatalf("tenant head did not advance (%d -> %d): no checkpoint was appended for the enumerated tenant", head0, head1)
	}
	rep := verifyCheckpoints(t, st, tenant, signer.PublicKey())
	if !rep.OK || rep.Checkpoints < 1 {
		t.Fatalf("checkpoint report over the enumerated tenant: ok=%v checkpoints=%d reason=%q", rep.OK, rep.Checkpoints, rep.Reason)
	}
}

// uniqueSlug keeps each run's org slugs distinct within one isolated database.
func uniqueSlug() string {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("audit test: read entropy: " + err.Error())
	}
	return hex.EncodeToString(b[:])
}

func newSigner(t *testing.T) *audit.Signer {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	s, err := audit.NewSigner(priv)
	if err != nil {
		t.Fatalf("new signer: %v", err)
	}
	return s
}

func createTenant(t *testing.T, st store.Store, slug string) model.TenantID {
	t.Helper()
	var org model.Org
	if err := st.System(context.Background(), func(sys store.SystemScope) error {
		o, err := sys.CreateOrg(context.Background(), model.Org{
			Name: slug, Slug: strings.ToLower(slug), Status: model.StatusActive,
		})
		org = o
		return err
	}); err != nil {
		t.Fatalf("create org %q: %v", slug, err)
	}
	return org.TenantID
}

func appendEvent(t *testing.T, st store.Store, tenant model.TenantID) {
	t.Helper()
	if err := st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		_, err := sc.Audit().Append(context.Background(), model.AuditDraft{
			Actor: model.ActorSystem, ActorKind: model.ActorSystem,
			Action: "test.event", TargetKind: "core.test",
		})
		return err
	}); err != nil {
		t.Fatalf("append event for %s: %v", tenant, err)
	}
}

func headSeq(t *testing.T, st store.Store, tenant model.TenantID) int64 {
	t.Helper()
	var seq int64
	if err := st.View(context.Background(), tenant, func(sc store.Scope) error {
		head, ok, err := sc.Audit().Head(context.Background())
		if err != nil || !ok {
			return err
		}
		seq = head.Seq
		return nil
	}); err != nil {
		t.Fatalf("head for %s: %v", tenant, err)
	}
	return seq
}

func verifyCheckpoints(t *testing.T, st store.Store, tenant model.TenantID, pub ed25519.PublicKey) audit.CheckpointReport {
	t.Helper()
	var rep audit.CheckpointReport
	if err := st.View(context.Background(), tenant, func(sc store.Scope) error {
		r, err := audit.VerifyCheckpoints(context.Background(), sc.Audit(), pub)
		rep = r
		return err
	}); err != nil {
		t.Fatalf("verify checkpoints for %s: %v", tenant, err)
	}
	return rep
}
