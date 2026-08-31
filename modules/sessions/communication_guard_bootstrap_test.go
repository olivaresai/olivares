// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func TestCommunicationGuardReconcilePlanner(t *testing.T) {
	t.Parallel()

	tenant := model.TenantID(model.NewID())
	workspace := model.NewID()
	now := time.Date(2026, time.August, 15, 12, 0, 0, 0, time.UTC)
	guard := CommunicationGuard{
		MutableCommunicationEntity: MutableCommunicationEntity{
			CommunicationEntity: CommunicationEntity{
				ID: model.NewID(), TenantID: tenant, WorkspaceID: workspace,
				Version: 4, CreatedAt: now.Add(-time.Hour),
			},
			UpdatedAt: now,
		},
		Kind: CommunicationGuardRouteRevision, NextSeq: 7, LastDBTime: now,
	}

	t.Run("fresh max plus one", func(t *testing.T) {
		plan, err := planCommunicationGuardReconcile(communicationGuardReconcileInput{
			Kind: CommunicationGuardRouteRevision, Tenant: tenant, Workspace: workspace,
			ObservedMax: 0, DBNow: now, CreateID: model.NewID(),
			Mode: CommunicationGuardReconcileStaged,
		})
		if err != nil || plan.Action != communicationGuardCreate ||
			plan.After.NextSeq != 1 || plan.After.Version != 1 || plan.Required != 1 {
			t.Fatalf("fresh plan = %+v, err %v", plan, err)
		}
	})

	t.Run("staged advances lagging guard", func(t *testing.T) {
		plan, err := planCommunicationGuardReconcile(communicationGuardReconcileInput{
			Kind: CommunicationGuardRouteRevision, Tenant: tenant, Workspace: workspace,
			Existing: &guard, ObservedMax: 9, LatestSourceDBTime: now,
			DBNow: now.Add(time.Second),
			Mode:  CommunicationGuardReconcileStaged,
		})
		if err != nil || plan.Action != communicationGuardUpdate ||
			plan.After.NextSeq != 10 || plan.After.Version != guard.Version+1 ||
			!plan.After.LastDBTime.Equal(now.Add(time.Second)) {
			t.Fatalf("advance plan = %+v, err %v", plan, err)
		}
		if guard.NextSeq != 7 || guard.Version != 4 {
			t.Fatalf("planner mutated input guard: %+v", guard)
		}
	})

	t.Run("ahead never descends", func(t *testing.T) {
		ahead := guard
		ahead.NextSeq = 20
		plan, err := planCommunicationGuardReconcile(communicationGuardReconcileInput{
			Kind: CommunicationGuardRouteRevision, Tenant: tenant, Workspace: workspace,
			Existing: &ahead, ObservedMax: 9, LatestSourceDBTime: now,
			DBNow: now.Add(time.Second),
			Mode:  CommunicationGuardReconcileStaged,
		})
		if err != nil || plan.Action != communicationGuardNoop ||
			plan.After.NextSeq != 20 || plan.After.Version != ahead.Version {
			t.Fatalf("ahead plan = %+v, err %v", plan, err)
		}
	})

	t.Run("ahead sequence still advances independent time high water mark", func(t *testing.T) {
		ahead := guard
		ahead.NextSeq = 20
		latestSource := now.Add(time.Second)
		dbNow := now.Add(2 * time.Second)
		plan, err := planCommunicationGuardReconcile(communicationGuardReconcileInput{
			Kind: CommunicationGuardRouteRevision, Tenant: tenant, Workspace: workspace,
			Existing: &ahead, ObservedMax: 2, LatestSourceDBTime: latestSource,
			DBNow: dbNow, Mode: CommunicationGuardReconcileStaged,
		})
		if err != nil || plan.Action != communicationGuardUpdate ||
			plan.After.NextSeq != ahead.NextSeq || plan.After.Version != ahead.Version+1 ||
			!plan.After.LastDBTime.Equal(dbNow) || !plan.After.UpdatedAt.Equal(dbNow) {
			t.Fatalf("ahead temporal plan = %+v, err %v", plan, err)
		}
		if ahead.NextSeq != 20 || ahead.Version != guard.Version ||
			!ahead.LastDBTime.Equal(now) {
			t.Fatalf("temporal planner mutated input guard: %+v", ahead)
		}

		_, err = planCommunicationGuardReconcile(communicationGuardReconcileInput{
			Kind: CommunicationGuardRouteRevision, Tenant: tenant, Workspace: workspace,
			Existing: &ahead, ObservedMax: 2, LatestSourceDBTime: latestSource,
			DBNow: dbNow, Mode: CommunicationGuardReconcileEnforced,
		})
		if !errors.Is(err, ErrCommunicationEvidenceUnknown) {
			t.Fatalf("enforced temporal lag = %v, want evidence unknown", err)
		}
	})

	for _, test := range []struct {
		name     string
		input    communicationGuardReconcileInput
		wantBase error
	}{
		{
			name: "enforced missing",
			input: communicationGuardReconcileInput{
				Kind: CommunicationGuardRouteRevision, Tenant: tenant, Workspace: workspace,
				ObservedMax: 0, DBNow: now, Mode: CommunicationGuardReconcileEnforced,
			},
			wantBase: ErrCommunicationEvidenceUnknown,
		},
		{
			name: "enforced behind",
			input: communicationGuardReconcileInput{
				Kind: CommunicationGuardRouteRevision, Tenant: tenant, Workspace: workspace,
				Existing: &guard, ObservedMax: 7, LatestSourceDBTime: now, DBNow: now,
				Mode: CommunicationGuardReconcileEnforced,
			},
			wantBase: ErrCommunicationEvidenceUnknown,
		},
		{
			name: "source overflow",
			input: communicationGuardReconcileInput{
				Kind: CommunicationGuardRouteRevision, Tenant: tenant, Workspace: workspace,
				Existing: &guard, ObservedMax: math.MaxInt64, DBNow: now,
				Mode: CommunicationGuardReconcileStaged,
			},
			wantBase: ErrInvalidCommunicationTransition,
		},
		{
			name: "required value would exhaust sequence",
			input: communicationGuardReconcileInput{
				Kind: CommunicationGuardRouteRevision, Tenant: tenant, Workspace: workspace,
				Existing: &guard, ObservedMax: math.MaxInt64 - 1,
				LatestSourceDBTime: now, DBNow: now,
				Mode: CommunicationGuardReconcileStaged,
			},
			wantBase: ErrInvalidCommunicationTransition,
		},
		{
			name: "database clock rollback",
			input: communicationGuardReconcileInput{
				Kind: CommunicationGuardRouteRevision, Tenant: tenant, Workspace: workspace,
				Existing: &guard, ObservedMax: 6, LatestSourceDBTime: now,
				DBNow: now.Add(-time.Nanosecond),
				Mode:  CommunicationGuardReconcileStaged,
			},
			wantBase: ErrCommunicationEvidenceUnknown,
		},
		{
			name: "guard sequence exhausted",
			input: communicationGuardReconcileInput{
				Kind: CommunicationGuardRouteRevision, Tenant: tenant, Workspace: workspace,
				Existing: func() *CommunicationGuard {
					exhausted := guard
					exhausted.NextSeq = math.MaxInt64
					return &exhausted
				}(),
				ObservedMax: 6, LatestSourceDBTime: now, DBNow: now,
				Mode: CommunicationGuardReconcileStaged,
			},
			wantBase: ErrCommunicationEvidenceUnknown,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			plan, err := planCommunicationGuardReconcile(test.input)
			if !errors.Is(err, test.wantBase) || plan != (communicationGuardReconcilePlan{}) {
				t.Fatalf("plan = %+v, err %v, want %v", plan, err, test.wantBase)
			}
		})
	}
}

func TestCommunicationWorkspaceInitializerSeedsExactlyTwoGuardsAcrossBackends(t *testing.T) {
	t.Parallel()

	for _, backend := range communicationSchemaBackends(t) {
		backend := backend
		t.Run(backend.name, func(t *testing.T) {
			fixture := communicationOpenFixture(t, backend)
			communicationAssertGuardSet(t, fixture.m, fixture.tenant, fixture.workspace, 1, 1)

			ctx := context.Background()
			var secondWorkspace model.ID
			if err := fixture.m.data.Mutate(ctx, fixture.tenant, func(scope store.Scope) error {
				workspace, err := scope.Workspaces().Create(ctx, model.Workspace{
					Name: "Second", Slug: "second", Status: model.StatusActive,
				})
				secondWorkspace = workspace.ID
				return err
			}); err != nil {
				t.Fatalf("create second workspace: %v", err)
			}
			communicationAssertGuardSet(t, fixture.m, fixture.tenant, secondWorkspace, 1, 1)

			var secondTenant model.TenantID
			var secondDefault model.ID
			if err := fixture.st.System(ctx, func(system store.SystemScope) error {
				org, err := system.CreateOrg(ctx, model.Org{
					Name: "Second tenant", Slug: "second-guard-tenant", Status: model.StatusActive,
				})
				secondTenant = org.TenantID
				return err
			}); err != nil {
				t.Fatalf("create second tenant: %v", err)
			}
			if err := fixture.m.data.View(ctx, secondTenant, func(scope store.Scope) error {
				workspace, err := scope.DefaultWorkspace(ctx)
				secondDefault = workspace.ID
				return err
			}); err != nil {
				t.Fatalf("read second tenant default workspace: %v", err)
			}
			communicationAssertGuardSet(t, fixture.m, secondTenant, secondDefault, 1, 1)
		})
	}
}

func TestCommunicationGuardReconcileUsesSourceMaxAndEnforcedDoesNotRepairAcrossBackends(t *testing.T) {
	t.Parallel()

	for _, backend := range communicationSchemaBackends(t) {
		backend := backend
		t.Run(backend.name, func(t *testing.T) {
			fixture := communicationOpenFixture(t, backend)
			ctx := context.Background()
			channel := communicationMustCreate(t, fixture, channelKind,
				communicationChannelRecord(fixture.workspace, "guard-source"))
			messageID := model.NewID()
			message := communicationStagingMessageRecord(
				fixture.workspace, model.ID(channel.String(model.ColID)), messageID, "guard-source-message",
			)
			if _, err := communicationCreateWithID(
				ctx, fixture.m, fixture.tenant, messageKind, messageID, message,
			); err != nil {
				t.Fatalf("create guard source Message: %v", err)
			}
			if _, err := communicationCreateWithID(
				ctx, fixture.m, fixture.tenant, messageDeliveryKind, model.NewID(), model.Record{
					colWorkWorkspaceID: fixture.workspace.String(), colCommMessageID: messageID.String(),
					colCommRecipientKind: string(RecipientUser), colCommRecipientRef: model.NewID().String(),
					colCommRecipientEpoch: int64(1), colCommDeliverySeq: int64(7),
					colCommRequired: false, colCommRouteReasonsJSON: `["direct"]`,
					colCommWakePolicy: string(WakeNone), colCommState: string(DeliveryAvailable),
					colCommAvailableAt: model.NewTimestamp(communicationSchemaNow()).String(),
				},
			); err != nil {
				t.Fatalf("create guard source Delivery: %v", err)
			}

			if err := fixture.m.VerifyCommunicationGuards(ctx, fixture.tenant); !errors.Is(err, ErrCommunicationEvidenceUnknown) {
				t.Fatalf("enforced verification of lagging guards = %v, want evidence unknown", err)
			}
			// Enforced is verify-only: both seeded rows remain at one.
			communicationAssertGuardSet(t, fixture.m, fixture.tenant, fixture.workspace, 1, 1)

			if err := fixture.m.ReconcileCommunicationGuards(
				ctx, fixture.tenant, CommunicationGuardReconcileStaged,
			); err != nil {
				t.Fatalf("staged communication guard reconcile: %v", err)
			}
			communicationAssertGuardSet(t, fixture.m, fixture.tenant, fixture.workspace, 2, 8)
			if err := fixture.m.VerifyCommunicationGuards(ctx, fixture.tenant); err != nil {
				t.Fatalf("enforced verification after reconcile: %v", err)
			}
		})
	}
}

func TestCommunicationGuardSourceFloorAndLatestTimeAreIndependentSQLite(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	base := time.Now().UTC()
	future := base.Add(7 * 24 * time.Hour)

	t.Run("tied maximum route revision still observes latest row", func(t *testing.T) {
		clock := &testClock{now: base}
		fixture := communicationOpenFixtureWithClock(t, communicationSchemaBackend{
			name: "sqlite", engineName: store.EngineSQLite,
			dsn: filepath.Join(t.TempDir(), "guard-route-latest.db"),
		}, clock)
		communicationMustCreate(t, fixture, channelKind,
			communicationChannelRecord(fixture.workspace, "route-old"))
		clock.set(future)
		communicationMustCreate(t, fixture, channelKind,
			communicationChannelRecord(fixture.workspace, "route-future"))

		observation := communicationObserveGuardSource(
			t, fixture, channelKind,
		)
		if observation.Max != 1 || !observation.LatestDBTime.Equal(future) {
			t.Fatalf("route observation = %+v, want max=1 latest=%s", observation, future)
		}
		if err := fixture.m.ReconcileCommunicationGuards(
			ctx, fixture.tenant, CommunicationGuardReconcileStaged,
		); !errors.Is(err, ErrCommunicationEvidenceUnknown) {
			t.Fatalf("route clock rollback reconcile = %v, want evidence unknown", err)
		}
		communicationAssertGuardSet(t, fixture.m, fixture.tenant, fixture.workspace, 1, 1)
	})

	t.Run("lower delivery sequence updated later still fences clock", func(t *testing.T) {
		clock := &testClock{now: base}
		fixture := communicationOpenFixtureWithClock(t, communicationSchemaBackend{
			name: "sqlite", engineName: store.EngineSQLite,
			dsn: filepath.Join(t.TempDir(), "guard-delivery-latest.db"),
		}, clock)
		channel := communicationMustCreate(t, fixture, channelKind,
			communicationChannelRecord(fixture.workspace, "delivery-source"))
		messageID := model.NewID()
		message := communicationStagingMessageRecord(
			fixture.workspace, model.ID(channel.String(model.ColID)), messageID, "delivery-source",
		)
		message[colCommAvailableAt] = model.NewTimestamp(base).String()
		if _, err := communicationCreateWithID(
			ctx, fixture.m, fixture.tenant, messageKind, messageID,
			message,
		); err != nil {
			t.Fatalf("create source Message: %v", err)
		}
		createDelivery := func(deliveryMessageID model.ID, sequence int64, availableAt time.Time) model.Record {
			t.Helper()
			created, err := communicationCreateWithID(
				ctx, fixture.m, fixture.tenant, messageDeliveryKind, model.NewID(), model.Record{
					colWorkWorkspaceID: fixture.workspace.String(), colCommMessageID: deliveryMessageID.String(),
					colCommRecipientKind: string(RecipientUser), colCommRecipientRef: model.NewID().String(),
					colCommRecipientEpoch: int64(1), colCommDeliverySeq: sequence,
					colCommRequired: false, colCommRouteReasonsJSON: `["direct"]`,
					colCommWakePolicy: string(WakeNone), colCommState: string(DeliveryAvailable),
					colCommAvailableAt: model.NewTimestamp(availableAt).String(),
				},
			)
			if err != nil {
				t.Fatalf("create delivery sequence %d: %v", sequence, err)
			}
			return created
		}
		createDelivery(messageID, 7, base)
		olderDelivery := createDelivery(messageID, 3, base)
		clock.set(future)
		if _, err := communicationUpdate(
			ctx, fixture.m, fixture.tenant, messageDeliveryKind, olderDelivery,
		); err != nil {
			t.Fatalf("update older delivery at future clock: %v", err)
		}

		observation := communicationObserveGuardSource(
			t, fixture, messageDeliveryKind,
		)
		if observation.Max != 7 || !observation.LatestDBTime.Equal(future) {
			t.Fatalf("delivery observation = %+v, want max=7 latest=%s", observation, future)
		}
		if err := fixture.m.ReconcileCommunicationGuards(
			ctx, fixture.tenant, CommunicationGuardReconcileStaged,
		); !errors.Is(err, ErrCommunicationEvidenceUnknown) {
			t.Fatalf("delivery clock rollback reconcile = %v, want evidence unknown", err)
		}
		communicationAssertGuardSet(t, fixture.m, fixture.tenant, fixture.workspace, 1, 1)
	})
}

func TestCommunicationGuardReconcilePersistsTimeHWMWhenSequenceIsAheadSQLite(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	fixture := newDirectNoticeFixture(t)
	clock, ok := fixture.m.clock.(*testClock)
	if !ok {
		t.Fatalf("direct notice clock = %T, want *testClock", fixture.m.clock)
	}
	// SQLite's production TransactionClock deliberately reads the engine wall
	// clock. Wrap only this test's ModuleData so it can replay the database-clock
	// sequence t0→t2→t3→t1 while retaining real SQLite transactions, constraints,
	// workspace confinement and row locks. Its guard-repo adapter maps stamped
	// writes to the store's same injected clock, preserving the same-time invariant.
	t0 := directNoticeDeliveryGuard(t, fixture).LastDBTime
	t1, t2, t3 := t0.Add(time.Second), t0.Add(2*time.Second), t0.Add(3*time.Second)
	clock.set(t0)
	rollbackData := &communicationGuardRollbackClockData{
		inner: api.NewModuleData(fixture.st), clock: clock,
	}
	fixture.m.UseData(rollbackData)
	fixture.m.UseCommunicationGuardReconciliationData(
		NewCommunicationGuardReconciliationData(rollbackData),
	)

	// Seed a low historical delivery at t0, then put its guard well ahead. The
	// later t2 update must advance the temporal HWM even though max(seq)+1 remains
	// below this guard's NextSeq.
	messageID := model.NewID()
	messageRecord := communicationStagingMessageRecord(
		fixture.workspace, fixture.channel.ID, messageID, "temporal-hwm-source",
	)
	messageRecord[colCommAvailableAt] = model.NewTimestamp(t0).String()
	if _, err := communicationCreateWithID(
		ctx, fixture.m, fixture.tenant, messageKind, messageID,
		messageRecord,
	); err != nil {
		t.Fatalf("create temporal-HWM source Message: %v", err)
	}
	lowDelivery, err := communicationCreateWithID(
		ctx, fixture.m, fixture.tenant, messageDeliveryKind, model.NewID(), model.Record{
			colWorkWorkspaceID: fixture.workspace.String(), colCommMessageID: messageID.String(),
			colCommRecipientKind: string(RecipientUser), colCommRecipientRef: fixture.recipient.String(),
			colCommRecipientEpoch: fixture.epoch, colCommDeliverySeq: int64(1),
			colCommRequired: false, colCommRouteReasonsJSON: `["direct"]`,
			colCommWakePolicy: string(WakeNone), colCommState: string(DeliveryAvailable),
			colCommAvailableAt: model.NewTimestamp(t0).String(),
		},
	)
	if err != nil {
		t.Fatalf("create low-sequence temporal-HWM Delivery: %v", err)
	}
	if err := fixture.m.data.Mutate(ctx, fixture.tenant, func(raw store.Scope) error {
		confined, err := store.ConfineWorkspace(ctx, raw, fixture.workspace)
		if err != nil {
			return err
		}
		txClock, ok := confined.(store.TransactionClock)
		if !ok {
			return errors.New("temporal-HWM fixture lacks transaction clock")
		}
		dbNow, err := txClock.TransactionNow(ctx)
		if err != nil {
			return err
		}
		repo, err := confined.Ext(communicationGuardKind)
		if err != nil {
			return err
		}
		stamped, ok := repo.(store.TransactionStampedGenericRepo)
		if !ok {
			return errors.New("temporal-HWM guard repository lacks stamped writes")
		}
		rows, page, err := repo.List(ctx, model.Query{
			Filters: []model.Filter{{
				Column: colCommGuardKind, Op: model.OpEq,
				Value: string(CommunicationGuardDeliverySequence),
			}},
			Limit: 2,
		})
		if err != nil {
			return err
		}
		if page.HasMore || len(rows) != 1 {
			return fmt.Errorf("delivery guard rows = %d more=%t, want one", len(rows), page.HasMore)
		}
		guard, err := communicationGuardFromRecord(rows[0])
		if err != nil {
			return err
		}
		beforeVersion := guard.Version
		guard.Version++
		guard.NextSeq = 20
		guard.UpdatedAt = dbNow.Time()
		guard.LastDBTime = dbNow.Time()
		record, err := communicationGuardToRecord(guard)
		if err != nil {
			return err
		}
		record[model.ColVersion] = beforeVersion
		_, err = stamped.UpdateAtTransactionTime(ctx, record)
		return err
	}); err != nil {
		t.Fatalf("put delivery guard ahead at t0: %v", err)
	}
	before := directNoticeDeliveryGuard(t, fixture)
	if before.NextSeq != 20 || !before.LastDBTime.Equal(t0) {
		t.Fatalf("ahead delivery guard = %+v, want next=20/last=t0", before)
	}

	clock.set(t2)
	updatedLow, err := communicationUpdate(
		ctx, fixture.m, fixture.tenant, messageDeliveryKind, workSchemaClone(lowDelivery),
	)
	if err != nil {
		t.Fatalf("update low-sequence Delivery at t2: %v", err)
	}
	decodedLow, err := messageDeliveryFromRecord(updatedLow)
	if err != nil {
		t.Fatalf("decode updated low-sequence Delivery: %v", err)
	}
	if decodedLow.DeliverySeq != 1 || !decodedLow.UpdatedAt.Equal(t2) {
		t.Fatalf("updated low-sequence Delivery = %+v, want seq=1/updated=t2", decodedLow)
	}
	observation := communicationObserveGuardSource(t, fixture.communicationSchemaFixture, messageDeliveryKind)
	if observation.Max != 1 || !observation.LatestDBTime.Equal(t2) {
		t.Fatalf("low-sequence temporal observation = %+v, want max=1/latest=t2", observation)
	}

	clock.set(t3)
	if err := fixture.m.ReconcileCommunicationGuards(
		ctx, fixture.tenant, CommunicationGuardReconcileStaged,
	); err != nil {
		t.Fatalf("staged temporal-HWM reconcile at t3: %v", err)
	}
	after := directNoticeDeliveryGuard(t, fixture)
	if after.NextSeq != before.NextSeq || after.Version != before.Version+1 ||
		!after.LastDBTime.Equal(t3) || !after.UpdatedAt.Equal(t3) {
		t.Fatalf("temporal-HWM guard = %+v, before %+v", after, before)
	}
	if err := fixture.m.VerifyCommunicationGuards(ctx, fixture.tenant); err != nil {
		t.Fatalf("enforced verification after temporal-HWM reconcile: %v", err)
	}

	// The source row itself is not part of the direct-notice lock set. Therefore
	// only the persisted guard HWM can make this t1 rollback fail closed; with the
	// old sequence-only noop the publish would proceed.
	clock.set(t1)
	beforeMessages := len(communicationRowsForTest(t, fixture, messageKind))
	beforeDeliveries := len(communicationRowsForTest(t, fixture, messageDeliveryKind))
	beforeAudit := directNoticeAuditHead(t, fixture)
	_, publishErr := fixture.m.publishDirectNotice(
		ctx, fixture.scope, CommunicationPrincipal{UserID: fixture.sender},
		fixture.command(model.NewID(), "temporal-HWM rollback denial"),
	)
	if !errors.Is(publishErr, ErrCommunicationEvidenceUnknown) {
		t.Fatalf("publish after database-clock rollback = %v, want evidence unknown", publishErr)
	}
	finalGuard := directNoticeDeliveryGuard(t, fixture)
	finalAudit := directNoticeAuditHead(t, fixture)
	if !reflect.DeepEqual(finalGuard, after) || finalAudit.Seq != beforeAudit.Seq ||
		!reflect.DeepEqual(finalAudit.Hash, beforeAudit.Hash) ||
		len(communicationRowsForTest(t, fixture, messageKind)) != beforeMessages ||
		len(communicationRowsForTest(t, fixture, messageDeliveryKind)) != beforeDeliveries {
		t.Fatalf("rollback denial changed state: guard=%+v want=%+v audit=%+v want=%+v",
			finalGuard, after, finalAudit, beforeAudit)
	}
}

// communicationGuardRollbackClockData is a test-only database-clock fault
// injector. It retains the real ModuleData transaction and changes only the
// TransactionNow observation handed to communication code.
type communicationGuardRollbackClockData struct {
	inner api.ModuleData
	clock *testClock
}

func (d *communicationGuardRollbackClockData) View(
	ctx context.Context,
	tenant model.TenantID,
	fn func(store.Scope) error,
) error {
	return d.inner.View(ctx, tenant, fn)
}

func (d *communicationGuardRollbackClockData) Mutate(
	ctx context.Context,
	tenant model.TenantID,
	fn func(store.Scope) error,
) error {
	return d.inner.Mutate(ctx, tenant, func(raw store.Scope) error {
		locker, hasLocker := raw.(store.TransactionLocker)
		authority, hasAuthority := raw.(store.AuthoritySnapshotLocker)
		directory, hasDirectory := raw.(store.DirectorySnapshotReader)
		if !hasLocker || !hasAuthority || !hasDirectory {
			return errors.New("rollback clock fixture lacks required SQL transaction capabilities")
		}
		return fn(&communicationGuardRollbackClockScope{
			Scope: raw, clock: d.clock, locker: locker,
			authority: authority, directory: directory,
		})
	})
}

type communicationGuardRollbackClockScope struct {
	store.Scope
	clock     *testClock
	locker    store.TransactionLocker
	authority store.AuthoritySnapshotLocker
	directory store.DirectorySnapshotReader
}

var (
	_ store.TransactionClock        = (*communicationGuardRollbackClockScope)(nil)
	_ store.TransactionLocker       = (*communicationGuardRollbackClockScope)(nil)
	_ store.AuthoritySnapshotLocker = (*communicationGuardRollbackClockScope)(nil)
	_ store.DirectorySnapshotReader = (*communicationGuardRollbackClockScope)(nil)
)

func (s *communicationGuardRollbackClockScope) TransactionNow(
	context.Context,
) (model.Timestamp, error) {
	return s.clock.Now(), nil
}

func (s *communicationGuardRollbackClockScope) LockTransaction(
	ctx context.Context,
	key string,
) error {
	return s.locker.LockTransaction(ctx, key)
}

func (s *communicationGuardRollbackClockScope) LockAuthoritySnapshot(
	ctx context.Context,
	refs []store.AuthorizationFactRef,
) error {
	return s.authority.LockAuthoritySnapshot(ctx, refs)
}

func (s *communicationGuardRollbackClockScope) ReadDirectoryEpoch(
	ctx context.Context,
) (model.DirectoryEpoch, error) {
	return s.directory.ReadDirectoryEpoch(ctx)
}

func (s *communicationGuardRollbackClockScope) ReadDirectoryTombstone(
	ctx context.Context,
	ref store.DirectoryPrincipalRef,
) (store.DirectoryTombstoneWitness, bool, error) {
	return s.directory.ReadDirectoryTombstone(ctx, ref)
}

func (s *communicationGuardRollbackClockScope) Ext(
	kind model.Kind,
) (store.GenericRepo, error) {
	repo, err := s.Scope.Ext(kind)
	if err != nil || kind != communicationGuardKind {
		return repo, err
	}
	locker, ok := repo.(store.RowLocker[model.Record])
	if !ok {
		return nil, errors.New("rollback clock guard repository lacks row locks")
	}
	return &communicationGuardRollbackClockRepo{GenericRepo: repo, locker: locker}, nil
}

// communicationGuardRollbackClockRepo keeps domain/base timestamps equal in
// the fault-injected test: ordinary GenericRepo writes use the same injected
// store clock that TransactionNow above returns.
type communicationGuardRollbackClockRepo struct {
	store.GenericRepo
	locker store.RowLocker[model.Record]
}

var (
	_ store.TransactionStampedGenericRepo = (*communicationGuardRollbackClockRepo)(nil)
	_ store.RowLocker[model.Record]       = (*communicationGuardRollbackClockRepo)(nil)
)

func (r *communicationGuardRollbackClockRepo) Lock(
	ctx context.Context,
	id model.ID,
) (model.Record, error) {
	return r.locker.Lock(ctx, id)
}

func (r *communicationGuardRollbackClockRepo) CreateAtTransactionTime(
	ctx context.Context,
	record model.Record,
) (model.Record, error) {
	return r.Create(ctx, record)
}

func (r *communicationGuardRollbackClockRepo) CreateWithIDAtTransactionTime(
	ctx context.Context,
	id model.ID,
	record model.Record,
) (model.Record, error) {
	return r.CreateWithID(ctx, id, record)
}

func (r *communicationGuardRollbackClockRepo) UpdateAtTransactionTime(
	ctx context.Context,
	record model.Record,
) (model.Record, error) {
	return r.Update(ctx, record)
}

func communicationObserveGuardSource(
	t *testing.T,
	fixture communicationSchemaFixture,
	kind model.Kind,
) communicationGuardSourceObservation {
	t.Helper()
	ctx := context.Background()
	var observation communicationGuardSourceObservation
	if err := fixture.m.data.Mutate(ctx, fixture.tenant, func(raw store.Scope) error {
		confined, err := store.ConfineWorkspace(ctx, raw, fixture.workspace)
		if err != nil {
			return err
		}
		rawScope, ok := confined.(communicationGuardRawScope)
		if !ok {
			return errors.New("confined source scope lacks guard bootstrap capabilities")
		}
		scope, err := newCommunicationGuardNarrowScope(rawScope)
		if err != nil {
			return err
		}
		observation, err = observeCommunicationGuardSource(ctx, scope, kind)
		return err
	}); err != nil {
		t.Fatalf("observe guard source %s: %v", kind, err)
	}
	return observation
}

func TestCommunicationGuardReconcileRefusesExhaustedRequiredFloorSQLite(t *testing.T) {
	t.Parallel()

	backend := communicationSchemaBackend{
		name: "sqlite", engineName: store.EngineSQLite,
		dsn: filepath.Join(t.TempDir(), "guard-exhaustion.db"),
	}
	fixture := communicationOpenFixtureWithClock(t, backend, nil)
	ctx := context.Background()
	channel := communicationMustCreate(t, fixture, channelKind,
		communicationChannelRecord(fixture.workspace, "guard-exhaustion"))
	exhausted := workSchemaClone(channel)
	exhausted[colCommRouteRevision] = int64(math.MaxInt64 - 1)
	if _, err := communicationUpdate(
		ctx, fixture.m, fixture.tenant, channelKind, exhausted,
	); err != nil {
		t.Fatalf("raise Channel route revision to exhausted floor: %v", err)
	}
	if err := fixture.m.ReconcileCommunicationGuards(
		ctx, fixture.tenant, CommunicationGuardReconcileStaged,
	); !errors.Is(err, ErrInvalidCommunicationTransition) {
		t.Fatalf("exhausted required floor reconcile = %v, want invalid transition", err)
	}
	communicationAssertGuardSet(t, fixture.m, fixture.tenant, fixture.workspace, 1, 1)
}

func TestCommunicationGuardUpgradeNeedsExplicitReconcileSQLite(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	dsn := filepath.Join(t.TempDir(), "upgrade-guards.db")
	liveRegistration := communicationCaptureSchema(t)
	legacy, err := engine.Open(ctx, store.Config{
		Engine: store.EngineSQLite, DSN: dsn, Debug: true,
	}, communicationK2Registration(t, liveRegistration))
	if err != nil {
		t.Fatalf("open pre-K3 SQLite store: %v", err)
	}
	var tenant model.TenantID
	var workspace model.ID
	if err := legacy.System(ctx, func(system store.SystemScope) error {
		if _, err := system.EnsureSystemTenant(ctx); err != nil {
			return err
		}
		org, err := system.CreateOrg(ctx, model.Org{
			Name: "Upgrade", Slug: "guard-upgrade", Status: model.StatusActive,
		})
		tenant = org.TenantID
		return err
	}); err != nil {
		_ = legacy.Close()
		t.Fatalf("seed pre-K3 tenant: %v", err)
	}
	if err := legacy.View(ctx, tenant, func(scope store.Scope) error {
		value, err := scope.DefaultWorkspace(ctx)
		workspace = value.ID
		return err
	}); err != nil {
		_ = legacy.Close()
		t.Fatalf("read pre-K3 default workspace: %v", err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatalf("close pre-K3 store: %v", err)
	}

	module := New()
	upgraded, err := engine.Open(ctx, store.Config{
		Engine: store.EngineSQLite, DSN: dsn, Debug: true,
	}, module.RegisterSchema)
	if err != nil {
		t.Fatalf("open upgraded SQLite store: %v", err)
	}
	t.Cleanup(func() { _ = upgraded.Close() })
	module.UseData(api.NewModuleData(upgraded))
	module.UseCommunicationGuardReconciliationData(
		NewCommunicationGuardReconciliationData(api.NewModuleData(upgraded)),
	)
	if err := module.VerifyCommunicationGuards(ctx, tenant); !errors.Is(err, ErrCommunicationEvidenceUnknown) {
		t.Fatalf("upgrade verify before reconcile = %v, want evidence unknown", err)
	}
	communicationAssertGuardSet(t, module, tenant, workspace, 0, 0)
	if err := module.ReconcileCommunicationGuards(
		ctx, tenant, CommunicationGuardReconcileStaged,
	); err != nil {
		t.Fatalf("upgrade staged reconcile: %v", err)
	}
	communicationAssertGuardSet(t, module, tenant, workspace, 1, 1)
}

func TestCommunicationSchemaRegistersOneGuardWorkspaceInitializer(t *testing.T) {
	t.Parallel()

	registry := communicationCaptureSchema(t)
	if len(registry.initializers) != 1 {
		t.Fatalf("workspace initializers = %d, want exactly one", len(registry.initializers))
	}
	initializer := registry.initializers[0]
	if initializer.Key != communicationGuardWorkspaceInitializerKey || initializer.Initialize == nil {
		t.Fatalf("workspace initializer = %#v", initializer)
	}
}

func TestCommunicationGuardSourceIndexesAreExact(t *testing.T) {
	t.Parallel()

	registry := communicationCaptureSchema(t)
	for _, test := range []struct {
		kind    model.Kind
		name    string
		columns []string
		unique  bool
	}{
		{
			kind: channelKind, name: "sessions_channel_guard_route",
			columns: []string{model.ColTenantID, colWorkWorkspaceID, colCommRouteRevision, model.ColID},
		},
		{
			kind: channelKind, name: "sessions_channel_guard_time",
			columns: []string{model.ColTenantID, colWorkWorkspaceID, model.ColUpdatedAt, model.ColID},
		},
		{
			kind: messageDeliveryKind, name: "sessions_message_delivery_seq_uniq",
			columns: []string{model.ColTenantID, colWorkWorkspaceID, colCommDeliverySeq}, unique: true,
		},
		{
			kind: messageDeliveryKind, name: "sessions_message_delivery_guard_time",
			columns: []string{model.ColTenantID, colWorkWorkspaceID, model.ColUpdatedAt, model.ColID},
		},
	} {
		descriptor := communicationDescriptor(t, registry, test.kind)
		found := false
		for _, index := range descriptor.Indexes {
			if index.Name == test.name {
				found = true
				if index.Unique != test.unique || !reflect.DeepEqual(index.Columns, test.columns) {
					t.Errorf("%s = %+v, want unique=%t columns %v",
						test.name, index, test.unique, test.columns)
				}
			}
		}
		if !found {
			t.Errorf("%s descriptor lacks index %s", test.kind, test.name)
		}
	}
}

func TestCommunicationGuardSourceIndexesServeBoundedSQLiteProbes(t *testing.T) {
	t.Parallel()

	dsn := filepath.Join(t.TempDir(), "guard-source-query-plan.db")
	fixture := communicationOpenFixture(t, communicationSchemaBackend{
		name: "sqlite", engineName: store.EngineSQLite, dsn: dsn,
	})
	raw, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close() //nolint:errcheck

	for _, test := range []struct {
		table  string
		column string
		index  string
	}{
		{channelTable, colCommRouteRevision, "sessions_channel_guard_route"},
		{channelTable, model.ColUpdatedAt, "sessions_channel_guard_time"},
		{messageDeliveryTable, colCommDeliverySeq, "sessions_message_delivery_seq_uniq"},
		{messageDeliveryTable, model.ColUpdatedAt, "sessions_message_delivery_guard_time"},
	} {
		query := fmt.Sprintf(
			"EXPLAIN QUERY PLAN SELECT * FROM %s WHERE tenant_id = ? AND workspace_id = ? "+
				"ORDER BY %s DESC, id DESC LIMIT 2",
			test.table, test.column,
		)
		rows, err := raw.QueryContext(
			context.Background(), query, fixture.tenant.String(), fixture.workspace.String(),
		)
		if err != nil {
			t.Fatalf("explain %s/%s: %v", test.table, test.column, err)
		}
		var details []string
		for rows.Next() {
			var id, parent, unused int
			var detail string
			if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
				_ = rows.Close()
				t.Fatalf("scan explain %s/%s: %v", test.table, test.column, err)
			}
			details = append(details, detail)
		}
		if err := rows.Close(); err != nil {
			t.Fatal(err)
		}
		plan := strings.Join(details, "\n")
		if !strings.Contains(plan, "USING INDEX "+test.index) ||
			strings.Contains(plan, "USE TEMP B-TREE") {
			t.Errorf("%s/%s query plan is not bounded by %s:\n%s",
				test.table, test.column, test.index, plan)
		}
	}
}

func TestCommunicationGuardReconciliationDataIsClosureOnlyAndScopeNarrowSQLite(t *testing.T) {
	t.Parallel()

	fixture := communicationOpenFixtureWithClock(t, communicationSchemaBackend{
		name: "sqlite", engineName: store.EngineSQLite,
		dsn: filepath.Join(t.TempDir(), "guard-narrow-data.db"),
	}, nil)
	data := NewCommunicationGuardReconciliationData(api.NewModuleData(fixture.st))
	if data == nil {
		t.Fatal("guard reconciliation data is nil")
	}

	// The exported handle must retain only closed operations. In particular, a
	// future refactor must not add api.ModuleData (or another capability object)
	// as a field and recreate an authority escape hatch.
	typeOfData := reflect.TypeOf(*data)
	moduleDataType := reflect.TypeOf((*api.ModuleData)(nil)).Elem()
	for index := 0; index < typeOfData.NumField(); index++ {
		field := typeOfData.Field(index)
		if field.Type.Kind() != reflect.Func {
			t.Errorf("CommunicationGuardReconciliationData field %s has type %s, want closure", field.Name, field.Type)
		}
		if field.Type == moduleDataType || field.Type.Implements(moduleDataType) {
			t.Errorf("CommunicationGuardReconciliationData field %s retains api.ModuleData", field.Name)
		}
	}

	err := data.mutateWorkspace(
		context.Background(), fixture.tenant, fixture.workspace,
		func(scope communicationGuardBootstrapScope) error {
			if _, leaked := any(scope).(store.Scope); leaked {
				return errors.New("guard reconciliation adapter leaked store.Scope")
			}
			if _, err := scope.GuardRepository(); err != nil {
				return fmt.Errorf("allowed guard repository: %w", err)
			}
			for _, allowed := range []model.Kind{channelKind, messageDeliveryKind} {
				reader, err := scope.Source(allowed)
				if err != nil {
					return fmt.Errorf("allowed source %s: %w", allowed, err)
				}
				if _, writable := any(reader).(store.GenericRepo); writable {
					return fmt.Errorf("source %s leaked store.GenericRepo", allowed)
				}
				if _, stamped := any(reader).(store.TransactionStampedGenericRepo); stamped {
					return fmt.Errorf("source %s leaked transaction-stamped writes", allowed)
				}
			}
			// messageKind is registered in the same module, which proves the adapter
			// denies by its exact allowlist rather than delegating all sessions kinds.
			if _, err := scope.Source(messageKind); !errors.Is(err, store.ErrUnknownEntity) {
				return fmt.Errorf("alien source %s error = %v, want ErrUnknownEntity", messageKind, err)
			}
			return nil
		},
	)
	if err != nil {
		t.Fatalf("narrow guard mutation: %v", err)
	}
}

type communicationGuardOrderingRepo struct {
	store.TransactionStampedGenericRepo
	locker   store.RowLocker[model.Record]
	kindByID map[model.ID]CommunicationGuardKind
	steps    *[]string
}

func (r *communicationGuardOrderingRepo) Lock(
	ctx context.Context,
	id model.ID,
) (model.Record, error) {
	*r.steps = append(*r.steps, string(r.kindByID[id]))
	return r.locker.Lock(ctx, id)
}

type communicationGuardOrderingScope struct {
	base        communicationGuardBootstrapScope
	guardRepo   store.GenericRepo
	steps       *[]string
	sourceSorts *[]string
}

func (s *communicationGuardOrderingScope) Tenant() model.TenantID { return s.base.Tenant() }

func (s *communicationGuardOrderingScope) TransactionNow(
	ctx context.Context,
) (model.Timestamp, error) {
	*s.steps = append(*s.steps, "db_now")
	return s.base.TransactionNow(ctx)
}

func (s *communicationGuardOrderingScope) LockTransaction(
	ctx context.Context,
	key string,
) error {
	*s.steps = append(*s.steps, "bootstrap")
	return s.base.LockTransaction(ctx, key)
}

func (s *communicationGuardOrderingScope) GuardRepository() (store.GenericRepo, error) {
	return s.guardRepo, nil
}

func (s *communicationGuardOrderingScope) Source(
	kind model.Kind,
) (communicationGuardSourceReader, error) {
	reader, err := s.base.Source(kind)
	if err != nil {
		return nil, err
	}
	if kind == channelKind || kind == messageDeliveryKind {
		return &communicationGuardOrderingSourceRepo{
			communicationGuardSourceReader: reader, kind: kind, sourceSorts: s.sourceSorts,
		}, nil
	}
	return reader, nil
}

type communicationGuardOrderingSourceRepo struct {
	communicationGuardSourceReader
	kind        model.Kind
	sourceSorts *[]string
}

func (r *communicationGuardOrderingSourceRepo) List(
	ctx context.Context,
	query model.Query,
) ([]model.Record, model.Page, error) {
	column := ""
	if len(query.Sort) > 0 {
		column = query.Sort[0].Column
	}
	*r.sourceSorts = append(*r.sourceSorts, string(r.kind)+":"+column)
	if query.Limit != 1 || !query.IncludeDeleted || len(query.Sort) != 2 ||
		!query.Sort[0].Desc || query.Sort[1] != (model.Sort{Column: model.ColID, Desc: true}) {
		return nil, model.Page{}, fmt.Errorf("unbounded guard source query: %+v", query)
	}
	return r.communicationGuardSourceReader.List(ctx, query)
}

func TestCommunicationGuardReconcileLockAndClockOrderSQLite(t *testing.T) {
	t.Parallel()

	backend := communicationSchemaBackend{
		name: "sqlite", engineName: store.EngineSQLite,
		dsn: filepath.Join(t.TempDir(), "guard-order.db"),
	}
	fixture := communicationOpenFixtureWithClock(t, backend, nil)
	ctx := context.Background()
	var steps []string
	var sourceSorts []string
	if err := fixture.m.data.Mutate(ctx, fixture.tenant, func(raw store.Scope) error {
		confined, err := store.ConfineWorkspace(ctx, raw, fixture.workspace)
		if err != nil {
			return err
		}
		rawScope, ok := confined.(communicationGuardRawScope)
		if !ok {
			return errors.New("confined scope lacks guard bootstrap capabilities")
		}
		base, err := newCommunicationGuardNarrowScope(rawScope)
		if err != nil {
			return err
		}
		repo, err := base.GuardRepository()
		if err != nil {
			return err
		}
		stamped, ok := repo.(store.TransactionStampedGenericRepo)
		if !ok {
			return errors.New("guard repository lacks stamped writes")
		}
		locker, ok := repo.(store.RowLocker[model.Record])
		if !ok {
			return errors.New("guard repository lacks row locks")
		}
		rows, _, err := repo.List(ctx, model.Query{Limit: 3})
		if err != nil {
			return err
		}
		kindByID := make(map[model.ID]CommunicationGuardKind, len(rows))
		for _, row := range rows {
			guard, err := communicationGuardFromRecord(row)
			if err != nil {
				return err
			}
			kindByID[guard.ID] = guard.Kind
		}
		orderingRepo := &communicationGuardOrderingRepo{
			TransactionStampedGenericRepo: stamped,
			locker:                        locker, kindByID: kindByID, steps: &steps,
		}
		orderingScope := &communicationGuardOrderingScope{
			base: base, guardRepo: orderingRepo, steps: &steps, sourceSorts: &sourceSorts,
		}
		return reconcileCommunicationGuardsInScope(
			ctx, orderingScope, fixture.workspace, CommunicationGuardReconcileEnforced,
		)
	}); err != nil {
		t.Fatalf("verify with ordering recorder: %v", err)
	}
	want := []string{"bootstrap", "route_revision", "delivery_sequence", "db_now"}
	if len(steps) != len(want) {
		t.Fatalf("guard reconcile steps = %#v, want %#v", steps, want)
	}
	for index := range want {
		if steps[index] != want[index] {
			t.Fatalf("guard reconcile steps = %#v, want %#v", steps, want)
		}
	}
	wantSourceSorts := []string{
		string(channelKind) + ":" + colCommRouteRevision,
		string(channelKind) + ":" + model.ColUpdatedAt,
		string(messageDeliveryKind) + ":" + colCommDeliverySeq,
		string(messageDeliveryKind) + ":" + model.ColUpdatedAt,
	}
	if len(sourceSorts) != len(wantSourceSorts) {
		t.Fatalf("guard source sorts = %#v, want %#v", sourceSorts, wantSourceSorts)
	}
	for index := range wantSourceSorts {
		if sourceSorts[index] != wantSourceSorts[index] {
			t.Fatalf("guard source sorts = %#v, want %#v", sourceSorts, wantSourceSorts)
		}
	}
}

type communicationGuardFaultData struct {
	inner        api.ModuleData
	failMutateAt int
	mutateCalls  int
	viewCalls    int
	failure      error
}

func (d *communicationGuardFaultData) View(
	ctx context.Context,
	tenant model.TenantID,
	fn func(store.Scope) error,
) error {
	d.viewCalls++
	return d.inner.View(ctx, tenant, fn)
}

func (d *communicationGuardFaultData) Mutate(
	ctx context.Context,
	tenant model.TenantID,
	fn func(store.Scope) error,
) error {
	d.mutateCalls++
	if d.failMutateAt > 0 && d.mutateCalls == d.failMutateAt {
		return d.failure
	}
	return d.inner.Mutate(ctx, tenant, fn)
}

type communicationGuardPageProbe struct {
	pages     []communicationGuardPageProbeResult
	pageIndex int
	mutated   []model.ID
}

type communicationGuardPageProbeResult struct {
	workspaces []model.Workspace
	page       model.Page
}

func (d *communicationGuardPageProbe) listWorkspacePage(
	_ context.Context,
	_ model.TenantID,
	_ model.Query,
) ([]model.Workspace, model.Page, error) {
	if d.pageIndex >= len(d.pages) {
		return nil, model.Page{}, errors.New("unexpected workspace page request")
	}
	result := d.pages[d.pageIndex]
	d.pageIndex++
	return result.workspaces, result.page, nil
}

func (d *communicationGuardPageProbe) mutateWorkspace(
	_ context.Context,
	_ model.TenantID,
	workspace model.ID,
	_ func(communicationGuardBootstrapScope) error,
) error {
	d.mutated = append(d.mutated, workspace)
	return nil
}

func TestCommunicationGuardWorkspacePaginationRejectsCyclesAndDuplicates(t *testing.T) {
	t.Parallel()

	tenant := model.TenantID(model.NewID())
	workspaceA := model.Workspace{BaseFields: model.BaseFields{
		ID: model.NewID(), TenantID: tenant,
	}}
	workspaceB := model.Workspace{BaseFields: model.BaseFields{
		ID: model.NewID(), TenantID: tenant,
	}}
	workspaceC := model.Workspace{BaseFields: model.BaseFields{
		ID: model.NewID(), TenantID: tenant,
	}}
	for _, test := range []struct {
		name        string
		pages       []communicationGuardPageProbeResult
		wantMutated int
	}{
		{
			name: "cursor A B A cycle",
			pages: []communicationGuardPageProbeResult{
				{workspaces: []model.Workspace{workspaceA}, page: model.Page{HasMore: true, Cursor: "A"}},
				{workspaces: []model.Workspace{workspaceB}, page: model.Page{HasMore: true, Cursor: "B"}},
				{workspaces: []model.Workspace{workspaceC}, page: model.Page{HasMore: true, Cursor: "A"}},
			},
			wantMutated: 2,
		},
		{
			name: "workspace repeated across pages",
			pages: []communicationGuardPageProbeResult{
				{workspaces: []model.Workspace{workspaceA}, page: model.Page{HasMore: true, Cursor: "A"}},
				{workspaces: []model.Workspace{workspaceA}},
			},
			wantMutated: 1,
		},
		{
			name: "workspace repeated within page",
			pages: []communicationGuardPageProbeResult{
				{workspaces: []model.Workspace{workspaceA, workspaceA}},
			},
			wantMutated: 0,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			probe := &communicationGuardPageProbe{pages: test.pages}
			module := New()
			module.communicationGuardData = probe
			err := module.ReconcileCommunicationGuards(
				context.Background(), tenant, CommunicationGuardReconcileStaged,
			)
			if !errors.Is(err, ErrCommunicationEvidenceUnknown) {
				t.Fatalf("pagination error = %v, want evidence unknown", err)
			}
			if len(probe.mutated) != test.wantMutated {
				t.Fatalf("mutated workspaces = %v, want count %d", probe.mutated, test.wantMutated)
			}
		})
	}
}

func TestCommunicationGuardReconcileBoundsTransactionsAndPreservesProgressSQLite(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	dsn := filepath.Join(t.TempDir(), "guard-progress.db")
	liveRegistration := communicationCaptureSchema(t)
	legacy, err := engine.Open(ctx, store.Config{
		Engine: store.EngineSQLite, DSN: dsn, Debug: true,
	}, communicationK2Registration(t, liveRegistration))
	if err != nil {
		t.Fatalf("open pre-K3 SQLite store: %v", err)
	}
	var tenant model.TenantID
	if err := legacy.System(ctx, func(system store.SystemScope) error {
		if _, err := system.EnsureSystemTenant(ctx); err != nil {
			return err
		}
		org, err := system.CreateOrg(ctx, model.Org{
			Name: "Guard progress", Slug: "guard-progress", Status: model.StatusActive,
		})
		tenant = org.TenantID
		return err
	}); err != nil {
		_ = legacy.Close()
		t.Fatalf("seed legacy tenant: %v", err)
	}
	const extraWorkspaces = 1001
	if err := legacy.Mutate(ctx, tenant, func(scope store.Scope) error {
		for index := 0; index < extraWorkspaces; index++ {
			if _, err := scope.Workspaces().Create(ctx, model.Workspace{
				Name:   fmt.Sprintf("Workspace %04d", index),
				Slug:   fmt.Sprintf("workspace-%04d", index),
				Status: model.StatusActive,
			}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		_ = legacy.Close()
		t.Fatalf("seed %d legacy workspaces: %v", extraWorkspaces, err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatalf("close pre-K3 store: %v", err)
	}

	module := New()
	upgraded, err := engine.Open(ctx, store.Config{
		Engine: store.EngineSQLite, DSN: dsn, Debug: true,
	}, module.RegisterSchema)
	if err != nil {
		t.Fatalf("open upgraded store: %v", err)
	}
	t.Cleanup(func() { _ = upgraded.Close() })
	data := api.NewModuleData(upgraded)
	module.UseData(data)
	lateFailure := errors.New("injected late workspace failure")
	const failAt = extraWorkspaces + 1
	fault := &communicationGuardFaultData{
		inner: data, failMutateAt: failAt, failure: lateFailure,
	}
	module.UseCommunicationGuardReconciliationData(NewCommunicationGuardReconciliationData(fault))

	err = module.ReconcileCommunicationGuards(
		ctx, tenant, CommunicationGuardReconcileStaged,
	)
	if !errors.Is(err, lateFailure) {
		t.Fatalf("late staged failure = %v, want injected failure", err)
	}
	if fault.mutateCalls != failAt {
		t.Fatalf("workspace mutations = %d, want %d", fault.mutateCalls, failAt)
	}
	minimumPages := (failAt + communicationGuardWorkspacePageSize - 1) /
		communicationGuardWorkspacePageSize
	if fault.viewCalls != minimumPages {
		t.Fatalf("workspace page transactions = %d, want %d", fault.viewCalls, minimumPages)
	}
	if got := communicationCountGuards(t, module, tenant); got != 2*(failAt-1) {
		t.Fatalf("guards committed before late failure = %d, want %d", got, 2*(failAt-1))
	}

	module.UseCommunicationGuardReconciliationData(NewCommunicationGuardReconciliationData(data))
	if err := module.ReconcileCommunicationGuards(
		ctx, tenant, CommunicationGuardReconcileStaged,
	); err != nil {
		t.Fatalf("resume staged reconcile: %v", err)
	}
	wantGuards := 2 * (extraWorkspaces + 1) // extra rows plus the default workspace.
	if got := communicationCountGuards(t, module, tenant); got != wantGuards {
		t.Fatalf("guards after resumed reconcile = %d, want %d", got, wantGuards)
	}
	if err := module.VerifyCommunicationGuards(ctx, tenant); err != nil {
		t.Fatalf("enforced verification after resume: %v", err)
	}
}

func communicationCountGuards(t *testing.T, module *Module, tenant model.TenantID) int {
	t.Helper()
	ctx := context.Background()
	count := 0
	if err := module.data.View(ctx, tenant, func(scope store.Scope) error {
		repo, err := scope.Ext(communicationGuardKind)
		if err != nil {
			return err
		}
		query := model.Query{Limit: 500, IncludeDeleted: true}
		for {
			rows, page, err := repo.List(ctx, query)
			if err != nil {
				return err
			}
			count += len(rows)
			if !page.HasMore {
				return nil
			}
			if page.Cursor == "" || page.Cursor == query.Cursor {
				return errors.New("communication guard count cursor did not advance")
			}
			query.Cursor = page.Cursor
		}
	}); err != nil {
		t.Fatalf("count communication guards: %v", err)
	}
	return count
}

func communicationAssertGuardSet(
	t *testing.T,
	module *Module,
	tenant model.TenantID,
	workspace model.ID,
	wantRoute int64,
	wantDelivery int64,
) {
	t.Helper()
	ctx := context.Background()
	wantCount := 2
	if wantRoute == 0 && wantDelivery == 0 {
		wantCount = 0
	}
	if err := module.data.View(ctx, tenant, func(raw store.Scope) error {
		confined, err := store.ConfineWorkspace(ctx, raw, workspace)
		if err != nil {
			return err
		}
		repo, err := confined.Ext(communicationGuardKind)
		if err != nil {
			return err
		}
		rows, page, err := repo.List(ctx, model.Query{Limit: 3, IncludeDeleted: true})
		if err != nil {
			return err
		}
		if page.HasMore || len(rows) != wantCount {
			t.Errorf("guard rows = %d (more=%v), want %d", len(rows), page.HasMore, wantCount)
		}
		seen := map[CommunicationGuardKind]bool{}
		for _, row := range rows {
			guard, err := communicationGuardFromRecord(row)
			if err != nil {
				return err
			}
			seen[guard.Kind] = true
			if guard.TenantID != tenant || guard.WorkspaceID != workspace ||
				(guard.Version == 1 && !guard.CreatedAt.Equal(guard.LastDBTime)) ||
				guard.LastDBTime.Before(guard.CreatedAt) ||
				guard.LastDBTime.After(guard.UpdatedAt) {
				t.Errorf("guard lineage/time = %+v", guard)
			}
			switch guard.Kind {
			case CommunicationGuardRouteRevision:
				if guard.NextSeq != wantRoute {
					t.Errorf("route guard next = %d, want %d", guard.NextSeq, wantRoute)
				}
			case CommunicationGuardDeliverySequence:
				if guard.NextSeq != wantDelivery {
					t.Errorf("delivery guard next = %d, want %d", guard.NextSeq, wantDelivery)
				}
			}
		}
		if wantCount == 2 && (!seen[CommunicationGuardRouteRevision] ||
			!seen[CommunicationGuardDeliverySequence]) {
			t.Errorf("guard kinds = %#v, want route and delivery", seen)
		}
		return nil
	}); err != nil {
		t.Fatalf("read communication guards: %v", err)
	}
}
