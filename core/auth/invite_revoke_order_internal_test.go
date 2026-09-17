// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	_ "github.com/jackc/pgx/v5/stdlib" // database/sql driver "pgx" for the observation connection
	"github.com/olivaresai/olivares/core/internal/pgtest"
	"github.com/olivaresai/olivares/core/internal/store/sqlstore"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// RevokeInvite's LOCK ORDER (onboarding.go).
//
// The defect these tests pin is not an authorization hole and not a new
// protocol: it is the ORDER in which one existing transaction takes two
// existing locks. RevokeInvite deleted the invitation row and only afterwards
// appended its audit event, and the audit log an AuthScope hands out takes the
// GLOBAL directory lock before any audit lock (sqlstore/authscope.go
// authAuditLog.globalFirst). AcceptInvite takes the same two locks the other way
// round: Users().Update runs the directory prepare (global) and only then does
// Invites().Update touch the invitation row. On PostgreSQL that is a cycle, and
// the engine resolves it by killing one of the two transactions.
//
// The correction is one call to the EXISTING compound-callback admission helper
// with an EMPTY User set (scim.go prepareUserAuthorityWrite): revocation changes
// no User, so it declares none, and the helper still takes global before the
// first source write. These tests therefore assert three separate things and
// keep them separate:
//
//  1. the observable ORDER inside the callback (admission, then the row, then
//     the audit append), on both engines;
//  2. that the admission is a DOOR, not a grant — an absent capability or a
//     refused preparation stops the method BEFORE the delete, and a successful
//     admission on its own moves neither the User authority H nor any tenant
//     directory epoch E;
//  3. that the order is the thing that matters, by driving two REAL PostgreSQL
//     transactions against each other on a controlled schedule: the preserved
//     baseline body deadlocks, and the corrected method does not.
//
// Nothing here adds a production test flag. The schedule is driven by narrow
// decorators around the existing store.Store/store.AuthScope surface, and the
// only "is the other transaction actually waiting?" question is answered by
// asking PostgreSQL (pg_stat_activity), never by sleeping and hoping.

const (
	phaseAdmission    = "prepare-user-authority"
	phaseInviteGet    = "invites-get"
	phaseInviteDelete = "invites-delete"
	phaseInviteUpdate = "invites-update"
	phaseUserUpdate   = "users-update"
	phaseAuditAppend  = "audit-append"

	revokeOrderPassword = "invitee-chosen-pw-1"
	revokeOrderIP       = "203.0.113.9"
)

// revokeOrderObserver records the phase boundaries one decorated transaction
// crosses and, for the concurrency schedules, releases or blocks the goroutine
// at a named boundary. before/after are read-only once a test has installed
// them; trace is guarded because two decorated transactions may share an
// observer in principle (the schedules below give each transaction its own).
type revokeOrderObserver struct {
	mu           sync.Mutex
	trace        []string
	admissionIDs []int

	// capability=false removes store.AuthUserAuthorityWriter from the scope's
	// method set entirely, which is what a store that cannot admit a compound
	// callback looks like to prepareUserAuthorityWrite.
	capability bool
	// prepareErr refuses the admission without calling the real preparation.
	prepareErr error

	before map[string]func()
	after  map[string]func()
}

func newRevokeOrderObserver() *revokeOrderObserver {
	return &revokeOrderObserver{capability: true}
}

func (o *revokeOrderObserver) begin(phase string) {
	o.mu.Lock()
	o.trace = append(o.trace, phase)
	hook := o.before[phase]
	o.mu.Unlock()
	if hook != nil {
		hook()
	}
}

func (o *revokeOrderObserver) end(phase string) {
	o.mu.Lock()
	hook := o.after[phase]
	o.mu.Unlock()
	if hook != nil {
		hook()
	}
}

func (o *revokeOrderObserver) phases() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return slices.Clone(o.trace)
}

func (o *revokeOrderObserver) declaredUserSets() []int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return slices.Clone(o.admissionIDs)
}

// revokeOrderStore decorates only AuthMutate: the fixture's own seeding and
// every assertion read through the UNDECORATED store, so an observation can
// never be an artifact of the observer.
type revokeOrderStore struct {
	store.Store
	obs *revokeOrderObserver
}

func (s *revokeOrderStore) AuthMutate(ctx context.Context, fn func(store.AuthScope) error) error {
	return s.Store.AuthMutate(ctx, func(as store.AuthScope) error {
		scope := &revokeOrderScope{AuthScope: as, obs: s.obs}
		if !s.obs.capability {
			// The method set of this anonymous struct is EXACTLY store.AuthScope,
			// so a caller's comma-ok for the optional writer is false — the
			// capability is absent structurally rather than by a flag the
			// production helper would have to consult.
			return fn(struct{ store.AuthScope }{scope})
		}
		return fn(scope)
	})
}

type revokeOrderScope struct {
	store.AuthScope
	obs *revokeOrderObserver
}

var _ store.AuthUserAuthorityWriter = (*revokeOrderScope)(nil)

func (s *revokeOrderScope) PrepareUserAuthorityWrite(ctx context.Context, ids []model.ID) error {
	s.obs.begin(phaseAdmission)
	defer s.obs.end(phaseAdmission)
	s.obs.mu.Lock()
	s.obs.admissionIDs = append(s.obs.admissionIDs, len(ids))
	s.obs.mu.Unlock()
	if s.obs.prepareErr != nil {
		return s.obs.prepareErr
	}
	writer, ok := s.AuthScope.(store.AuthUserAuthorityWriter)
	if !ok {
		return store.ErrDirectoryUnavailable
	}
	return writer.PrepareUserAuthorityWrite(ctx, ids)
}

func (s *revokeOrderScope) Invites() store.Repository[model.UserInvite] {
	return &revokeOrderInvites{Repository: s.AuthScope.Invites(), obs: s.obs}
}

func (s *revokeOrderScope) Users() store.MutableRepository[model.User] {
	return &revokeOrderUsers{MutableRepository: s.AuthScope.Users(), obs: s.obs}
}

func (s *revokeOrderScope) Audit() store.AuditLog {
	return &revokeOrderAudit{AuditLog: s.AuthScope.Audit(), obs: s.obs}
}

type revokeOrderInvites struct {
	store.Repository[model.UserInvite]
	obs *revokeOrderObserver
}

func (r *revokeOrderInvites) Get(ctx context.Context, id model.ID) (model.UserInvite, error) {
	r.obs.begin(phaseInviteGet)
	defer r.obs.end(phaseInviteGet)
	return r.Repository.Get(ctx, id)
}

func (r *revokeOrderInvites) Delete(ctx context.Context, id model.ID) error {
	r.obs.begin(phaseInviteDelete)
	defer r.obs.end(phaseInviteDelete)
	return r.Repository.Delete(ctx, id)
}

func (r *revokeOrderInvites) Update(ctx context.Context, in model.UserInvite) (model.UserInvite, error) {
	r.obs.begin(phaseInviteUpdate)
	defer r.obs.end(phaseInviteUpdate)
	return r.Repository.Update(ctx, in)
}

type revokeOrderUsers struct {
	store.MutableRepository[model.User]
	obs *revokeOrderObserver
}

func (r *revokeOrderUsers) Update(ctx context.Context, in model.User) (model.User, error) {
	r.obs.begin(phaseUserUpdate)
	defer r.obs.end(phaseUserUpdate)
	return r.MutableRepository.Update(ctx, in)
}

type revokeOrderAudit struct {
	store.AuditLog
	obs *revokeOrderObserver
}

func (l *revokeOrderAudit) Append(ctx context.Context, d model.AuditDraft) (model.AuditEvent, error) {
	l.obs.begin(phaseAuditAppend)
	defer l.obs.end(phaseAuditAppend)
	return l.AuditLog.Append(ctx, d)
}

// legacyRevokeInvite is the PRESERVED BASELINE body of RevokeInvite at
// 2e3e2c2b8d929c6462acc23b9c18bdcb928a9cc4 — delete the row, then audit — kept
// verbatim down to the same internal auditAct call, with one barrier added
// between the two statements so the schedule can be driven instead of guessed.
// It exists to prove the hazard is REAL on the engine, so that the corrected
// method's success is a difference in behavior and not a difference in luck.
func legacyRevokeInvite(
	ctx context.Context,
	st store.Store,
	actor Principal,
	tenant model.TenantID,
	id model.ID,
	afterDelete func(),
) error {
	return st.AuthMutate(ctx, func(as store.AuthScope) error {
		inv, err := as.Invites().Get(ctx, id)
		if err != nil {
			return err
		}
		if inv.TargetTenantID != tenant {
			return store.ErrNotFound // never a cross-tenant existence oracle
		}
		if err := as.Invites().Delete(ctx, id); err != nil {
			return err
		}
		if afterDelete != nil {
			afterDelete()
		}
		return auditAct(ctx, as, actor, "user.invite.revoke", "core.user_invite", id)
	})
}

// --- fixture -----------------------------------------------------------------

type revokeOrderFixture struct {
	ctx      context.Context
	raw      store.Store
	authr    *Authenticator
	admin    Principal
	tenant   model.TenantID
	other    model.TenantID
	engine   store.Engine
	database string  // PostgreSQL only: the isolated database name
	observer *sql.DB // PostgreSQL only: an independent connection used to ASK the engine who is waiting
}

func newRevokeOrderSQLite(t *testing.T) *revokeOrderFixture {
	t.Helper()
	return newRevokeOrderFixture(t, store.Config{
		Engine: store.EngineSQLite, DSN: ":memory:", Debug: true,
	}, "", "")
}

func newRevokeOrderPostgres(t *testing.T) *revokeOrderFixture {
	t.Helper()
	if !pgtest.Available(t) {
		t.Skip("no Postgres configured")
	}
	dsns := pgtest.Isolate(t, sqlstore.ProvisionPostgres, pgtest.SingleRole)
	return newRevokeOrderFixture(t, store.Config{
		Engine: store.EnginePostgres, DSN: dsns.App, MaxConns: 8,
	}, dsns.Database, dsns.App)
}

func newRevokeOrderFixture(t *testing.T, cfg store.Config, database, observeDSN string) *revokeOrderFixture {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	t.Cleanup(cancel)
	raw, err := sqlstore.Open(ctx, cfg, nil)
	if err != nil {
		t.Fatalf("open %s store: %v", cfg.Engine, err)
	}
	t.Cleanup(func() { _ = raw.Close() })

	f := &revokeOrderFixture{
		ctx: ctx, raw: raw, engine: cfg.Engine, database: database,
		authr: NewAuthenticator(raw, model.SystemClock{}),
		admin: Principal{
			Kind: KindUser, UserID: model.NewID(), CredID: model.NewID(),
			Superadmin: true, DisplayName: "r82-revoke-admin",
		},
	}
	f.tenant = f.provisionTenant(t, "r82-revoke-order")
	f.other = f.provisionTenant(t, "r82-revoke-order-other")

	if observeDSN != "" {
		// The observation connection is the APP role, the same role the store
		// uses, so pg_stat_activity shows it the wait columns of the backends it
		// is watching instead of NULL.
		db, err := sql.Open("pgx", observeDSN)
		if err != nil {
			t.Fatalf("open observation connection: %v", err)
		}
		db.SetMaxOpenConns(1)
		t.Cleanup(func() { _ = db.Close() })
		if err := db.PingContext(ctx); err != nil {
			t.Fatalf("reach observation connection: %v", err)
		}
		f.observer = db
	}
	return f
}

func (f *revokeOrderFixture) provisionTenant(t *testing.T, slug string) model.TenantID {
	t.Helper()
	var tenant model.TenantID
	if err := f.raw.System(f.ctx, func(sys store.SystemScope) error {
		org, err := sys.CreateOrg(f.ctx, model.Org{Name: slug, Slug: slug, Status: model.StatusActive})
		tenant = org.TenantID
		return err
	}); err != nil {
		t.Fatalf("provision tenant %q: %v", slug, err)
	}
	return tenant
}

// issueInvite seeds one pending invitation through the real onboarding path on
// the UNDECORATED store.
func (f *revokeOrderFixture) issueInvite(t *testing.T, tenant model.TenantID, email string) (model.ID, model.ID, string) {
	t.Helper()
	res, err := f.authr.OnboardMember(f.ctx, f.admin, tenant, OnboardInput{
		Email: email, DisplayName: "Invitee", Role: RoleViewer, Invite: true,
	})
	if err != nil {
		t.Fatalf("onboard %s in invite mode: %v", email, err)
	}
	if !res.Created || res.InviteID.IsZero() || res.InviteToken == "" {
		t.Fatalf("invite onboarding must create the account and mint a token: %+v", res)
	}
	return res.InviteID, res.User.ID, res.InviteToken
}

// revokeOrderState is everything a revocation, an acceptance or a refusal is
// allowed to move.
type revokeOrderState struct {
	inviteFound bool
	invite      model.UserInvite
	user        model.User
	sessions    int
	auditSeq    int64
	events      []model.AuditEvent
	authority   store.UserAuthorityFactRef
	epoch       store.AuthorizationFactRef
	otherEpoch  store.AuthorizationFactRef
}

func (f *revokeOrderFixture) state(t *testing.T, inviteID, userID model.ID) revokeOrderState {
	t.Helper()
	var s revokeOrderState
	if err := f.raw.AuthView(f.ctx, func(as store.AuthScope) error {
		inv, err := as.Invites().Get(f.ctx, inviteID)
		switch {
		case err == nil:
			s.inviteFound, s.invite = true, inv
		case errors.Is(err, store.ErrNotFound):
		default:
			return err
		}
		if s.user, err = as.Users().Get(f.ctx, userID); err != nil {
			return err
		}
		sessions, _, err := as.Sessions().List(f.ctx, byEq("user_id", userID.String(), 100))
		if err != nil {
			return err
		}
		s.sessions = len(sessions)
		if err := as.Audit().Walk(f.ctx, 0, func(ev model.AuditEvent) error {
			s.events = append(s.events, ev)
			s.auditSeq = ev.Seq
			return nil
		}); err != nil {
			return err
		}
		authority, ok := as.(store.AuthUserAuthorityEvidenceScope)
		if !ok {
			return errors.New("fixture store cannot read the User authority fact")
		}
		if s.authority, err = authority.ReadUserAuthorityFact(f.ctx, userID); err != nil {
			return err
		}
		epochs, ok := as.(store.AuthPrincipalEvidenceScope)
		if !ok {
			return errors.New("fixture store cannot read a directory epoch fact")
		}
		if s.epoch, err = epochs.ReadDirectoryEpochFact(f.ctx, f.tenant); err != nil {
			return err
		}
		s.otherEpoch, err = epochs.ReadDirectoryEpochFact(f.ctx, f.other)
		return err
	}); err != nil {
		t.Fatalf("read revoke-order state: %v", err)
	}
	return s
}

// eventsWith counts by action AND target kind on purpose: an acceptance appends
// TWO events with the action "user.invite.accept" — the redemption itself
// (core.user_invite) and the session mintSessionTx seals for the account
// (core.user) — so matching on the action alone would silently conflate them.
func (s revokeOrderState) eventsWith(action string, kind model.Kind) []model.AuditEvent {
	var out []model.AuditEvent
	for _, ev := range s.events {
		if ev.Action == action && ev.TargetKind == kind {
			out = append(out, ev)
		}
	}
	return out
}

// waitForLockWaiters asks PostgreSQL — not the clock — whether the other
// transaction really is parked on a heavyweight lock. It returns the wait events
// it saw so a failure message can name them.
func (f *revokeOrderFixture) waitForLockWaiters(t *testing.T, want int) []string {
	t.Helper()
	if f.observer == nil {
		t.Fatal("lock-wait observation requires the PostgreSQL fixture")
	}
	deadline := time.Now().Add(60 * time.Second)
	for {
		rows, err := f.observer.QueryContext(f.ctx, `
			SELECT coalesce(wait_event, '?')
			  FROM pg_catalog.pg_stat_activity
			 WHERE datname = $1
			   AND pid <> pg_catalog.pg_backend_pid()
			   AND wait_event_type = 'Lock'`, f.database)
		if err != nil {
			t.Fatalf("observe lock waits: %v", err)
		}
		var seen []string
		for rows.Next() {
			var event string
			if err := rows.Scan(&event); err != nil {
				rows.Close()
				t.Fatalf("scan lock wait: %v", err)
			}
			seen = append(seen, event)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			t.Fatalf("observe lock waits: %v", err)
		}
		rows.Close()
		if len(seen) >= want {
			return seen
		}
		if time.Now().After(deadline) {
			// Errorf, not Fatalf: the peer goroutines are parked on the barrier
			// this call is supposed to release, so the test must fail AND keep
			// going far enough to release them.
			t.Errorf("no transaction in %s ever waited on a heavyweight lock (saw %v); "+
				"the schedule this test needs did not form, so its result would mean nothing",
				f.database, seen)
			return seen
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// isPostgresDeadlock reports SQLSTATE 40P01 through whatever wrapping the store
// applied on the way out.
func isPostgresDeadlock(err error) bool {
	if err == nil {
		return false
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "40P01"
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "sqlstate 40p01") || strings.Contains(msg, "deadlock detected")
}

// revokeOrderBarrierTimeout bounds every rendezvous below. A barrier that is
// never reached means the schedule this test needs did not form — a RESULT, and
// one that has to be reported rather than waited out, so the wait fails the test
// and then RELEASES the goroutine instead of parking the run forever.
const revokeOrderBarrierTimeout = 20 * time.Second

func awaitSignal(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(revokeOrderBarrierTimeout):
		t.Errorf("the schedule never reached %q within %s; the interleaving under test did not form",
			what, revokeOrderBarrierTimeout)
	}
}

// assertNoPartialCredential pins the atomicity the two concurrent schedules must
// never break: an acceptance either committed ALL of its effects (the password,
// the invitation's single-use marker while the row survives, one session, and
// both of the events it seals) or NONE of them.
//
// The account's STATUS is deliberately not one of the facets: invite-mode
// onboarding already creates the account active (with no password), so status
// carries no information about whether a redemption committed.
func assertNoPartialCredential(t *testing.T, s revokeOrderState) {
	t.Helper()
	facets := map[string]bool{
		"password set":    s.user.PasswordHash != "",
		"session minted":  s.sessions == 1,
		"accept audited":  len(s.eventsWith("user.invite.accept", "core.user_invite")) > 0,
		"session audited": len(s.eventsWith("user.invite.accept", "core.user")) > 0,
	}
	if s.inviteFound {
		facets["invite marked used"] = s.invite.AcceptedAt != nil
	}
	var yes, no []string
	for name, ok := range facets {
		if ok {
			yes = append(yes, name)
		} else {
			no = append(no, name)
		}
	}
	slices.Sort(yes)
	slices.Sort(no)
	if len(yes) != 0 && len(no) != 0 {
		t.Fatalf("a concurrent schedule committed a PARTIAL acceptance: %v happened, %v did not", yes, no)
	}
	if s.sessions > 1 {
		t.Fatalf("one acceptance may mint at most one session, found %d", s.sessions)
	}
}

// --- ordering and admission --------------------------------------------------

func forEachRevokeOrderEngine(t *testing.T, fn func(*testing.T, *revokeOrderFixture)) {
	t.Helper()
	t.Run("sqlite", func(t *testing.T) { fn(t, newRevokeOrderSQLite(t)) })
	t.Run("postgres", func(t *testing.T) { fn(t, newRevokeOrderPostgres(t)) })
}

// A successful revocation removes the RIGHT invitation, commits an audit event
// attributed to the acting admin, and does all of it with the global directory
// admission taken BEFORE the invitation row.
func TestRevokeInviteAdmitsTheDirectoryBeforeTheInvitationRow(t *testing.T) {
	forEachRevokeOrderEngine(t, func(t *testing.T, f *revokeOrderFixture) {
		inviteID, userID, _ := f.issueInvite(t, f.tenant, "revoke-order-target@acme.test")
		keepID, keepUserID, _ := f.issueInvite(t, f.other, "revoke-order-bystander@other.test")
		before := f.state(t, inviteID, userID)

		obs := newRevokeOrderObserver()
		a := NewAuthenticator(&revokeOrderStore{Store: f.raw, obs: obs}, model.SystemClock{})
		if err := a.RevokeInvite(f.ctx, f.admin, f.tenant, inviteID); err != nil {
			t.Fatalf("revoke a pending invitation: %v", err)
		}

		want := []string{phaseAdmission, phaseInviteGet, phaseInviteDelete, phaseAuditAppend}
		if got := obs.phases(); !slices.Equal(got, want) {
			t.Fatalf("revocation phase order = %v, want %v (the admission must precede the row)", got, want)
		}
		if got := obs.declaredUserSets(); !slices.Equal(got, []int{0}) {
			t.Fatalf("revocation declared User sets %v, want exactly one EMPTY set: revocation changes no User", got)
		}

		after := f.state(t, inviteID, userID)
		if after.inviteFound {
			t.Fatal("the revoked invitation is still readable")
		}
		if keep := f.state(t, keepID, keepUserID); !keep.inviteFound {
			t.Fatal("revocation removed another tenant's invitation")
		}
		if after.auditSeq != before.auditSeq+1 {
			t.Fatalf("revocation appended %d audit events, want exactly 1", after.auditSeq-before.auditSeq)
		}
		ev := after.events[len(after.events)-1]
		wantActor, err := f.admin.AttributableActor()
		if err != nil {
			t.Fatalf("fixture actor is unattributable: %v", err)
		}
		if ev.Action != "user.invite.revoke" || ev.TargetKind != "core.user_invite" ||
			ev.TargetID != inviteID || ev.Actor != wantActor || ev.ActorKind != model.ActorUser {
			t.Fatalf("revocation audit event = %+v, want action user.invite.revoke on %s by %s", ev, inviteID, wantActor)
		}
	})
}

// A cross-tenant id is not an existence oracle and, just as importantly, is not
// a write: the admission runs, the row is read, and nothing else happens.
func TestRevokeInviteCrossTenantRefusesWithoutDeletionOrAudit(t *testing.T) {
	forEachRevokeOrderEngine(t, func(t *testing.T, f *revokeOrderFixture) {
		inviteID, userID, _ := f.issueInvite(t, f.tenant, "revoke-order-cross@acme.test")
		before := f.state(t, inviteID, userID)

		obs := newRevokeOrderObserver()
		a := NewAuthenticator(&revokeOrderStore{Store: f.raw, obs: obs}, model.SystemClock{})
		err := a.RevokeInvite(f.ctx, f.admin, f.other, inviteID)
		if !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("cross-tenant revocation error = %v, want store.ErrNotFound", err)
		}

		want := []string{phaseAdmission, phaseInviteGet}
		if got := obs.phases(); !slices.Equal(got, want) {
			t.Fatalf("refused revocation phases = %v, want %v (no delete, no append)", got, want)
		}
		after := f.state(t, inviteID, userID)
		if !after.inviteFound {
			t.Fatal("a cross-tenant revocation deleted the invitation")
		}
		if after.auditSeq != before.auditSeq {
			t.Fatalf("a cross-tenant revocation appended %d audit events", after.auditSeq-before.auditSeq)
		}
	})
}

// The admission is a door that fails CLOSED. Both refusal shapes — the store
// cannot admit a compound callback at all, and the preparation itself refuses —
// must stop the method before the invitation row is touched.
func TestRevokeInviteRefusedAdmissionFailsBeforeTheDelete(t *testing.T) {
	refused := errors.New("r82: preparation refused")
	cases := []struct {
		name   string
		setup  func(*revokeOrderObserver)
		phases []string
		wants  func(error) bool
		desc   string
	}{
		{
			name:   "absent-capability",
			setup:  func(o *revokeOrderObserver) { o.capability = false },
			phases: nil,
			wants:  func(err error) bool { return errors.Is(err, store.ErrDirectoryUnavailable) },
			desc:   "store.ErrDirectoryUnavailable",
		},
		{
			name:   "rejected-preparation",
			setup:  func(o *revokeOrderObserver) { o.prepareErr = refused },
			phases: []string{phaseAdmission},
			wants:  func(err error) bool { return errors.Is(err, refused) },
			desc:   "the preparation's own error",
		},
	}
	forEachRevokeOrderEngine(t, func(t *testing.T, f *revokeOrderFixture) {
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				inviteID, userID, _ := f.issueInvite(t, f.tenant, tc.name+"@acme.test")
				before := f.state(t, inviteID, userID)

				obs := newRevokeOrderObserver()
				tc.setup(obs)
				a := NewAuthenticator(&revokeOrderStore{Store: f.raw, obs: obs}, model.SystemClock{})
				err := a.RevokeInvite(f.ctx, f.admin, f.tenant, inviteID)
				if !tc.wants(err) {
					t.Fatalf("revocation error = %v, want %s", err, tc.desc)
				}
				if got := obs.phases(); !slices.Equal(got, tc.phases) {
					t.Fatalf("refused revocation phases = %v, want %v: the delete must never be reached", got, tc.phases)
				}
				after := f.state(t, inviteID, userID)
				if !after.inviteFound {
					t.Fatal("a refused admission still deleted the invitation")
				}
				if after.auditSeq != before.auditSeq {
					t.Fatalf("a refused admission appended %d audit events", after.auditSeq-before.auditSeq)
				}
			})
		}
	})
}

// The admission takes a lock; it does not hand out authority. A completed
// revocation must leave the invited account's User authority H and BOTH tenant
// directory epochs E exactly where it found them — the new call is an ordering
// device, not a directory write.
func TestRevokeInviteAdmissionMovesNeitherUserAuthorityNorDirectoryEpoch(t *testing.T) {
	forEachRevokeOrderEngine(t, func(t *testing.T, f *revokeOrderFixture) {
		inviteID, userID, _ := f.issueInvite(t, f.tenant, "revoke-order-facts@acme.test")
		before := f.state(t, inviteID, userID)

		a := NewAuthenticator(&revokeOrderStore{Store: f.raw, obs: newRevokeOrderObserver()}, model.SystemClock{})
		if err := a.RevokeInvite(f.ctx, f.admin, f.tenant, inviteID); err != nil {
			t.Fatalf("revoke a pending invitation: %v", err)
		}
		after := f.state(t, inviteID, userID)

		if after.authority != before.authority {
			t.Fatalf("revocation moved the User authority H: %+v -> %+v", before.authority, after.authority)
		}
		if after.epoch != before.epoch {
			t.Fatalf("revocation moved the invitation tenant's directory epoch E: %+v -> %+v", before.epoch, after.epoch)
		}
		if after.otherEpoch != before.otherEpoch {
			t.Fatalf("revocation moved an unrelated tenant's directory epoch E: %+v -> %+v", before.otherEpoch, after.otherEpoch)
		}
		if after.user.Version != before.user.Version {
			t.Fatalf("revocation wrote the invited account row (version %d -> %d)", before.user.Version, after.user.Version)
		}
	})
}

// --- the schedule that made the order matter ---------------------------------

// The POSITIVE CONTROL for the defect. It drives the PRESERVED BASELINE body
// against a real concurrent AcceptInvite on the engine that can exhibit the
// cycle, with both waits observed rather than assumed:
//
//	baseline revoke : DELETE the invitation row .......... then wait for GLOBAL
//	accept          : take GLOBAL (Users().Update) ....... then wait for the ROW
//
// If this ever stops deadlocking, the hazard the correction removes is no longer
// reproducible here and the correction's justification must be re-derived rather
// than assumed — so a missing deadlock is a FAILURE, not a skip.
func TestRevokeInviteBaselineOrderDeadlocksAgainstAcceptOnPostgres(t *testing.T) {
	f := newRevokeOrderPostgres(t)
	inviteID, userID, token := f.issueInvite(t, f.tenant, "revoke-order-baseline@acme.test")

	revokeHoldsRow := make(chan struct{})
	acceptIsBlocked := make(chan struct{})

	acceptObs := newRevokeOrderObserver()
	acceptObs.before = map[string]func(){
		// Accept does not reach for the global lock until the baseline revoke is
		// holding the invitation row, so the inverse order is MADE here.
		phaseUserUpdate: func() { awaitSignal(t, revokeHoldsRow, "the revocation holding the invitation row") },
	}
	accept := NewAuthenticator(&revokeOrderStore{Store: f.raw, obs: acceptObs}, model.SystemClock{})

	var revokeErr, acceptErr error
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		revokeErr = legacyRevokeInvite(f.ctx, f.raw, f.admin, f.tenant, inviteID, func() {
			close(revokeHoldsRow)
			awaitSignal(t, acceptIsBlocked, "the acceptance waiting on the invitation row")
		})
	}()
	go func() {
		defer wg.Done()
		_, _, acceptErr = accept.AcceptInvite(f.ctx, token, revokeOrderPassword, revokeOrderIP)
	}()

	awaitSignal(t, revokeHoldsRow, "the revocation holding the invitation row")
	waits := f.waitForLockWaiters(t, 1)
	close(acceptIsBlocked)
	wg.Wait()

	if !isPostgresDeadlock(revokeErr) && !isPostgresDeadlock(acceptErr) {
		t.Fatalf("the baseline order must still deadlock on PostgreSQL (observed waits %v): revoke=%v accept=%v",
			waits, revokeErr, acceptErr)
	}
	t.Logf("baseline order deadlocked as designed (waits %v): revoke=%v accept=%v", waits, revokeErr, acceptErr)
	assertNoPartialCredential(t, f.state(t, inviteID, userID))
}

// The CORRECTED method under the SAME two schedules. Neither may deadlock, and
// each must land on one of the two legitimate serial outcomes that the existing
// invitation semantics already allow.
func TestRevokeInviteCorrectedOrderSurvivesConcurrentAcceptOnPostgres(t *testing.T) {
	// Revocation admitted first: it holds global from its admission, so the
	// acceptance queues on the global lock instead of forming a cycle. Once the
	// revocation commits, the acceptance CANNOT commit a credential — the
	// invitation it read is gone.
	t.Run("revoke-commits-first", func(t *testing.T) {
		f := newRevokeOrderPostgres(t)
		inviteID, userID, token := f.issueInvite(t, f.tenant, "revoke-order-first@acme.test")

		revokeHoldsRow := make(chan struct{})
		acceptIsBlocked := make(chan struct{})

		acceptObs := newRevokeOrderObserver()
		acceptObs.before = map[string]func(){
			phaseUserUpdate: func() { awaitSignal(t, revokeHoldsRow, "the revocation holding the invitation row") },
		}
		accept := NewAuthenticator(&revokeOrderStore{Store: f.raw, obs: acceptObs}, model.SystemClock{})

		revokeObs := newRevokeOrderObserver()
		revokeObs.after = map[string]func(){
			phaseInviteDelete: func() {
				close(revokeHoldsRow)
				awaitSignal(t, acceptIsBlocked, "the acceptance waiting on a lock")
			},
		}
		revoke := NewAuthenticator(&revokeOrderStore{Store: f.raw, obs: revokeObs}, model.SystemClock{})

		var revokeErr, acceptErr error
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			revokeErr = revoke.RevokeInvite(f.ctx, f.admin, f.tenant, inviteID)
		}()
		go func() {
			defer wg.Done()
			_, _, acceptErr = accept.AcceptInvite(f.ctx, token, revokeOrderPassword, revokeOrderIP)
		}()

		awaitSignal(t, revokeHoldsRow, "the revocation holding the invitation row")
		waits := f.waitForLockWaiters(t, 1)
		close(acceptIsBlocked)
		wg.Wait()

		if isPostgresDeadlock(revokeErr) || isPostgresDeadlock(acceptErr) {
			t.Fatalf("the corrected order deadlocked (waits %v): revoke=%v accept=%v", waits, revokeErr, acceptErr)
		}
		if revokeErr != nil {
			t.Fatalf("the admitted revocation must commit: %v", revokeErr)
		}
		// The oracle is the SPECIFIC serial refusal this schedule produces, not
		// "some error". Accept read the invitation, queued at the directory lock
		// before Users().Update, and resumed only after the revocation committed
		// its DELETE; its Invites().Update then affects no row and the generic
		// update path distinguishes an absent row from a stale version
		// (sqlstore/generic.go:527-531). Accepting any non-nil error would let a
		// context deadline or an infrastructure fault pass as the outcome this
		// controlled schedule claims to have produced.
		if !errors.Is(acceptErr, store.ErrNotFound) {
			t.Fatalf("acceptance error = %v, want store.ErrNotFound: the invitation it read was deleted by the committed revocation", acceptErr)
		}
		s := f.state(t, inviteID, userID)
		if s.inviteFound {
			t.Fatal("the revoked invitation survived")
		}
		if got := len(s.eventsWith("user.invite.revoke", "core.user_invite")); got != 1 {
			t.Fatalf("revocation audit events = %d, want 1", got)
		}
		if got := len(s.eventsWith("user.invite.accept", "core.user_invite")); got != 0 {
			t.Fatalf("a refused acceptance committed %d accept events", got)
		}
		assertNoPartialCredential(t, s)
		t.Logf("revoke-first outcome: revoke committed, accept refused with %v (waits %v)", acceptErr, waits)
	})

	// The other legitimate serialization. The acceptance holds global first, the
	// revocation queues on the admission, and when it runs it legitimately deletes
	// an invitation that has already been used — existing semantics, preserved.
	t.Run("accept-commits-first", func(t *testing.T) {
		f := newRevokeOrderPostgres(t)
		inviteID, userID, token := f.issueInvite(t, f.tenant, "revoke-order-second@acme.test")

		acceptHoldsGlobal := make(chan struct{})
		revokeIsBlocked := make(chan struct{})

		acceptObs := newRevokeOrderObserver()
		acceptObs.after = map[string]func(){
			phaseUserUpdate: func() {
				close(acceptHoldsGlobal)
				awaitSignal(t, revokeIsBlocked, "the revocation waiting on the global admission")
			},
		}
		accept := NewAuthenticator(&revokeOrderStore{Store: f.raw, obs: acceptObs}, model.SystemClock{})

		revokeObs := newRevokeOrderObserver()
		revokeObs.before = map[string]func(){
			phaseAdmission: func() { awaitSignal(t, acceptHoldsGlobal, "the acceptance holding the global lock") },
		}
		revoke := NewAuthenticator(&revokeOrderStore{Store: f.raw, obs: revokeObs}, model.SystemClock{})

		var revokeErr, acceptErr error
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, _, acceptErr = accept.AcceptInvite(f.ctx, token, revokeOrderPassword, revokeOrderIP)
		}()
		go func() {
			defer wg.Done()
			revokeErr = revoke.RevokeInvite(f.ctx, f.admin, f.tenant, inviteID)
		}()

		awaitSignal(t, acceptHoldsGlobal, "the acceptance holding the global lock")
		waits := f.waitForLockWaiters(t, 1)
		close(revokeIsBlocked)
		wg.Wait()

		if isPostgresDeadlock(revokeErr) || isPostgresDeadlock(acceptErr) {
			t.Fatalf("the corrected order deadlocked (waits %v): revoke=%v accept=%v", waits, revokeErr, acceptErr)
		}
		if acceptErr != nil {
			t.Fatalf("the acceptance that held global first must commit: %v", acceptErr)
		}
		if revokeErr != nil {
			t.Fatalf("revoking an already-used invitation is legal under the existing semantics: %v", revokeErr)
		}
		s := f.state(t, inviteID, userID)
		if s.inviteFound {
			t.Fatal("the revocation did not remove the already-used invitation")
		}
		if got := len(s.eventsWith("user.invite.accept", "core.user_invite")); got != 1 {
			t.Fatalf("acceptance audit events = %d, want 1", got)
		}
		if got := len(s.eventsWith("user.invite.accept", "core.user")); got != 1 {
			t.Fatalf("session audit events = %d, want 1", got)
		}
		if got := len(s.eventsWith("user.invite.revoke", "core.user_invite")); got != 1 {
			t.Fatalf("revocation audit events = %d, want 1", got)
		}
		assertNoPartialCredential(t, s)
		if s.user.Status != model.StatusActive || s.sessions != 1 {
			t.Fatalf("the committed acceptance must leave an active account with one session, got %s/%d",
				s.user.Status, s.sessions)
		}
		t.Logf("accept-first outcome: accept committed, revoke removed the used invitation (waits %v)", waits)
	})
}
