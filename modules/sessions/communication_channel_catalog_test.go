// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// channelCatalogFixtureAuthorityWindow is the width of every authority window
// this fixture declares — the core request evidence here, and the five minutes
// the principal resolver and the ChannelGrant closure each declare for
// themselves. It is named so that a renewal can be shown to re-anchor exactly
// the same TTL instead of quietly widening one.
const channelCatalogFixtureAuthorityWindow = 5 * time.Minute

// channelCatalogFixture is the exact-authority fixture (real Authenticator,
// real store, fake core evidence source, fake directory resolver and closure)
// with an onboarded READER whose credential the catalog rebinds.
type channelCatalogFixture struct {
	directNoticeFixture
	readerID  model.ID
	readerRef auth.PrincipalRef
	resolver  *directNoticeReadDirectoryResolver
	closure   *directNoticeGrantClosureResolver
	source    *directNoticeInboxEvidenceSource
	ring      *communicationCursorTokenKeyring
}

func newChannelCatalogFixture(t *testing.T) channelCatalogFixture {
	t.Helper()
	return newChannelCatalogFixtureOn(t, communicationSchemaBackend{
		name: "sqlite-channel-catalog", engineName: store.EngineSQLite,
		dsn: filepath.Join(t.TempDir(), "channel-catalog.db"),
	})
}

func newChannelCatalogFixtureOn(t *testing.T, backend communicationSchemaBackend) channelCatalogFixture {
	t.Helper()
	ctx := context.Background()
	base := newDirectNoticeFixtureForBackend(t, backend, AckPolicyNone, 0, true, true, true)
	onboarded, err := base.authr.OnboardMember(ctx, base.authUser, base.tenant, auth.OnboardInput{
		Email: "catalog-reader@direct-notice.test", DisplayName: "Catalog reader",
		Role: auth.RoleViewer, Password: "catalog-reader-password",
	})
	if err != nil {
		t.Fatalf("onboard catalog reader: %v", err)
	}
	token, _, err := base.authr.Login(ctx, "catalog-reader@direct-notice.test", "catalog-reader-password", "127.0.0.4")
	if err != nil {
		t.Fatalf("login catalog reader: %v", err)
	}
	reader, err := base.authr.Authenticate(ctx, token)
	if err != nil {
		t.Fatalf("authenticate catalog reader: %v", err)
	}
	readerRef, ok := reader.Ref()
	if !ok {
		t.Fatal("catalog reader has no opaque ref")
	}
	epoch, facts := directNoticeExactReadEpochFacts(t, base)
	base.epoch = epoch
	base.attestor.epoch = epoch
	base.closure.epoch = epoch
	base.source.evidence.Facts = append([]store.AuthorizationFactRef(nil), facts...)
	base.source.evidence.ObservedAt = base.now
	base.source.evidence.FreshUntil = base.now.Add(channelCatalogFixtureAuthorityWindow)
	resolver := &directNoticeReadDirectoryResolver{now: base.now, epoch: epoch}
	closure := &directNoticeGrantClosureResolver{
		epoch: epoch, now: base.now, subjectsByUser: map[model.ID][]CommunicationSubjectRef{},
	}
	source := &directNoticeInboxEvidenceSource{
		base: base.source.evidence, outcomes: map[model.ID]auth.EvidenceOutcome{},
	}
	base.m.communicationDirectoryResolver = resolver
	base.m.communicationGrantClosure = closure
	base.m.useCommunicationRequestAuthoritySources(base.authr, source)
	ring := newChannelCatalogNavigationKeyring(t, "k3cat-fixture")
	base.m.communicationCursorKeyring = ring
	return channelCatalogFixture{
		directNoticeFixture: base, readerID: onboarded.User.ID, readerRef: readerRef,
		resolver: resolver, closure: closure, source: source, ring: ring,
	}
}

func (f channelCatalogFixture) readerSubject() CommunicationSubjectRef {
	return CommunicationSubjectRef{Kind: SubjectUser, Ref: f.readerID.String()}
}

// observeEngineClock samples database time through the module's own existing
// seam. That is deliberately NOT the injected fixture clock: the guard that
// judges a request-authority window, communicationTx.validateAuthorityFreshness,
// is evaluated against store.TransactionClock, which reads the engine's own
// clock and never consults the application clock.
func (f channelCatalogFixture) observeEngineClock(t *testing.T) time.Time {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	observedAt, err := f.m.observeChannelCatalogDatabaseTime(ctx, f.scope)
	if err != nil {
		t.Fatalf("observe the engine clock: %v", err)
	}
	return observedAt.UTC()
}

// declareRequestAuthorityWindow re-seals the fixture's request-authority
// evidence on an EXPLICITLY STATED window and changes nothing else about it —
// outcome, checks and facts are untouched. A malformed window is refused here so
// that a test asserting a freshness refusal cannot be satisfied by "the window
// is malformed", which is a different verdict from "the window has closed".
func (f channelCatalogFixture) declareRequestAuthorityWindow(
	t *testing.T,
	observedAt, freshUntil time.Time,
) {
	t.Helper()
	if observedAt.IsZero() || !freshUntil.After(observedAt) {
		t.Fatalf("declared request authority window is malformed: [%s, %s)", observedAt, freshUntil)
	}
	f.source.base.ObservedAt = observedAt
	f.source.base.FreshUntil = freshUntil
}

// renewRequestAuthorityEvidence re-anchors this fixture's whole preflight
// authority on the engine clock, after setup, and returns the window it
// declared.
//
// ⛔ THREE WINDOWS AND TWO CLOCKS, WHICH IS THE ENTIRE DEFECT.
// newChannelCatalogFixtureOn seals three five-minute windows ONCE, at
// construction, off the fixture clock that newDirectNoticeFixtureForBackend
// deliberately sets ONE MINUTE BEHIND real time: the principal resolution, the
// ChannelGrant closure, and the core request evidence. Two different clocks then
// judge them. preflightDirectNoticeReaderIdentity reads the injected application
// clock, which is frozen and therefore never leaves its own window;
// communicationTx.validateAuthorityFreshness reads store.TransactionClock — the
// database engine's clock, which by contract never consults the injected one. So
// the request seal is worth FOUR real minutes counted from construction, and
// directNoticeReaderAuthorityWindow pins it there for good: narrowTo intersects
// the request window with the resolver/closure one, so re-declaring the core
// evidence ALONE cannot lift that ceiling by a single second.
//
// Renewing therefore moves the fixture clock forward to the engine's own reading
// and re-anchors all three windows on that one instant, each keeping its own
// five minutes. No TTL grows, no deadline widens, no guard is relaxed and no
// permission is invented: a request whose window has closed is still refused,
// which TestChannelCatalogSetupClockGapRefusesExpiredRequestAuthority proves
// against the real guard, through the real clock seam, on both sides of the
// renewal.
func (f channelCatalogFixture) renewRequestAuthorityEvidence(t *testing.T) (time.Time, time.Time) {
	t.Helper()
	observedAt := f.observeEngineClock(t)
	freshUntil := observedAt.Add(channelCatalogFixtureAuthorityWindow)
	clock, ok := f.m.clock.(*testClock)
	if !ok {
		t.Fatalf("fixture clock is %T, not the test clock this renewal moves", f.m.clock)
	}
	if observedAt.Before(clock.get()) {
		t.Fatalf("the engine clock reads %s, BEHIND the fixture clock at %s: a renewal must "+
			"never move the fixture clock backwards", observedAt, clock.get())
	}
	clock.set(observedAt)
	// The resolver and the closure each declare their own five minutes from that
	// same instant, so the window narrowTo intersects with is no longer one that
	// expired during setup.
	f.resolver.now = observedAt
	f.closure.now = observedAt
	f.declareRequestAuthorityWindow(t, observedAt, freshUntil)
	return observedAt, freshUntil
}

func (f channelCatalogFixture) createChannel(t *testing.T, seed string, state ChannelState) Channel {
	t.Helper()
	id := model.NewID()
	input := communicationChannelRecord(f.workspace, seed)
	input[colCommState] = string(state)
	record, err := communicationCreateWithID(context.Background(), f.m, f.tenant, channelKind, id, input)
	if err != nil {
		t.Fatalf("create catalog Channel %s: %v", seed, err)
	}
	channel, err := channelFromRecord(record)
	if err != nil {
		t.Fatalf("decode catalog Channel %s: %v", seed, err)
	}
	return channel
}

func (f channelCatalogFixture) grant(
	t *testing.T,
	channelID model.ID,
	subject CommunicationSubjectRef,
	read, write, admin bool,
	expiresAt *time.Time,
) model.ID {
	t.Helper()
	id := model.NewID()
	// The store stamps created_at from the fixture clock (one minute behind real
	// time), and both the shape validator and the insert guard require an expiry
	// after created_at. An "already expired" grant therefore expires shortly
	// AFTER the fixture clock and BEFORE the engine clock the catalog observes.
	grant := ChannelGrant{
		MutableCommunicationEntity: MutableCommunicationEntity{
			CommunicationEntity: CommunicationEntity{
				ID: id, TenantID: f.tenant, WorkspaceID: f.workspace, Version: 1, CreatedAt: f.now,
			},
			UpdatedAt: f.now,
		},
		ChannelID: channelID, Subject: subject, Generation: 1,
		CanRead: read, CanWrite: write, CanAdmin: admin, State: ChannelGrantActive,
		GrantedBy: CommunicationActorRef{Kind: ActorUser, Ref: f.sender.String()},
		ExpiresAt: cloneDirectNoticeTime(expiresAt),
	}
	record, err := channelGrantToRecord(grant)
	if err != nil {
		t.Fatalf("encode catalog ChannelGrant: %v", err)
	}
	if _, err := communicationCreateWithID(context.Background(), f.m, f.tenant, channelGrantKind, id, record); err != nil {
		t.Fatalf("create catalog ChannelGrant: %v", err)
	}
	return id
}

func (f channelCatalogFixture) revokeGrant(t *testing.T, grantID model.ID) {
	t.Helper()
	ctx := context.Background()
	var current model.Record
	if err := f.m.data.View(ctx, f.tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(channelGrantKind)
		if err != nil {
			return err
		}
		current, err = repo.Get(ctx, grantID)
		return err
	}); err != nil {
		t.Fatalf("read grant %s: %v", grantID, err)
	}
	current[colCommState] = string(ChannelGrantRevoked)
	current[colCommRevokedByKind] = string(ActorUser)
	current[colCommRevokedByRef] = f.sender.String()
	if _, err := communicationUpdate(ctx, f.m, f.tenant, channelGrantKind, current); err != nil {
		t.Fatalf("revoke grant %s: %v", grantID, err)
	}
}

func (f channelCatalogFixture) reconcile(t *testing.T) {
	t.Helper()
	if err := f.m.ReconcileCommunicationGuards(
		context.Background(), f.tenant, CommunicationGuardReconcileStaged,
	); err != nil {
		t.Fatalf("reconcile catalog guards: %v", err)
	}
}

func (f channelCatalogFixture) list(t *testing.T, request ChannelCatalogRequest) (ChannelCatalogPage, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	return f.m.listVisibleChannelsWithAuthority(ctx, f.scope, f.readerRef, request)
}

func channelCatalogIDs(page ChannelCatalogPage) []model.ID {
	ids := make([]model.ID, 0, len(page.Items))
	for _, item := range page.Items {
		ids = append(ids, item.ID)
	}
	return ids
}

func decodeChannelCatalogTokenAnchor(t *testing.T, token string) map[string]any {
	t.Helper()
	parts := strings.Split(token, ".")
	if len(parts) != 4 || parts[0] != "c3n1" {
		t.Fatalf("continuation shape = %q", token)
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		t.Fatalf("decode continuation claims: %v", err)
	}
	var claims map[string]any
	if err := json.Unmarshal(raw, &claims); err != nil {
		t.Fatalf("continuation claims JSON: %v", err)
	}
	return claims
}

func TestChannelCatalogVisibilityTruthTable(t *testing.T) {
	t.Parallel()
	fx := newChannelCatalogFixture(t)
	group := CommunicationSubjectRef{Kind: SubjectUserGroup, Ref: model.NewID().String()}
	fx.closure.subjectsByUser[fx.readerID] = []CommunicationSubjectRef{fx.readerSubject(), group}
	// Expired at engine time, yet after the fixture clock that stamps created_at.
	past := fx.now.Add(30 * time.Second)
	future := time.Now().UTC().Add(time.Hour)

	readOnly := fx.createChannel(t, "read-only", ChannelActive)
	fx.grant(t, readOnly.ID, fx.readerSubject(), true, false, false, nil)
	readWrite := fx.createChannel(t, "read-write", ChannelActive)
	fx.grant(t, readWrite.ID, fx.readerSubject(), true, true, false, nil)
	readAdmin := fx.createChannel(t, "read-admin", ChannelActive)
	fx.grant(t, readAdmin.ID, fx.readerSubject(), true, false, true, nil)
	writeOnly := fx.createChannel(t, "write-only", ChannelActive)
	fx.grant(t, writeOnly.ID, fx.readerSubject(), false, true, false, nil)
	adminOnly := fx.createChannel(t, "admin-only", ChannelActive)
	fx.grant(t, adminOnly.ID, fx.readerSubject(), false, false, true, nil)
	mixed := fx.createChannel(t, "mixed", ChannelActive)
	fx.grant(t, mixed.ID, group, true, false, false, nil)
	fx.grant(t, mixed.ID, fx.readerSubject(), false, true, false, nil)
	expired := fx.createChannel(t, "expired", ChannelActive)
	fx.grant(t, expired.ID, fx.readerSubject(), true, true, true, &past)
	alternative := fx.createChannel(t, "alternative", ChannelActive)
	fx.grant(t, alternative.ID, fx.readerSubject(), true, false, false, &future)
	fx.grant(t, alternative.ID, group, true, false, false, nil)
	archived := fx.createChannel(t, "archived", ChannelArchived)
	fx.grant(t, archived.ID, fx.readerSubject(), true, true, true, nil)
	otherReader := fx.createChannel(t, "other-reader", ChannelActive)
	fx.grant(t, otherReader.ID, CommunicationSubjectRef{Kind: SubjectUser, Ref: model.NewID().String()}, true, true, true, nil)
	fx.reconcile(t)

	page, err := fx.list(t, ChannelCatalogRequest{Limit: 50})
	if err != nil {
		t.Fatalf("list catalog: %v", err)
	}
	want := map[model.ID]ChannelCatalogAccess{
		readOnly.ID:    {Read: true},
		readWrite.ID:   {Read: true, Write: true},
		readAdmin.ID:   {Read: true, Admin: true},
		mixed.ID:       {Read: true, Write: true},
		alternative.ID: {Read: true},
	}
	if len(page.Items) != len(want) || page.HasMore || page.Continuation != "" {
		t.Fatalf("catalog page = %+v, want %d visible without continuation", page, len(want))
	}
	for index, item := range page.Items {
		access, visible := want[item.ID]
		if !visible {
			t.Fatalf("hidden Channel %s (%s) is visible", item.ID, item.Slug)
		}
		if item.MyAccess != access {
			t.Fatalf("%s my_access = %+v, want %+v", item.Slug, item.MyAccess, access)
		}
		if index > 0 && page.Items[index-1].ID.String() >= item.ID.String() {
			t.Fatalf("catalog is not ordered by Channel ID: %s then %s", page.Items[index-1].ID, item.ID)
		}
	}
	for _, request := range fx.source.requests {
		if request.Permission != permChannelRead || request.Resource.Kind != string(channelKind) ||
			request.Resource.WorkspaceID != fx.workspace || request.Resource.ID == "" {
			t.Fatalf("catalog core question = %#v", request)
		}
	}
	// Every provisionally visible Channel is asked exactly once at discovery and
	// once at final closure; hidden Channels are never asked at all.
	if fx.source.calls != 2*len(want) {
		t.Fatalf("core questions = %d, want %d", fx.source.calls, 2*len(want))
	}
}

func TestChannelCatalogCoreDenyHidesAndUnknownAborts(t *testing.T) {
	t.Parallel()
	fx := newChannelCatalogFixture(t)
	first := fx.createChannel(t, "first", ChannelActive)
	fx.grant(t, first.ID, fx.readerSubject(), true, false, false, nil)
	second := fx.createChannel(t, "second", ChannelActive)
	fx.grant(t, second.ID, fx.readerSubject(), true, false, false, nil)
	fx.reconcile(t)

	fx.source.outcomes[first.ID] = auth.EvidenceDeny
	page, err := fx.list(t, ChannelCatalogRequest{Limit: 10})
	if err != nil || !reflect.DeepEqual(channelCatalogIDs(page), []model.ID{second.ID}) || page.HasMore {
		t.Fatalf("core deny page = %+v, %v; want only %s", page, err, second.ID)
	}
	fx.source.outcomes[first.ID] = auth.EvidenceUnknown
	page, err = fx.list(t, ChannelCatalogRequest{Limit: 10})
	if !errors.Is(err, ErrCommunicationEvidenceUnknown) || len(page.Items) != 0 || page.Continuation != "" {
		t.Fatalf("core unknown page = %+v, %v; want unavailable with no payload", page, err)
	}
}

func TestChannelCatalogPaginationCrossesHiddenRunsWithVisibleLookahead(t *testing.T) {
	t.Parallel()
	fx := newChannelCatalogFixture(t)
	past := fx.now.Add(30 * time.Second)
	visibleA := fx.createChannel(t, "visible-a", ChannelActive)
	fx.grant(t, visibleA.ID, fx.readerSubject(), true, false, false, nil)
	denied := fx.createChannel(t, "core-denied", ChannelActive)
	fx.grant(t, denied.ID, fx.readerSubject(), true, false, false, nil)
	writeOnly := fx.createChannel(t, "write-only", ChannelActive)
	fx.grant(t, writeOnly.ID, fx.readerSubject(), false, true, false, nil)
	expired := fx.createChannel(t, "expired", ChannelActive)
	fx.grant(t, expired.ID, fx.readerSubject(), true, false, false, &past)
	visibleB := fx.createChannel(t, "visible-b", ChannelActive)
	grantB := fx.grant(t, visibleB.ID, fx.readerSubject(), true, true, false, nil)
	trailing := fx.createChannel(t, "trailing-denied", ChannelActive)
	fx.grant(t, trailing.ID, fx.readerSubject(), true, false, false, nil)
	fx.reconcile(t)
	fx.source.outcomes[denied.ID] = auth.EvidenceDeny
	fx.source.outcomes[trailing.ID] = auth.EvidenceDeny

	first, err := fx.list(t, ChannelCatalogRequest{Limit: 1})
	if err != nil || !reflect.DeepEqual(channelCatalogIDs(first), []model.ID{visibleA.ID}) ||
		!first.HasMore || first.Continuation == "" {
		t.Fatalf("page one = %+v, %v; want [%s] with verified lookahead", first, err, visibleA.ID)
	}
	claims := decodeChannelCatalogTokenAnchor(t, first.Continuation)
	if claims["lc"] != visibleA.ID.String() || claims["rr"] != fx.readerID.String() || claims["rk"] != "user" {
		t.Fatalf("continuation claims = %v; anchor must be the returned Channel and the canonical reader", claims)
	}
	for _, hidden := range []model.ID{denied.ID, writeOnly.ID, expired.ID, visibleB.ID, trailing.ID, grantB} {
		if strings.Contains(first.Continuation, hidden.String()) {
			t.Fatalf("continuation discloses %s", hidden)
		}
	}
	raw, _ := base64.RawURLEncoding.DecodeString(strings.Split(first.Continuation, ".")[2])
	for _, hidden := range []model.ID{denied.ID, writeOnly.ID, expired.ID, visibleB.ID, trailing.ID, grantB} {
		if bytes.Contains(raw, []byte(hidden.String())) {
			t.Fatalf("decoded continuation discloses %s", hidden)
		}
	}

	second, err := fx.list(t, ChannelCatalogRequest{Limit: 1, Continuation: first.Continuation})
	if err != nil || !reflect.DeepEqual(channelCatalogIDs(second), []model.ID{visibleB.ID}) ||
		second.HasMore || second.Continuation != "" || !second.Items[0].MyAccess.Write {
		t.Fatalf("page two = %+v, %v; want [%s] terminal", second, err, visibleB.ID)
	}

	// Revocation between pages: the next request recomputes current authority.
	fx.revokeGrant(t, grantB)
	third, err := fx.list(t, ChannelCatalogRequest{Limit: 1, Continuation: first.Continuation})
	if err != nil || len(third.Items) != 0 || third.HasMore || third.Continuation != "" {
		t.Fatalf("page after revocation = %+v, %v; want empty terminal page", third, err)
	}
	// The first page now has no lookahead either.
	again, err := fx.list(t, ChannelCatalogRequest{Limit: 1})
	if err != nil || !reflect.DeepEqual(channelCatalogIDs(again), []model.ID{visibleA.ID}) ||
		again.HasMore || again.Continuation != "" {
		t.Fatalf("page one after revocation = %+v, %v; want [%s] without lookahead", again, err, visibleA.ID)
	}

	// A continuation of another reader, tenant or workspace is refused; an inbox
	// token is another family; a raw store cursor or Channel ID is not a token.
	otherReader := fx.ring
	filter := channelCatalogFilterHash()
	foreign, err := otherReader.mintChannelCatalogNavigation(communicationChannelCatalogNavigationClaims{
		tenantID: fx.tenant, workspaceID: fx.workspace,
		reader:     RecipientRef{Kind: RecipientUser, Ref: model.NewID().String()},
		filterHash: filter[:], lastChannelID: visibleA.ID,
	}, time.Now().UTC())
	if err != nil {
		t.Fatalf("mint foreign token: %v", err)
	}
	for name, token := range map[string]string{
		"other reader":    foreign,
		"raw channel id":  visibleA.ID.String(),
		"raw cursor":      "c3n1.raw",
		"inbox family":    "c2n1" + first.Continuation[4:],
		"tampered anchor": first.Continuation[:len(first.Continuation)-4] + "AAAA",
	} {
		page, err := fx.list(t, ChannelCatalogRequest{Limit: 1, Continuation: token})
		if !errors.Is(err, errCommunicationCursorTokenInvalid) || len(page.Items) != 0 {
			t.Fatalf("%s accepted: %+v, %v", name, page, err)
		}
	}
}

// channelCatalogEvidenceHook runs a callback around the Nth evidence request for
// one resource, which lets a test revoke a grant exactly between discovery and
// final closure.
type channelCatalogEvidenceHook struct {
	inner  *directNoticeInboxEvidenceSource
	target model.ID
	nth    int
	seen   int
	hook   func()
}

func (h *channelCatalogEvidenceHook) AuthorizeEvidence(
	ctx context.Context,
	request auth.Request,
) auth.AuthorizationEvidence {
	if request.Resource.ID == h.target.String() {
		h.seen++
		if h.seen == h.nth && h.hook != nil {
			h.hook()
		}
	}
	return h.inner.AuthorizeEvidence(ctx, request)
}

func TestChannelCatalogRevocationBetweenDiscoveryAndClosureAbortsWholePage(t *testing.T) {
	t.Parallel()
	fx := newChannelCatalogFixture(t)
	first := fx.createChannel(t, "first", ChannelActive)
	fx.grant(t, first.ID, fx.readerSubject(), true, false, false, nil)
	second := fx.createChannel(t, "second", ChannelActive)
	grantSecond := fx.grant(t, second.ID, fx.readerSubject(), true, false, false, nil)
	fx.reconcile(t)
	hook := &channelCatalogEvidenceHook{
		inner: fx.source, target: second.ID, nth: 2,
		hook: func() { fx.revokeGrant(t, grantSecond) },
	}
	fx.m.useCommunicationRequestAuthoritySources(fx.authr, hook)
	observer := &directNoticeMutateObserverData{inner: fx.m.data}
	fx.m.data = observer

	page, err := fx.list(t, ChannelCatalogRequest{Limit: 10})
	// Two mutations: the injected revocation itself and the page's single bound
	// transaction, which rolled back with no payload.
	if !errors.Is(err, ErrCommunicationEvidenceUnknown) || len(page.Items) != 0 ||
		page.HasMore || page.Continuation != "" || hook.seen != 2 || observer.mutates.Load() != 2 {
		t.Fatalf("revoked between discovery and closure = %+v, %v; seen=%d mutates=%d",
			page, err, hook.seen, observer.mutates.Load())
	}
	// The next request sees the revocation as a plain current fact.
	page, err = fx.list(t, ChannelCatalogRequest{Limit: 10})
	if err != nil || !reflect.DeepEqual(channelCatalogIDs(page), []model.ID{first.ID}) || page.HasMore {
		t.Fatalf("page after revocation = %+v, %v; want [%s]", page, err, first.ID)
	}
}

// channelCatalogClockShiftData shifts the database clock observations a bound
// transaction makes AFTER its initial observation (the refresh after locks and
// the final freshness check), which models grants expiring while locks wait.
type channelCatalogClockShiftData struct {
	inner api.ModuleData
	shift time.Duration
	mu    sync.Mutex
	calls int
}

type channelCatalogClockShiftScope struct {
	store.Scope
	clock     store.TransactionClock
	locker    store.TransactionLocker
	authority store.AuthoritySnapshotLocker
	directory store.DirectorySnapshotReader
	data      *channelCatalogClockShiftData
}

func (d *channelCatalogClockShiftData) View(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	return d.inner.View(ctx, tenant, fn)
}

func (d *channelCatalogClockShiftData) Mutate(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	return d.inner.Mutate(ctx, tenant, func(sc store.Scope) error {
		clock, clockOK := sc.(store.TransactionClock)
		locker, lockerOK := sc.(store.TransactionLocker)
		authority, authorityOK := sc.(store.AuthoritySnapshotLocker)
		directory, directoryOK := sc.(store.DirectorySnapshotReader)
		if !clockOK || !lockerOK || !authorityOK || !directoryOK {
			return errors.New("clock shift scope lacks transaction capabilities")
		}
		d.mu.Lock()
		d.calls = 0
		d.mu.Unlock()
		return fn(&channelCatalogClockShiftScope{
			Scope: sc, clock: clock, locker: locker, authority: authority, directory: directory, data: d,
		})
	})
}

func (s *channelCatalogClockShiftScope) TransactionNow(ctx context.Context) (model.Timestamp, error) {
	now, err := s.clock.TransactionNow(ctx)
	if err != nil {
		return now, err
	}
	s.data.mu.Lock()
	s.data.calls++
	calls := s.data.calls
	s.data.mu.Unlock()
	if calls >= 2 && s.data.shift != 0 {
		return model.NewTimestamp(now.Time().Add(s.data.shift)), nil
	}
	return now, nil
}

func (s *channelCatalogClockShiftScope) LockTransaction(ctx context.Context, key string) error {
	return s.locker.LockTransaction(ctx, key)
}

func (s *channelCatalogClockShiftScope) LockAuthoritySnapshot(ctx context.Context, facts []store.AuthorizationFactRef) error {
	return s.authority.LockAuthoritySnapshot(ctx, facts)
}

func (s *channelCatalogClockShiftScope) ReadDirectoryEpoch(ctx context.Context) (model.DirectoryEpoch, error) {
	return s.directory.ReadDirectoryEpoch(ctx)
}

func (s *channelCatalogClockShiftScope) ReadDirectoryTombstone(
	ctx context.Context,
	ref store.DirectoryPrincipalRef,
) (store.DirectoryTombstoneWitness, bool, error) {
	return s.directory.ReadDirectoryTombstone(ctx, ref)
}

func TestChannelCatalogExpiryWhileLocksWaitAndOrHorizons(t *testing.T) {
	t.Parallel()
	// Start each grant horizon after its parallel subtest resumes and its fixture
	// is ready. Time spent queued behind other tests must not expire the grant
	// before the initial read that the refresh clock is meant to advance.

	t.Run("only read grant expires while locks wait", func(t *testing.T) {
		t.Parallel()
		fx := newChannelCatalogFixture(t)
		soon := time.Now().UTC().Add(2 * time.Minute)
		channel := fx.createChannel(t, "expiring-read", ChannelActive)
		fx.grant(t, channel.ID, fx.readerSubject(), true, true, true, &soon)
		fx.reconcile(t)
		fx.renewRequestAuthorityEvidence(t)
		shift := &channelCatalogClockShiftData{inner: fx.m.data, shift: 3 * time.Minute}
		fx.m.data = shift
		page, err := fx.list(t, ChannelCatalogRequest{Limit: 10})
		if !errors.Is(err, ErrCommunicationEvidenceUnknown) || len(page.Items) != 0 {
			t.Fatalf("expired-at-refresh page = %+v, %v; want unavailable", page, err)
		}
		shift.shift = 0
		page, err = fx.list(t, ChannelCatalogRequest{Limit: 10})
		if err != nil || len(page.Items) != 1 || !page.Items[0].MyAccess.Write || !page.Items[0].MyAccess.Admin {
			t.Fatalf("unshifted page = %+v, %v; want the still-current grant", page, err)
		}
	})

	t.Run("later of two expiring read grants keeps the Channel visible", func(t *testing.T) {
		t.Parallel()
		fx := newChannelCatalogFixture(t)
		now := time.Now().UTC()
		soon, later := now.Add(2*time.Minute), now.Add(10*time.Minute)
		group := CommunicationSubjectRef{Kind: SubjectUserGroup, Ref: model.NewID().String()}
		fx.closure.subjectsByUser[fx.readerID] = []CommunicationSubjectRef{fx.readerSubject(), group}
		channel := fx.createChannel(t, "two-horizons", ChannelActive)
		fx.grant(t, channel.ID, fx.readerSubject(), true, false, false, &soon)
		fx.grant(t, channel.ID, group, true, false, false, &later)
		fx.reconcile(t)
		fx.renewRequestAuthorityEvidence(t)
		shift := &channelCatalogClockShiftData{inner: fx.m.data, shift: 3 * time.Minute}
		fx.m.data = shift
		page, err := fx.list(t, ChannelCatalogRequest{Limit: 10})
		if err != nil || len(page.Items) != 1 || page.Items[0].MyAccess != (ChannelCatalogAccess{Read: true}) {
			t.Fatalf("OR-horizon page = %+v, %v; the later grant must keep read current", page, err)
		}
		shift.shift = 11 * time.Minute
		page, err = fx.list(t, ChannelCatalogRequest{Limit: 10})
		if !errors.Is(err, ErrCommunicationEvidenceUnknown) || len(page.Items) != 0 {
			t.Fatalf("beyond both horizons = %+v, %v; want unavailable", page, err)
		}
	})

	t.Run("positive write bit expires while locks wait", func(t *testing.T) {
		t.Parallel()
		fx := newChannelCatalogFixture(t)
		soon := time.Now().UTC().Add(2 * time.Minute)
		// One subject holds one current grant generation per Channel, so the
		// durable read comes through a group subject and the expiring write
		// through the direct user grant.
		group := CommunicationSubjectRef{Kind: SubjectUserGroup, Ref: model.NewID().String()}
		fx.closure.subjectsByUser[fx.readerID] = []CommunicationSubjectRef{fx.readerSubject(), group}
		channel := fx.createChannel(t, "expiring-write", ChannelActive)
		fx.grant(t, channel.ID, group, true, false, false, nil)
		fx.grant(t, channel.ID, fx.readerSubject(), false, true, false, &soon)
		fx.reconcile(t)
		fx.renewRequestAuthorityEvidence(t)
		page, err := fx.list(t, ChannelCatalogRequest{Limit: 10})
		if err != nil || len(page.Items) != 1 || page.Items[0].MyAccess != (ChannelCatalogAccess{Read: true, Write: true}) {
			t.Fatalf("current write page = %+v, %v", page, err)
		}
		shift := &channelCatalogClockShiftData{inner: fx.m.data, shift: 3 * time.Minute}
		fx.m.data = shift
		page, err = fx.list(t, ChannelCatalogRequest{Limit: 10})
		if err != nil || len(page.Items) != 1 || page.Items[0].MyAccess != (ChannelCatalogAccess{Read: true}) {
			t.Fatalf("write expired at refresh = %+v, %v; want read only from the durable grant", page, err)
		}
	})
}

func TestChannelCatalogClosureDenyIsEmptyAndUnknownAborts(t *testing.T) {
	t.Parallel()
	fx := newChannelCatalogFixture(t)
	channel := fx.createChannel(t, "granted", ChannelActive)
	fx.grant(t, channel.ID, fx.readerSubject(), true, false, false, nil)
	fx.reconcile(t)
	deny := &directNoticeReadClosureResolver{now: fx.now, epoch: fx.epoch, outcome: ReadDeny}
	fx.m.communicationGrantClosure = deny
	page, err := fx.list(t, ChannelCatalogRequest{Limit: 10})
	if err != nil || len(page.Items) != 0 || page.HasMore || page.Continuation != "" || fx.source.calls != 0 {
		t.Fatalf("closure deny page = %+v, %v; calls=%d; want empty without any core question", page, err, fx.source.calls)
	}
	unknown := &directNoticeReadClosureResolver{now: fx.now, epoch: fx.epoch, outcome: ReadUnknown}
	fx.m.communicationGrantClosure = unknown
	page, err = fx.list(t, ChannelCatalogRequest{Limit: 10})
	if !errors.Is(err, ErrCommunicationEvidenceUnknown) || len(page.Items) != 0 || fx.source.calls != 0 {
		t.Fatalf("closure unknown page = %+v, %v; want unavailable", page, err)
	}
}

func TestChannelCatalogPublicBoundaryBindsCredentialBeforeReadiness(t *testing.T) {
	t.Parallel()
	fx := newChannelCatalogFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	page, err := fx.m.ListVisibleChannels(ctx, fx.scope, fx.readerRef, ChannelCatalogRequest{Limit: 1})
	if !errors.Is(err, ErrCommunicationEvidenceUnknown) || len(page.Items) != 0 ||
		fx.source.calls != 0 || fx.resolver.calls.Load() != 0 {
		t.Fatalf("public catalog with readiness OFF = %+v, %v; calls=%d resolver=%d",
			page, err, fx.source.calls, fx.resolver.calls.Load())
	}
	stale := auth.PrincipalRef{}
	if _, err := fx.m.ListVisibleChannels(ctx, fx.scope, stale, ChannelCatalogRequest{}); !errors.Is(err, ErrCommunicationEvidenceUnknown) {
		t.Fatalf("empty credential ref = %v", err)
	}
	if _, err := fx.m.listVisibleChannelsWithAuthority(context.Background(), fx.scope, fx.readerRef, ChannelCatalogRequest{}); !errors.Is(err, ErrCommunicationEvidenceUnknown) {
		t.Fatalf("catalog without a finite deadline = %v", err)
	}
	if _, err := fx.list(t, ChannelCatalogRequest{Limit: channelCatalogMaximumLimit + 1}); !errors.Is(err, ErrInvalidCommunicationModel) {
		t.Fatalf("limit above maximum = %v", err)
	}
}

// channelCatalogWorkObserver counts store work per entity kind and method so a
// sparse estate can prove that hidden Channels cost nothing beyond bounded
// projection rounds.
type channelCatalogWorkObserver struct {
	inner api.ModuleData
	mu    sync.Mutex
	calls map[string]int
}

func (o *channelCatalogWorkObserver) record(kind model.Kind, method string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.calls[string(kind)+"."+method]++
}

func (o *channelCatalogWorkObserver) count(kind model.Kind, method string) int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.calls[string(kind)+"."+method]
}

type channelCatalogWorkScope struct {
	store.Scope
	clock     store.TransactionClock
	locker    store.TransactionLocker
	authority store.AuthoritySnapshotLocker
	directory store.DirectorySnapshotReader
	observer  *channelCatalogWorkObserver
}

type channelCatalogWorkRepo struct {
	raw       store.GenericRepo
	stamped   store.TransactionStampedGenericRepo
	locker    store.RowLocker[model.Record]
	projector store.DistinctProjector
	kind      model.Kind
	observer  *channelCatalogWorkObserver
}

func (o *channelCatalogWorkObserver) View(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	return o.inner.View(ctx, tenant, func(sc store.Scope) error { return fn(o.wrap(sc)) })
}

func (o *channelCatalogWorkObserver) Mutate(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	return o.inner.Mutate(ctx, tenant, func(sc store.Scope) error { return fn(o.wrap(sc)) })
}

func (o *channelCatalogWorkObserver) wrap(sc store.Scope) store.Scope {
	clock, _ := sc.(store.TransactionClock)
	locker, _ := sc.(store.TransactionLocker)
	authority, _ := sc.(store.AuthoritySnapshotLocker)
	directory, _ := sc.(store.DirectorySnapshotReader)
	return &channelCatalogWorkScope{
		Scope: sc, clock: clock, locker: locker, authority: authority, directory: directory, observer: o,
	}
}

func (s *channelCatalogWorkScope) TransactionNow(ctx context.Context) (model.Timestamp, error) {
	return s.clock.TransactionNow(ctx)
}

func (s *channelCatalogWorkScope) LockTransaction(ctx context.Context, key string) error {
	return s.locker.LockTransaction(ctx, key)
}

func (s *channelCatalogWorkScope) LockAuthoritySnapshot(ctx context.Context, facts []store.AuthorizationFactRef) error {
	return s.authority.LockAuthoritySnapshot(ctx, facts)
}

func (s *channelCatalogWorkScope) ReadDirectoryEpoch(ctx context.Context) (model.DirectoryEpoch, error) {
	return s.directory.ReadDirectoryEpoch(ctx)
}

func (s *channelCatalogWorkScope) ReadDirectoryTombstone(
	ctx context.Context,
	ref store.DirectoryPrincipalRef,
) (store.DirectoryTombstoneWitness, bool, error) {
	return s.directory.ReadDirectoryTombstone(ctx, ref)
}

func (s *channelCatalogWorkScope) Ext(kind model.Kind) (store.GenericRepo, error) {
	raw, err := s.Scope.Ext(kind)
	if err != nil {
		return nil, err
	}
	stamped, _ := raw.(store.TransactionStampedGenericRepo)
	locker, _ := raw.(store.RowLocker[model.Record])
	projector, _ := raw.(store.DistinctProjector)
	if stamped == nil || locker == nil || projector == nil {
		return nil, fmt.Errorf("work observer: repository %s lacks an expected capability", kind)
	}
	return &channelCatalogWorkRepo{
		raw: raw, stamped: stamped, locker: locker, projector: projector, kind: kind, observer: s.observer,
	}, nil
}

func (r *channelCatalogWorkRepo) Descriptor() model.EntityDescriptor { return r.raw.Descriptor() }
func (r *channelCatalogWorkRepo) Get(ctx context.Context, id model.ID) (model.Record, error) {
	r.observer.record(r.kind, "get")
	return r.raw.Get(ctx, id)
}
func (r *channelCatalogWorkRepo) List(ctx context.Context, q model.Query) ([]model.Record, model.Page, error) {
	r.observer.record(r.kind, "list")
	return r.raw.List(ctx, q)
}
func (r *channelCatalogWorkRepo) Create(ctx context.Context, rec model.Record) (model.Record, error) {
	return r.raw.Create(ctx, rec)
}
func (r *channelCatalogWorkRepo) CreateWithID(ctx context.Context, id model.ID, rec model.Record) (model.Record, error) {
	return r.raw.CreateWithID(ctx, id, rec)
}
func (r *channelCatalogWorkRepo) Update(ctx context.Context, rec model.Record) (model.Record, error) {
	return r.raw.Update(ctx, rec)
}
func (r *channelCatalogWorkRepo) Delete(ctx context.Context, id model.ID) error {
	return r.raw.Delete(ctx, id)
}
func (r *channelCatalogWorkRepo) CreateAtTransactionTime(ctx context.Context, rec model.Record) (model.Record, error) {
	return r.stamped.CreateAtTransactionTime(ctx, rec)
}
func (r *channelCatalogWorkRepo) CreateWithIDAtTransactionTime(ctx context.Context, id model.ID, rec model.Record) (model.Record, error) {
	return r.stamped.CreateWithIDAtTransactionTime(ctx, id, rec)
}
func (r *channelCatalogWorkRepo) UpdateAtTransactionTime(ctx context.Context, rec model.Record) (model.Record, error) {
	return r.stamped.UpdateAtTransactionTime(ctx, rec)
}
func (r *channelCatalogWorkRepo) Lock(ctx context.Context, id model.ID) (model.Record, error) {
	r.observer.record(r.kind, "lock")
	return r.locker.Lock(ctx, id)
}
func (r *channelCatalogWorkRepo) ProjectDistinct(ctx context.Context, p store.DistinctProjection) (store.DistinctPage, error) {
	r.observer.record(r.kind, "project")
	return r.projector.ProjectDistinct(ctx, p)
}

// seedChannelCatalogHiddenChannels inserts n active Channels, each with an
// active read grant for an UNRELATED subject, in one store transaction. They are
// exactly the hidden rows a grant-first catalog must never touch.
func seedChannelCatalogHiddenChannels(t *testing.T, fx channelCatalogFixture, prefix string, n int) []model.ID {
	t.Helper()
	ctx := context.Background()
	stranger := CommunicationSubjectRef{Kind: SubjectUser, Ref: model.NewID().String()}
	ids := make([]model.ID, 0, n)
	if err := fx.m.data.Mutate(ctx, fx.tenant, func(sc store.Scope) error {
		channels, err := sc.Ext(channelKind)
		if err != nil {
			return err
		}
		grants, err := sc.Ext(channelGrantKind)
		if err != nil {
			return err
		}
		for index := 0; index < n; index++ {
			channelID := model.NewID()
			if _, err := channels.CreateWithID(ctx, channelID, communicationChannelRecord(
				fx.workspace, fmt.Sprintf("%s-%05d", prefix, index),
			)); err != nil {
				return err
			}
			grant := ChannelGrant{
				MutableCommunicationEntity: MutableCommunicationEntity{
					CommunicationEntity: CommunicationEntity{
						ID: model.NewID(), TenantID: fx.tenant, WorkspaceID: fx.workspace,
						Version: 1, CreatedAt: fx.now,
					},
					UpdatedAt: fx.now,
				},
				ChannelID: channelID, Subject: stranger, Generation: 1, CanRead: true,
				State:     ChannelGrantActive,
				GrantedBy: CommunicationActorRef{Kind: ActorUser, Ref: fx.sender.String()},
			}
			record, err := channelGrantToRecord(grant)
			if err != nil {
				return err
			}
			if _, err := grants.CreateWithID(ctx, grant.ID, record); err != nil {
				return err
			}
			ids = append(ids, channelID)
		}
		return nil
	}); err != nil {
		t.Fatalf("seed %d hidden Channels: %v", n, err)
	}
	return ids
}

func TestChannelCatalogSparseEstateProgressesWithoutPerHiddenChannelWork(t *testing.T) {
	t.Parallel()
	fx := newChannelCatalogFixture(t)
	const before, between, after = 4300, 150, 60
	setupStarted := time.Now()
	seedChannelCatalogHiddenChannels(t, fx, "before", before)
	visibleA := fx.createChannel(t, "visible-a", ChannelActive)
	fx.grant(t, visibleA.ID, fx.readerSubject(), true, false, false, nil)
	seedChannelCatalogHiddenChannels(t, fx, "between", between)
	visibleB := fx.createChannel(t, "visible-b", ChannelActive)
	fx.grant(t, visibleB.ID, fx.readerSubject(), true, true, false, nil)
	seedChannelCatalogHiddenChannels(t, fx, "after", after)
	fx.reconcile(t)
	setupElapsed := time.Since(setupStarted)

	// ⛔ SETUP IS OVER AND THE MEASUREMENT STARTS HERE, SO THE AUTHORITY IS
	// OBSERVED HERE. Seeding 4510 hidden rows under -race is the ESTATE this test
	// measures, not part of the REQUEST it measures. On the self-hosted
	// race-modules runner that seeding took 290.76 s — longer than the four real
	// minutes the constructor's five-minute seal is worth against the engine
	// clock — and every page came back empty with "request authority is no longer
	// fresh", which reads like a pagination defect and is a fixture that sealed
	// its authority before the work it was going to authorize. The seal is
	// re-anchored, never widened: same TTL, declared out loud, on the clock that
	// judges it.
	observedAt, freshUntil := fx.renewRequestAuthorityEvidence(t)
	t.Logf("sparse estate setup: %s for %d hidden Channels; request authority re-observed on the "+
		"engine clock at %s, fresh until %s (%s)", setupElapsed, before+between+after,
		observedAt.Format(time.RFC3339Nano), freshUntil.Format(time.RFC3339Nano),
		freshUntil.Sub(observedAt))

	observer := &channelCatalogWorkObserver{inner: fx.m.data, calls: map[string]int{}}
	fx.m.data = observer
	started := time.Now()
	page, err := fx.list(t, ChannelCatalogRequest{Limit: 10})
	elapsed := time.Since(started)
	if err != nil || !reflect.DeepEqual(channelCatalogIDs(page), []model.ID{visibleA.ID, visibleB.ID}) ||
		page.HasMore || page.Continuation != "" {
		t.Fatalf("sparse estate page = %+v, %v; want [%s %s] terminal", page, err, visibleA.ID, visibleB.ID)
	}
	projections := observer.count(channelGrantKind, "project")
	channelGets := observer.count(channelKind, "get")
	grantLists := observer.count(channelGrantKind, "list")
	channelLocks := observer.count(channelKind, "lock")
	grantLocks := observer.count(channelGrantKind, "lock")
	t.Logf("sparse estate: %d hidden Channels; work: projections=%d channel.get=%d grant.list=%d channel.lock=%d grant.lock=%d core_questions=%d elapsed=%s",
		before+between+after, projections, channelGets, grantLists, channelLocks, grantLocks, fx.source.calls, elapsed)
	if projections > 2 || channelGets != 2 || grantLists != 2 || channelLocks != 2 || grantLocks != 2 ||
		fx.source.calls != 4 {
		t.Fatalf("store work scaled with hidden Channels: projections=%d channel.get=%d grant.list=%d channel.lock=%d grant.lock=%d core=%d",
			projections, channelGets, grantLists, channelLocks, grantLocks, fx.source.calls)
	}
	// A page of one crosses the 4300-row hidden run and the 150-row run with one
	// continuation, each page verified by its own final transaction.
	first, err := fx.list(t, ChannelCatalogRequest{Limit: 1})
	if err != nil || !reflect.DeepEqual(channelCatalogIDs(first), []model.ID{visibleA.ID}) || !first.HasMore {
		t.Fatalf("sparse page one = %+v, %v", first, err)
	}
	second, err := fx.list(t, ChannelCatalogRequest{Limit: 1, Continuation: first.Continuation})
	if err != nil || !reflect.DeepEqual(channelCatalogIDs(second), []model.ID{visibleB.ID}) || second.HasMore || second.Continuation != "" {
		t.Fatalf("sparse page two = %+v, %v", second, err)
	}
}

// TestChannelCatalogSetupClockGapRefusesExpiredRequestAuthority is the causal
// pair for that renewal: ONE fixture, ONE estate, and the only thing that moves
// between the two reads is the window the fixture declares.
//
// ⛔ WHAT IT EXISTS TO CATCH. If renewRequestAuthorityEvidence ever became a
// bypass — a longer TTL, a window pinned open, a guard taught to ignore the
// engine clock — the sparse-estate test above would still be green and would
// prove nothing about freshness. So this one states the other half directly: a
// window that closed BEFORE the request is refused by the real guard,
// communicationTx.validateAuthorityFreshness, by name, with nothing disclosed.
// It buys the setup gap outright instead of paying 4510 rows for it.
func TestChannelCatalogSetupClockGapRefusesExpiredRequestAuthority(t *testing.T) {
	t.Parallel()
	fx := newChannelCatalogFixture(t)
	visible := fx.createChannel(t, "setup-gap", ChannelActive)
	fx.grant(t, visible.ID, fx.readerSubject(), true, false, false, nil)
	fx.reconcile(t)

	// The setup gap, bought outright instead of paid for in 4510 rows: the clock
	// the guard reads runs ahead of the one the fixture sealed against, which is
	// what 290.76 s of -race seeding did to the race-modules runner. The existing
	// shift seam applies it to the observations a bound transaction makes AFTER
	// its first one, which is exactly where refreshNow re-validates freshness.
	//
	// 4m30s sits 30 s clear of BOTH edges, so neither half of this test depends on
	// how fast the box is: it is half a minute past the four real minutes the
	// constructor's seal is worth, and half a minute short of a five-minute window
	// anchored after setup.
	const setupClockGap = channelCatalogFixtureAuthorityWindow - 30*time.Second
	gap := &channelCatalogClockShiftData{inner: fx.m.data, shift: setupClockGap}
	fx.m.data = gap

	page, err := fx.list(t, ChannelCatalogRequest{Limit: 10})
	if !errors.Is(err, ErrCommunicationEvidenceUnknown) || len(page.Items) != 0 ||
		page.HasMore || page.Continuation != "" {
		t.Fatalf("authority sealed before the gap = %+v, %v; want refused with nothing disclosed",
			page, err)
	}
	// Named, not merely "an error occurred": a refusal from any OTHER guard — a
	// window that fails to overlap, a closure gone stale, a credential no longer
	// current — would leave the freshness check itself unmeasured.
	if !strings.Contains(err.Error(), "request authority is no longer fresh") {
		t.Fatalf("refusal = %v; want communicationTx.validateAuthorityFreshness by name", err)
	}

	// ⛔ THE HALF THAT KEEPS THE RENEWAL HONEST: THE GAP STAYS. The guard still
	// reads a clock 4m30s ahead; only the sealing instant moves, at the same TTL,
	// and the same read succeeds. So the refusal above was the seal and not the
	// estate.
	observedAt, freshUntil := fx.renewRequestAuthorityEvidence(t)
	if freshUntil.Sub(observedAt) != channelCatalogFixtureAuthorityWindow {
		t.Fatalf("renewed window = %s, want the fixture TTL %s unchanged",
			freshUntil.Sub(observedAt), channelCatalogFixtureAuthorityWindow)
	}
	page, err = fx.list(t, ChannelCatalogRequest{Limit: 10})
	if err != nil || !reflect.DeepEqual(channelCatalogIDs(page), []model.ID{visible.ID}) ||
		page.HasMore || page.Continuation != "" {
		t.Fatalf("renewed authority page = %+v, %v; want [%s] terminal", page, err, visible.ID)
	}

	// ⛔ AND ONE WINDOW IS ALL IT BUYS. Widen the gap past the renewed window and
	// the same guard refuses again, by the same name: the renewal did not disarm
	// it, exempt this fixture from it, or pin a window open.
	gap.shift = channelCatalogFixtureAuthorityWindow + time.Minute
	page, err = fx.list(t, ChannelCatalogRequest{Limit: 10})
	if !errors.Is(err, ErrCommunicationEvidenceUnknown) || len(page.Items) != 0 ||
		page.HasMore || page.Continuation != "" {
		t.Fatalf("gap beyond the renewed window = %+v, %v; want refused again", page, err)
	}
	if !strings.Contains(err.Error(), "request authority is no longer fresh") {
		t.Fatalf("second refusal = %v; want the same freshness guard by name", err)
	}
}

func TestChannelCatalogGenericBatchRefusesCrossedQuestionsAndReuse(t *testing.T) {
	t.Parallel()
	fx := newChannelCatalogFixture(t)
	channel := fx.createChannel(t, "batch", ChannelActive)
	other := fx.createChannel(t, "batch-other", ChannelActive)
	fx.reconcile(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	identity, err := fx.m.bindCurrentCommunicationIdentity(ctx, fx.scope, fx.readerRef, requireChannelCatalogPrincipal)
	if err != nil {
		t.Fatalf("bind identity: %v", err)
	}
	question := func(scope DirectoryScopeRef, kind model.Kind, id model.ID, op CommunicationOperation) communicationAuthorityQuestion {
		q, err := newCommunicationAuthorityQuestion(scope, kind, id, op)
		if err != nil {
			t.Fatalf("question: %v", err)
		}
		return q
	}
	good := question(fx.scope, channelKind, channel.ID, CommunicationRead)
	second := question(fx.scope, channelKind, other.ID, CommunicationRead)
	foreignWorkspace := question(DirectoryScopeRef{TenantID: fx.tenant, WorkspaceID: model.NewID()}, channelKind, channel.ID, CommunicationRead)
	foreignTenant := question(DirectoryScopeRef{TenantID: model.TenantID(model.NewID()), WorkspaceID: fx.workspace}, channelKind, channel.ID, CommunicationRead)

	for name, questions := range map[string][]communicationAuthorityQuestion{
		"crossed workspace": {good, foreignWorkspace},
		"crossed tenant":    {good, foreignTenant},
		"repeated entity":   {good, good},
		"empty":             {},
	} {
		if _, _, err := bindCommunicationAuthorityBatch(ctx, identity, requireChannelCatalogPrincipal, questions); err == nil {
			t.Fatalf("%s batch bound", name)
		}
	}
	deny := func(CommunicationPrincipal) error { return errors.New("refused") }
	if _, _, err := bindCommunicationAuthorityBatch(ctx, identity, deny, []communicationAuthorityQuestion{good}); err == nil {
		t.Fatal("refusing principal requirement bound a batch")
	}
	drifted, cancelDrifted := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancelDrifted()
	if _, _, err := bindCommunicationAuthorityBatch(drifted, identity, requireChannelCatalogPrincipal, []communicationAuthorityQuestion{good}); err == nil {
		t.Fatal("deadline drift bound a batch")
	}

	batch, admitted, err := bindCommunicationAuthorityBatch(ctx, identity, requireChannelCatalogPrincipal, []communicationAuthorityQuestion{good, second})
	if err != nil || !reflect.DeepEqual(admitted, []int{0, 1}) {
		t.Fatalf("bind = %v, %v", admitted, err)
	}
	if _, _, err := batch.transactionSnapshot([]communicationAuthorityQuestion{second, good}); err == nil {
		t.Fatal("reordered consumption accepted")
	}
	if _, _, err := batch.transactionSnapshot([]communicationAuthorityQuestion{good}); err == nil {
		t.Fatal("partial consumption accepted")
	}
	if _, _, err := batch.transactionSnapshot([]communicationAuthorityQuestion{good, second}); err != nil {
		t.Fatalf("exact consumption refused: %v", err)
	}
	if _, _, err := batch.transactionSnapshot([]communicationAuthorityQuestion{good, second}); err == nil {
		t.Fatal("second consumption accepted")
	}
	window, err := newCommunicationAuthorityWindow(fx.now, fx.now.Add(time.Minute))
	if err != nil {
		t.Fatalf("window: %v", err)
	}
	foreignScope := DirectoryScopeRef{TenantID: fx.tenant, WorkspaceID: model.NewID()}
	if err := fx.m.mutateCommunicationWithBoundAuthorityBatch(ctx, foreignScope, identity, requireChannelCatalogPrincipal,
		[]communicationAuthorityQuestion{good}, batch, window,
		func(*communicationTx, []communicationRequestAuthorityBatchContext) error { return nil },
	); err == nil {
		t.Fatal("crossed transaction scope accepted")
	}
	// The inbox adapter still refuses a candidate whose question is not its own
	// Delivery/read question.
	if err := fx.m.mutateCommunicationWithAuthorityBatch(ctx, fx.scope, identity,
		[]communicationInboxAuthorityCandidate{{candidate: directNoticeInboxCandidate{DeliveryID: channel.ID, DeliverySeq: 1}, question: good}},
		batch, window,
		func(*communicationTx, []communicationRequestAuthorityBatchContext) error { return nil },
	); err == nil {
		t.Fatal("inbox adapter accepted a Channel question")
	}
}

func TestMergeChannelCatalogProjectionCutsAtTheTruncatedBatch(t *testing.T) {
	t.Parallel()
	id := func(suffix string) string { return "00000000-0000-7000-8000-00000000000" + suffix }
	pages := []store.DistinctPage{
		{Values: []string{id("1"), id("4")}, HasMore: true},
		{Values: []string{id("2"), id("3"), id("9")}, HasMore: false},
		{Values: []string{id("4"), id("7")}, HasMore: false},
	}
	merged, hasMore, err := mergeChannelCatalogProjection(pages, "", 10)
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	if !hasMore || fmt.Sprint(merged) != fmt.Sprint([]model.ID{model.ID(id("1")), model.ID(id("2")), model.ID(id("3")), model.ID(id("4"))}) {
		t.Fatalf("merge = %v has_more=%t; values beyond the truncated batch's last value must wait", merged, hasMore)
	}
	limited, hasMore, err := mergeChannelCatalogProjection(pages[1:], "", 2)
	if err != nil || !hasMore || len(limited) != 2 || limited[0] != model.ID(id("2")) || limited[1] != model.ID(id("3")) {
		t.Fatalf("limited merge = %v %t %v", limited, hasMore, err)
	}
	if _, _, err := mergeChannelCatalogProjection([]store.DistinctPage{{Values: []string{"not-an-id"}}}, "", 2); err == nil {
		t.Fatal("malformed projection row accepted")
	}
	if _, _, err := mergeChannelCatalogProjection([]store.DistinctPage{{Values: []string{id("2")}}}, model.ID(id("5")), 2); err == nil {
		t.Fatal("row at or before the anchor accepted")
	}
}

// TestChannelCatalogProjectionAcrossBackends exercises the grant-first store
// projection on SQLite and PostgreSQL through the module's own seam: NULL
// expiry, just-expired grants, duplicates through several closure subjects,
// ordering, keyset anchor, subject batching beyond one statement, and
// workspace/tenant isolation.
func TestChannelCatalogProjectionAcrossBackends(t *testing.T) {
	t.Parallel()
	for _, backend := range communicationSchemaBackends(t) {
		backend := backend
		t.Run(backend.name, func(t *testing.T) {
			t.Parallel()
			// The store clock sits an hour behind real time so an "already expired"
			// grant can carry an expiry after its stamped created_at yet before the
			// engine clock the projection observes.
			now := time.Now().UTC()
			fixture := communicationOpenFixtureWithClock(t, backend, &testClock{now: now.Add(-time.Hour)})
			ctx := context.Background()
			scope := DirectoryScopeRef{TenantID: fixture.tenant, WorkspaceID: fixture.workspace}
			var otherWorkspace model.ID
			if err := fixture.st.Mutate(ctx, fixture.tenant, func(sc store.Scope) error {
				created, err := sc.Workspaces().Create(ctx, model.Workspace{
					Name: "Other", Slug: "other-projection", Status: model.StatusActive,
				})
				otherWorkspace = created.ID
				return err
			}); err != nil {
				t.Fatalf("other workspace: %v", err)
			}
			user := CommunicationSubjectRef{Kind: SubjectUser, Ref: model.NewID().String()}
			group := CommunicationSubjectRef{Kind: SubjectUserGroup, Ref: model.NewID().String()}
			stranger := CommunicationSubjectRef{Kind: SubjectUser, Ref: model.NewID().String()}
			type seed struct {
				channel   model.ID
				workspace model.ID
				subject   CommunicationSubjectRef
				read      bool
				state     ChannelGrantState
				expires   *time.Time
			}
			channels := make([]model.ID, 6)
			for index := range channels {
				channels[index] = model.NewID()
			}
			past := now.Add(-time.Minute)
			future := now.Add(time.Hour)
			createdAt := now.Add(-time.Hour)
			_ = createdAt
			seeds := []seed{
				{channels[0], fixture.workspace, user, true, ChannelGrantActive, nil},
				{channels[0], fixture.workspace, group, true, ChannelGrantActive, &future},
				{channels[1], fixture.workspace, user, true, ChannelGrantActive, &past},
				{channels[1], fixture.workspace, group, true, ChannelGrantActive, nil},
				{channels[2], fixture.workspace, user, true, ChannelGrantActive, &future},
				{channels[3], fixture.workspace, user, false, ChannelGrantActive, nil},
				{channels[3], fixture.workspace, stranger, true, ChannelGrantActive, nil},
				{channels[4], fixture.workspace, user, true, ChannelGrantRevoked, nil},
				{channels[5], otherWorkspace, user, true, ChannelGrantActive, nil},
			}
			if err := fixture.st.Mutate(ctx, fixture.tenant, func(sc store.Scope) error {
				channelRepo, err := sc.Ext(channelKind)
				if err != nil {
					return err
				}
				for index, channelID := range channels {
					workspace := fixture.workspace
					if index == 5 {
						workspace = otherWorkspace
					}
					if _, err := channelRepo.CreateWithID(ctx, channelID, communicationChannelRecord(
						workspace, fmt.Sprintf("projection-%d", index),
					)); err != nil {
						return err
					}
				}
				repo, err := sc.Ext(channelGrantKind)
				if err != nil {
					return err
				}
				for _, s := range seeds {
					grant := ChannelGrant{
						MutableCommunicationEntity: MutableCommunicationEntity{
							CommunicationEntity: CommunicationEntity{
								ID: model.NewID(), TenantID: fixture.tenant, WorkspaceID: s.workspace,
								Version: 1, CreatedAt: createdAt,
							},
							UpdatedAt: createdAt,
						},
						ChannelID: s.channel, Subject: s.subject, Generation: 1, CanRead: s.read,
						// A grant without the read bit still carries a bit: it is write-only.
						CanWrite: !s.read,
						State:    s.state, GrantedBy: CommunicationActorRef{Kind: ActorUser, Ref: model.NewID().String()},
						ExpiresAt: s.expires,
					}
					if s.state == ChannelGrantRevoked {
						grant.RevokedBy = &CommunicationActorRef{Kind: ActorUser, Ref: model.NewID().String()}
					}
					record, err := channelGrantToRecord(grant)
					if err != nil {
						return err
					}
					if _, err := repo.CreateWithID(ctx, grant.ID, record); err != nil {
						return err
					}
				}
				return nil
			}); err != nil {
				t.Fatalf("seed grants: %v", err)
			}
			project := func(subjects []CommunicationSubjectRef, after model.ID, limit int) channelCatalogProjectionPage {
				t.Helper()
				page, err := fixture.m.projectChannelCatalogCandidates(ctx, scope, subjects, after, limit)
				if err != nil {
					t.Fatalf("project: %v", err)
				}
				return page
			}
			expected := []model.ID{channels[0], channels[1], channels[2]}
			if got := project([]CommunicationSubjectRef{user, group}, "", 10); !reflect.DeepEqual(got.candidates, expected) || got.hasMore || got.observedAt.IsZero() {
				t.Fatalf("user+group projection = %+v, want %v (expired direct grant covered by the group, write-only and revoked hidden)", got, expected)
			}
			if got := project([]CommunicationSubjectRef{user}, "", 10); !reflect.DeepEqual(got.candidates, []model.ID{channels[0], channels[2]}) {
				t.Fatalf("user projection = %+v, want [0 2]", got)
			}
			if got := project([]CommunicationSubjectRef{user, group}, "", 2); !reflect.DeepEqual(got.candidates, expected[:2]) || !got.hasMore {
				t.Fatalf("limited projection = %+v, want first two with more", got)
			}
			if got := project([]CommunicationSubjectRef{user, group}, channels[1], 10); !reflect.DeepEqual(got.candidates, []model.ID{channels[2]}) || got.hasMore {
				t.Fatalf("anchored projection = %+v, want [2]", got)
			}
			if got := project([]CommunicationSubjectRef{stranger}, "", 10); !reflect.DeepEqual(got.candidates, []model.ID{channels[3]}) {
				t.Fatalf("stranger projection = %+v, want [3]", got)
			}
			// A closure wider than one statement's alternative bound is batched and
			// merged into the same ordered result.
			wide := make([]CommunicationSubjectRef, 0, 3*channelCatalogSubjectBatch)
			for index := 0; index < 3*channelCatalogSubjectBatch; index++ {
				wide = append(wide, CommunicationSubjectRef{Kind: SubjectUserGroup, Ref: model.NewID().String()})
			}
			wide[channelCatalogSubjectBatch-1] = user
			wide[2*channelCatalogSubjectBatch+5] = group
			if got := project(wide, "", 10); !reflect.DeepEqual(got.candidates, expected) || got.hasMore {
				t.Fatalf("batched wide projection = %+v, want %v", got, expected)
			}
			if got := project(wide, "", 1); !reflect.DeepEqual(got.candidates, expected[:1]) || !got.hasMore {
				t.Fatalf("batched limited projection = %+v, want [0] with more", got)
			}
			otherScope := DirectoryScopeRef{TenantID: fixture.tenant, WorkspaceID: otherWorkspace}
			got, err := fixture.m.projectChannelCatalogCandidates(ctx, otherScope, []CommunicationSubjectRef{user}, "", 10)
			if err != nil || !reflect.DeepEqual(got.candidates, []model.ID{channels[5]}) {
				t.Fatalf("other workspace projection = %+v, %v; want [5]", got, err)
			}
		})
	}
}

// TestChannelCatalogAuthorityAcrossBackends runs the exact-authority catalog on
// every configured backend: visibility, bits, pagination and a revocation
// between pages must read the same on SQLite and PostgreSQL.
func TestChannelCatalogAuthorityAcrossBackends(t *testing.T) {
	t.Parallel()
	for _, backend := range communicationSchemaBackends(t) {
		backend := backend
		t.Run(backend.name, func(t *testing.T) {
			t.Parallel()
			fx := newChannelCatalogFixtureOn(t, backend)
			readOnly := fx.createChannel(t, "cb-read", ChannelActive)
			fx.grant(t, readOnly.ID, fx.readerSubject(), true, false, false, nil)
			hidden := fx.createChannel(t, "cb-hidden", ChannelActive)
			fx.grant(t, hidden.ID, fx.readerSubject(), false, true, true, nil)
			readWrite := fx.createChannel(t, "cb-read-write", ChannelActive)
			grantRW := fx.grant(t, readWrite.ID, fx.readerSubject(), true, true, false, nil)
			fx.reconcile(t)
			first, err := fx.list(t, ChannelCatalogRequest{Limit: 1})
			if err != nil || !reflect.DeepEqual(channelCatalogIDs(first), []model.ID{readOnly.ID}) || !first.HasMore {
				t.Fatalf("%s page one = %+v, %v", backend.name, first, err)
			}
			second, err := fx.list(t, ChannelCatalogRequest{Limit: 1, Continuation: first.Continuation})
			if err != nil || !reflect.DeepEqual(channelCatalogIDs(second), []model.ID{readWrite.ID}) ||
				second.HasMore || second.Items[0].MyAccess != (ChannelCatalogAccess{Read: true, Write: true}) {
				t.Fatalf("%s page two = %+v, %v", backend.name, second, err)
			}
			fx.revokeGrant(t, grantRW)
			third, err := fx.list(t, ChannelCatalogRequest{Limit: 5})
			if err != nil || !reflect.DeepEqual(channelCatalogIDs(third), []model.ID{readOnly.ID}) || third.HasMore {
				t.Fatalf("%s after revocation = %+v, %v", backend.name, third, err)
			}
		})
	}
}

var _ atomic.Int64
