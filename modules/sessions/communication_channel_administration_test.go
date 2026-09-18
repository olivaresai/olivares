// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/engine/enginetest"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// TestChannelAdministrationStateFilterIsClosed pins the catalog's state
// selection as a CLOSED set: three named values, each admitting exactly the
// stored states it names, and anything else admitting nothing at all.
func TestChannelAdministrationStateFilterIsClosed(t *testing.T) {
	t.Parallel()
	cases := map[ChannelAdministrationStateFilter]map[ChannelState]bool{
		ChannelAdministrationStateAll:      {ChannelActive: true, ChannelArchived: true},
		ChannelAdministrationStateActive:   {ChannelActive: true, ChannelArchived: false},
		ChannelAdministrationStateArchived: {ChannelActive: false, ChannelArchived: true},
	}
	if len(cases) != len(channelAdministrationStateFilters()) {
		t.Fatalf("the closed set has %d members, the table covers %d",
			len(channelAdministrationStateFilters()), len(cases))
	}
	for filter, expected := range cases {
		if !filter.Valid() {
			t.Fatalf("declared filter %q is not valid", filter)
		}
		for state, want := range expected {
			if filter.admits(state) != want {
				t.Fatalf("%q.admits(%q) = %t, want %t", filter, state, filter.admits(state), want)
			}
		}
	}
	for _, unknown := range []ChannelAdministrationStateFilter{"", "deleted", "ALL", "active ", "any"} {
		if unknown.Valid() {
			t.Fatalf("unknown filter %q accepted", unknown)
		}
		for _, state := range []ChannelState{ChannelActive, ChannelArchived} {
			if unknown.admits(state) {
				t.Fatalf("unknown filter %q admitted %q", unknown, state)
			}
		}
	}
	// Archived is inspectable and remains terminal: nothing in this surface maps
	// a filter back onto a state transition.
	if !ChannelAdministrationStateAll.admits(ChannelArchived) ||
		!ChannelAdministrationStateArchived.admits(ChannelArchived) {
		t.Fatal("archived Channels must remain inspectable")
	}
}

// TestChannelGrantAdministrationStateFilterBindsThePersistedColumn pins the
// sheet's state selection to the STORED column and to nothing temporal.
func TestChannelGrantAdministrationStateFilterBindsThePersistedColumn(t *testing.T) {
	t.Parallel()
	cases := map[ChannelGrantAdministrationStateFilter]struct {
		state   ChannelGrantState
		bounded bool
	}{
		ChannelGrantAdministrationStateActive:  {ChannelGrantActive, true},
		ChannelGrantAdministrationStateRevoked: {ChannelGrantRevoked, true},
		ChannelGrantAdministrationStateExpired: {ChannelGrantExpired, true},
		ChannelGrantAdministrationStateAll:     {"", false},
	}
	if len(cases) != len(channelGrantAdministrationStateFilters()) {
		t.Fatalf("the closed set has %d members, the table covers %d",
			len(channelGrantAdministrationStateFilters()), len(cases))
	}
	for filter, expected := range cases {
		if !filter.Valid() {
			t.Fatalf("declared filter %q is not valid", filter)
		}
		state, bounded := filter.persistedState()
		if bounded != expected.bounded || state != expected.state {
			t.Fatalf("%q.persistedState() = (%q,%t), want (%q,%t)",
				filter, state, bounded, expected.state, expected.bounded)
		}
	}
	for _, unknown := range []ChannelGrantAdministrationStateFilter{"", "pending", "ACTIVE", "any"} {
		if unknown.Valid() {
			t.Fatalf("unknown grant filter %q accepted", unknown)
		}
		if _, bounded := unknown.persistedState(); bounded {
			t.Fatalf("unknown grant filter %q bound a state predicate", unknown)
		}
	}
}

// TestChannelGrantTemporalStateDescribesTheRowNotThePermission pins the derived
// value, including the equality boundary, and proves the STORED state is never
// rewritten by the derivation.
func TestChannelGrantTemporalStateDescribesTheRowNotThePermission(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, time.September, 6, 12, 0, 0, 0, time.UTC)
	past := now.Add(-time.Minute)
	future := now.Add(time.Minute)
	actor := CommunicationActorRef{Kind: ActorUser, Ref: model.NewID().String()}
	cases := []struct {
		name  string
		grant ChannelGrant
		want  ChannelGrantTemporalState
	}{
		{"active without expiry", ChannelGrant{State: ChannelGrantActive}, ChannelGrantTemporalActive},
		{"active before expiry", ChannelGrant{State: ChannelGrantActive, ExpiresAt: &future}, ChannelGrantTemporalActive},
		{"active exactly at expiry", ChannelGrant{State: ChannelGrantActive, ExpiresAt: &now}, ChannelGrantTemporalExpired},
		{"active past expiry", ChannelGrant{State: ChannelGrantActive, ExpiresAt: &past}, ChannelGrantTemporalExpired},
		{"revoked stays revoked", ChannelGrant{State: ChannelGrantRevoked, RevokedBy: &actor}, ChannelGrantTemporalRevoked},
		{"revoked with a future expiry stays revoked", ChannelGrant{State: ChannelGrantRevoked, RevokedBy: &actor, ExpiresAt: &future}, ChannelGrantTemporalRevoked},
		{"expired stays expired", ChannelGrant{State: ChannelGrantExpired, ExpiresAt: &past}, ChannelGrantTemporalExpired},
	}
	for _, test := range cases {
		stored := test.grant.State
		if got := channelGrantTemporalState(test.grant, now); got != test.want {
			t.Fatalf("%s: temporal state = %q, want %q", test.name, got, test.want)
		}
		if test.grant.State != stored {
			t.Fatalf("%s: the derivation rewrote the stored state", test.name)
		}
	}
}

// TestChannelGrantAdministrationFiltersAreRecheckedAfterTheLock proves the Go
// re-check refuses exactly the rows the store predicate would have excluded, so
// a row that changed identity between the list and the lock is not reported.
func TestChannelGrantAdministrationFiltersAreRecheckedAfterTheLock(t *testing.T) {
	t.Parallel()
	subject := CommunicationSubjectRef{Kind: SubjectUser, Ref: model.NewID().String()}
	other := CommunicationSubjectRef{Kind: SubjectUser, Ref: model.NewID().String()}
	active := ChannelGrant{State: ChannelGrantActive, Subject: subject}
	revoked := ChannelGrant{State: ChannelGrantRevoked, Subject: subject}
	if !channelGrantMatchesAdministrationFilters(active, ChannelGrantAdministrationStateActive, subject, true) {
		t.Fatal("the matching row was refused")
	}
	if channelGrantMatchesAdministrationFilters(revoked, ChannelGrantAdministrationStateActive, subject, true) {
		t.Fatal("a row whose stored state left the selection was reported")
	}
	if channelGrantMatchesAdministrationFilters(active, ChannelGrantAdministrationStateActive, other, true) {
		t.Fatal("a row of another subject was reported under an exact-subject filter")
	}
	if !channelGrantMatchesAdministrationFilters(revoked, ChannelGrantAdministrationStateAll, CommunicationSubjectRef{}, false) {
		t.Fatal("state=all with no subject filter refused a legitimate row")
	}
	if channelGrantMatchesAdministrationFilters(
		ChannelGrant{State: "bogus", Subject: subject}, ChannelGrantAdministrationStateAll, subject, true,
	) {
		t.Fatal("a row with an unknown stored state was reported")
	}
}

// TestChannelAdministrationRefusesInvalidNavigationBeforeAnyAuthority proves the
// closed navigation contract is enforced before a credential is even rebound: an
// invalid selection is an invalid request, not an empty page and not a 503.
func TestChannelAdministrationRefusesInvalidNavigationBeforeAnyAuthority(t *testing.T) {
	t.Parallel()
	m := New()
	scope := DirectoryScopeRef{TenantID: model.TenantID(model.NewID()), WorkspaceID: model.NewID()}
	ref := auth.PrincipalRef{}
	channelID := model.NewID()
	longToken := string(make([]byte, communicationCursorTokenMaxBytes+1))

	for name, request := range map[string]ChannelAdministrationRequest{
		"unknown state":     {State: "deleted"},
		"negative limit":    {Limit: -1},
		"limit above bound": {Limit: channelAdministrationMaximumLimit + 1},
		"oversized token":   {Continuation: longToken},
	} {
		_, err := m.listAdministrableChannels(context.Background(), scope, ref, request, false)
		if !errors.Is(err, ErrInvalidCommunicationModel) {
			t.Fatalf("administration %s = %v, want an invalid request", name, err)
		}
	}
	for name, request := range map[string]ChannelGrantAdministrationRequest{
		"unknown state":     {ChannelID: channelID, State: "pending"},
		"negative limit":    {ChannelID: channelID, Limit: -1},
		"limit above bound": {ChannelID: channelID, Limit: channelGrantAdministrationMaximumLimit + 1},
		"oversized token":   {ChannelID: channelID, Continuation: longToken},
		"malformed subject": {ChannelID: channelID, Subject: CommunicationSubjectRef{Kind: SubjectUser, Ref: "not-a-uuid"}, HasSubject: true},
		"partial subject":   {ChannelID: channelID, Subject: CommunicationSubjectRef{Kind: SubjectUser}},
	} {
		_, err := m.listChannelGrantAdministration(context.Background(), scope, ref, request, false)
		if !errors.Is(err, ErrInvalidCommunicationModel) {
			t.Fatalf("grant sheet %s = %v, want an invalid request", name, err)
		}
	}
	// A non-canonical Channel identifier is CONCEALED as not found, exactly like
	// the read point read: the route must not become an enumeration oracle.
	_, err := m.listChannelGrantAdministration(context.Background(), scope, ref,
		ChannelGrantAdministrationRequest{ChannelID: "not-a-uuid"}, false)
	if !errors.Is(err, ErrCommunicationNotFound) {
		t.Fatalf("non-canonical Channel id = %v, want a concealed not-found", err)
	}
}

// TestChannelAdministrationPrincipalRequirementRefusesSystemCredentials proves
// the administrative surfaces admit directory principals only. A system
// principal is refused at binding time and again at batch time, so a surface
// cannot widen what it admitted after the credential was re-resolved.
func TestChannelAdministrationPrincipalRequirementRefusesSystemCredentials(t *testing.T) {
	t.Parallel()
	system := CommunicationPrincipal{System: true, SystemGrantAgentID: model.NewID()}
	if err := requireChannelAdministrationPrincipal(system); !errors.Is(err, ErrCommunicationForbidden) {
		t.Fatalf("system principal admitted: %v", err)
	}
	if err := requireChannelAdministrationPrincipal(CommunicationPrincipal{}); !errors.Is(err, ErrCommunicationForbidden) {
		t.Fatalf("empty principal admitted: %v", err)
	}
	user := CommunicationPrincipal{UserID: model.NewID()}
	if ValidateCommunicationPrincipal(user) == nil {
		if err := requireChannelAdministrationPrincipal(user); err != nil {
			t.Fatalf("a valid directory principal was refused: %v", err)
		}
	}
}

// TestChannelAdministrationClosureValidationFailsClosed proves an unusable
// grant-subject closure is EVIDENCE UNAVAILABLE and never an empty page: an
// unknown outcome, a malformed subject, a repeated subject, an over-bound
// closure and an allow-without-subjects are each refused.
func TestChannelAdministrationClosureValidationFailsClosed(t *testing.T) {
	t.Parallel()
	subject := CommunicationSubjectRef{Kind: SubjectUser, Ref: model.NewID().String()}
	for name, closure := range map[string]ChannelGrantSubjectClosure{
		"unknown outcome":       {Outcome: ReadUnknown},
		"invalid outcome":       {Outcome: "sideways"},
		"malformed subject":     {Outcome: ReadAllow, Subjects: []CommunicationSubjectRef{{Kind: SubjectUser, Ref: "nope"}}},
		"repeated subject":      {Outcome: ReadAllow, Subjects: []CommunicationSubjectRef{subject, subject}},
		"allow with no subject": {Outcome: ReadAllow},
	} {
		if err := validateChannelAdministrationClosure(closure); !errors.Is(err, ErrCommunicationEvidenceUnknown) {
			t.Fatalf("%s = %v, want evidence unavailable", name, err)
		}
	}
	if err := validateChannelAdministrationClosure(ChannelGrantSubjectClosure{
		Outcome: ReadAllow, Subjects: []CommunicationSubjectRef{subject},
	}); err != nil {
		t.Fatalf("a usable closure was refused: %v", err)
	}
	// A current DENY closure is USABLE evidence — it means "nothing is
	// administrable here" — and the caller answers an empty page with it.
	if err := validateChannelAdministrationClosure(ChannelGrantSubjectClosure{
		Outcome: ReadDeny,
	}); err != nil {
		t.Fatalf("a current DENY closure was refused: %v", err)
	}
	over := make([]CommunicationSubjectRef, channelAdministrationClosureBound+1)
	for index := range over {
		over[index] = CommunicationSubjectRef{Kind: SubjectUser, Ref: model.NewID().String()}
	}
	if err := validateChannelAdministrationClosure(ChannelGrantSubjectClosure{
		Outcome: ReadAllow, Subjects: over,
	}); !errors.Is(err, ErrCommunicationEvidenceUnknown) {
		t.Fatalf("an over-bound closure was accepted: %v", err)
	}
}

// TestChannelSnapshotChangedIsItsOwnWireMeaning proves the paged-history
// conflict is a distinct sentinel with a distinct code, and that it is not
// reachable by reinterpreting an ordinary store conflict.
func TestChannelSnapshotChangedIsItsOwnWireMeaning(t *testing.T) {
	t.Parallel()
	err := communicationError(
		ErrCommunicationChannelSnapshotChanged, "channel revision changed between administration pages",
	)
	status, code, verdict, ok := communicationHTTPDisposition(err)
	if !ok || status != 409 || code != "channel_snapshot_changed" || verdict != VerdictBroken {
		t.Fatalf("disposition = (%d, %q, %q, %t)", status, code, verdict, ok)
	}
	if errors.Is(err, ErrCommunicationTerminal) {
		t.Fatal("the snapshot conflict collapsed into the terminal conflict")
	}
	// An ordinary terminal conflict keeps its own code.
	if _, code, _, _ := communicationHTTPDisposition(
		communicationError(ErrCommunicationTerminal, "terminal"),
	); code != "terminal" {
		t.Fatalf("terminal conflict code = %q", code)
	}
}

// TestAdministrativeReadsHaveNoUnrelatedActiveGrantCeiling is the causal
// reproducer for the ceiling K3-IR-02 measured on the frozen source.
//
// THE DEFECT. Both administrative closures used to resolve current authority with
// lockCurrentChannelGrants, which reads EVERY active grant of the Channel and
// answers UNKNOWN above the shared 4,096-row bound. So a reader holding one admin
// grant, asking for one row, filtered to its own subject, could be refused with
// `active ChannelGrant snapshot exceeds bound` by 4,096 grants belonging to people
// it will never see. Nothing about that reader, that request or that page was
// unbounded: the ceiling was a property of OTHER subjects.
//
// THE WITNESS. One admin grant for the reader, one for a group the reader is in,
// and `directNoticeGrantSetBound` unrelated ACTIVE grants on the same Channel.
// Both readers must answer, and answer with the reader's own rows.
//
// It runs on every configured engine and SAYS which: the ceiling lived in shared
// Go, so SQLite alone would be a weak claim about a store-independent defect.
func TestAdministrativeReadsHaveNoUnrelatedActiveGrantCeiling(t *testing.T) {
	backends := administrationReaderBackends(t)
	engines := make([]string, 0, len(backends))
	for _, backend := range backends {
		engines = append(engines, backend.name)
	}
	t.Logf("K3_UNRELATED_ACTIVE_SET_ENGINES|%s", strings.Join(engines, ","))
	for _, backend := range backends {
		backend := backend
		t.Run(backend.name, func(t *testing.T) {
			fx := newChannelCatalogFixtureOn(t, backend)
			// The reader's closure is its own subject PLUS a group: the scoped read
			// must consult every subject the resolver produced, not just the direct
			// one, or a group-granted administrator would silently lose access.
			group := CommunicationSubjectRef{Kind: SubjectUserGroup, Ref: model.NewID().String()}
			fx.closure.subjectsByUser[fx.readerID] = []CommunicationSubjectRef{
				fx.readerSubject(), group,
			}
			channel := fx.createChannel(t, "administration-active-set-bound", ChannelActive)
			readerGrant := fx.grant(t, channel.ID, fx.readerSubject(), false, false, true, nil)
			groupGrant := fx.grant(t, channel.ID, group, false, false, true, nil)

			unrelated := seedUnrelatedActiveChannelGrants(t, fx, channel.ID, directNoticeGrantSetBound)
			fx.reconcile(t)

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
			defer cancel()

			// The SHEET, filtered to the reader's own subject, one row.
			sheet, err := fx.m.listChannelGrantAdministrationWithAuthority(
				ctx, fx.scope, fx.readerRef, ChannelGrantAdministrationRequest{
					ChannelID: channel.ID, State: ChannelGrantAdministrationStateAll,
					Subject: fx.readerSubject(), HasSubject: true, Limit: 1,
				},
			)
			if err != nil {
				t.Fatalf("K3_UNRELATED_ACTIVE_SET_CEILING|surface=sheet|unrelated=%d|selected=1|err=%v",
					unrelated, err)
			}
			if len(sheet.Items) != 1 || sheet.Items[0].Grant.ID != readerGrant ||
				sheet.Items[0].Grant.Subject != fx.readerSubject() {
				t.Fatalf("filtered first page = %+v, want only the reader's own generation", sheet.Items)
			}
			if sheet.Channel.ID != channel.ID || sheet.ETag == "" {
				t.Fatalf("sheet Channel/etag = %+v / %q", sheet.Channel.ID, sheet.ETag)
			}

			// The CATALOG closed the same authority read per selected Channel.
			catalog, err := fx.m.listAdministrableChannelsWithAuthority(
				ctx, fx.scope, fx.readerRef, ChannelAdministrationRequest{Limit: 1},
			)
			if err != nil {
				t.Fatalf("K3_UNRELATED_ACTIVE_SET_CEILING|surface=catalog|unrelated=%d|err=%v",
					unrelated, err)
			}
			if len(catalog.Items) != 1 || catalog.Items[0].Channel.ID != channel.ID {
				t.Fatalf("administrative catalog = %+v, want the one administrable Channel", catalog.Items)
			}

			// The GROUP path alone still administers, and it is listed SECOND behind a
			// decoy subject that holds nothing on this Channel. That ordering is the
			// point: a scoped read that consulted only the first closure subject — the
			// obvious way to make this bounded — would answer "no admin grant" here.
			decoy := CommunicationSubjectRef{Kind: SubjectUser, Ref: model.NewID().String()}
			fx.closure.subjectsByUser[fx.readerID] = []CommunicationSubjectRef{decoy, group}
			viaGroup, err := fx.m.listChannelGrantAdministrationWithAuthority(
				ctx, fx.scope, fx.readerRef, ChannelGrantAdministrationRequest{
					ChannelID: channel.ID, State: ChannelGrantAdministrationStateAll,
					Subject: group, HasSubject: true, Limit: 1,
				},
			)
			if err != nil {
				t.Fatalf("group-only administration = %v", err)
			}
			if len(viaGroup.Items) != 1 || viaGroup.Items[0].Grant.ID != groupGrant {
				t.Fatalf("group-only page = %+v, want the group generation", viaGroup.Items)
			}

			// And a closure with NO grant on this Channel is still refused: the scoped
			// read narrows what is looked at, never what is required.
			fx.closure.subjectsByUser[fx.readerID] = []CommunicationSubjectRef{decoy}
			if _, err := fx.m.listChannelGrantAdministrationWithAuthority(
				ctx, fx.scope, fx.readerRef, ChannelGrantAdministrationRequest{
					ChannelID: channel.ID, State: ChannelGrantAdministrationStateAll, Limit: 1,
				},
			); !errors.Is(err, ErrCommunicationNotFound) {
				t.Fatalf("a closure with no grant on this Channel = %v, want a concealed not-found", err)
			}
		})
	}
}

// seedUnrelatedActiveChannelGrants writes `count` ACTIVE grants for subjects the
// reader's closure never contains. They are a fixture for cardinality, and they
// are the exact rows the administrative readers must no longer read.
func seedUnrelatedActiveChannelGrants(
	t *testing.T,
	fx channelCatalogFixture,
	channelID model.ID,
	count int,
) int {
	t.Helper()
	if err := fx.m.data.Mutate(context.Background(), fx.tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(channelGrantKind)
		if err != nil {
			return err
		}
		for index := 0; index < count; index++ {
			record := model.Record{
				colWorkWorkspaceID:   fx.workspace.String(),
				colCommChannelID:     channelID.String(),
				colCommSubjectKind:   string(SubjectUser),
				colCommSubjectRef:    model.NewID().String(),
				colCommGeneration:    int64(1),
				colCommCanRead:       true,
				colCommCanWrite:      false,
				colCommCanAdmin:      false,
				colCommState:         string(ChannelGrantActive),
				colCommGrantedByKind: string(ActorUser),
				colCommGrantedByRef:  fx.sender.String(),
			}
			if _, err := repo.CreateWithID(context.Background(), model.NewID(), record); err != nil {
				return fmt.Errorf("seed unrelated active grant %d: %w", index, err)
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("seed %d unrelated active grants: %v", count, err)
	}
	return count
}

// TestChannelAdministrationAuthorityGrantQueryIsSubjectBoundAndIndexed pins the
// EXACT store query the scoped authority read binds, without a database.
//
// It is the structural half of the K3-IR-02 correction, and it now pins the term
// the FIRST correction left out. Binding the right columns was never the whole
// question: with no Sort the store renders `ORDER BY id ASC`, and a PostgreSQL
// planner answers that from an ordered primary-key scan while every equality
// column degrades to a row filter. So this test pins BOTH halves — the equality
// prefix AND an ordering the primary key cannot serve — and it pins them against
// the index the composed tree actually declares.
//
// It also pins that the query is the WRITER's, term for term. The administrative
// mutation asks the same question about the same rows; two spellings of one
// predicate is how one caller's measured plan stops describing the other's.
func TestChannelAdministrationAuthorityGrantQueryIsSubjectBoundAndIndexed(t *testing.T) {
	t.Parallel()
	channelID := model.NewID()
	subject := CommunicationSubjectRef{Kind: SubjectUserGroup, Ref: model.NewID().String()}
	query := channelAdministrationAuthorityGrantQuery(channelID, subject)

	want := append([]model.Filter{
		{Column: colCommChannelID, Op: model.OpEq, Value: channelID.String()},
	}, currentChannelGrantSubjectFilters(subject)...)
	if !reflect.DeepEqual(query.Filters, want) {
		t.Fatalf("authority grant query filters = %+v, want %+v", query.Filters, want)
	}
	// Spelled out once, so a future edit to the shared helper that silently
	// dropped a term would fail here rather than widen this read.
	if !reflect.DeepEqual(query.Filters, []model.Filter{
		{Column: colCommChannelID, Op: model.OpEq, Value: channelID.String()},
		{Column: colCommSubjectKind, Op: model.OpEq, Value: string(subject.Kind)},
		{Column: colCommSubjectRef, Op: model.OpEq, Value: subject.Ref},
		{Column: colCommState, Op: model.OpEq, Value: string(ChannelGrantActive)},
	}) {
		t.Fatalf("the shared subject predicate changed shape: %+v", query.Filters)
	}
	if query.Limit != channelAdministrationSubjectGrantBound+1 {
		t.Fatalf("authority grant query limit = %d, want the per-subject bound plus the "+
			"truncation probe", query.Limit)
	}
	if query.Cursor != "" || query.IncludeDeleted {
		t.Fatalf("authority grant query carries navigation state it must not: %+v", query)
	}

	// ⛔ THE ORDERING, WHICH IS THE DEFECT K3-IR-02 REPRODUCED.
	if !reflect.DeepEqual(query.Sort, currentChannelGrantSubjectOrder()) {
		t.Fatalf("authority grant query sort = %+v, want the writer's %+v",
			query.Sort, currentChannelGrantSubjectOrder())
	}
	if len(query.Sort) == 0 {
		t.Fatal("an unsorted query renders ORDER BY id ASC, which a primary-key scan " +
			"satisfies while filtering the whole relation — that is the measured defect")
	}
	if query.Sort[0].Column == model.ColID {
		t.Fatalf("the leading sort term is %q: an id-led ordering is exactly what the "+
			"primary key serves for free", query.Sort[0].Column)
	}

	// The equality prefix the ENGINE binds is wider than this query's filters:
	// the generic repository forces tenant_id and the confined workspace scope
	// forces workspace_id, both ahead of everything named here.
	bound := []string{model.ColTenantID, colWorkWorkspaceID}
	for _, filter := range query.Filters {
		bound = append(bound, filter.Column)
	}
	sorted := make([]string, 0, len(query.Sort))
	for _, term := range query.Sort {
		sorted = append(sorted, term.Column)
	}
	reg := communicationCaptureSchema(t)
	descriptor := communicationDescriptor(t, reg, channelGrantKind)
	// sessions_channel_grant_subject_current is the index the composed writer
	// declares for this exact predicate. It must lead with every bound equality
	// column, in order, and CONTINUE with the sort terms — an index that binds the
	// equalities but not the ordering leaves the substitution available.
	current := channelAdministrationIndex(t, descriptor, "sessions_channel_grant_subject_current")
	if len(current.Columns) < len(bound)+len(sorted) {
		t.Fatalf("sessions_channel_grant_subject_current = %v, too short for the bound "+
			"prefix %v plus the ordering %v", current.Columns, bound, sorted)
	}
	if !reflect.DeepEqual(current.Columns[:len(bound)], bound) {
		t.Fatalf("sessions_channel_grant_subject_current columns %v do not lead with the "+
			"bound equality prefix %v", current.Columns, bound)
	}
	if !reflect.DeepEqual(current.Columns[len(bound):len(bound)+len(sorted)], sorted) {
		t.Fatalf("sessions_channel_grant_subject_current continues with %v, not the "+
			"ordering terms %v", current.Columns[len(bound):len(bound)+len(sorted)], sorted)
	}

	// ⚠ AND THE INDEX IS NOT THE GUARANTEE. Every claim above is about what the
	// engine COULD do. What it does on a populated relation is measured, per
	// engine, with rows examined, in
	// TestAdministrativeAuthorityReadCostIsBoundedAmidForeignEstate. This test
	// exists to make that measurement reproducible, not to replace it.
	//
	// The pre-existing indexes the first correction cited are still declared and
	// still bind the subject, but neither of them binds the ordering: naming them
	// as the reason this read is bounded is what the review called an overstated
	// index claim, and it was.
	for _, name := range []string{
		"sessions_channel_grant_uniq", "sessions_channel_grant_subject_history",
	} {
		declared := channelAdministrationIndex(t, descriptor, name)
		if len(declared.Columns) == 0 {
			t.Fatalf("%s disappeared from the descriptor", name)
		}
	}
}

// administrationReaderBackends is the engine set both R2 reader witnesses run on:
// SQLite always, and — when a server is configured — a SPLIT-OWNER PostgreSQL
// database, which is the topology the product supports rather than the
// single-role convenience one. It announces which engines it got, so an exit 0
// with PostgreSQL unset cannot be read as a two-engine result.
func administrationReaderBackends(t *testing.T) []communicationSchemaBackend {
	t.Helper()
	backends := []communicationSchemaBackend{{
		name: "sqlite", engineName: store.EngineSQLite,
		dsn: filepath.Join(t.TempDir(), "administration-readers.db"),
	}}
	if enginetest.PostgresAvailable(t) {
		backends = append(backends,
			splitOwnerPostgresBackendForTest(t, "postgres-split-owner"))
	} else {
		required, err := strconv.ParseBool(
			strings.TrimSpace(os.Getenv("OLIVARES_TEST_POSTGRES_REQUIRED")))
		if err == nil && required {
			t.Fatal("the administrative reader witnesses are required on PostgreSQL for this run " +
				"and no isolated server is available")
		}
		t.Logf("%s unset: split-owner PostgreSQL NOT exercised", enginetest.EnvSuperuserDSN)
	}
	return backends
}
