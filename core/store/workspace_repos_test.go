// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package store

import (
	"context"
	"errors"
	"testing"

	"github.com/olivaresai/olivares/core/model"
)

type workspaceCapabilityTestRepo struct{}

func (workspaceCapabilityTestRepo) Descriptor() model.EntityDescriptor {
	return model.EntityDescriptor{}
}
func (workspaceCapabilityTestRepo) Get(context.Context, model.ID) (model.Record, error) {
	return nil, nil
}
func (workspaceCapabilityTestRepo) List(
	context.Context,
	model.Query,
) ([]model.Record, model.Page, error) {
	return nil, model.Page{}, nil
}
func (workspaceCapabilityTestRepo) Create(context.Context, model.Record) (model.Record, error) {
	return nil, nil
}
func (workspaceCapabilityTestRepo) CreateWithID(
	context.Context,
	model.ID,
	model.Record,
) (model.Record, error) {
	return nil, nil
}
func (workspaceCapabilityTestRepo) Update(context.Context, model.Record) (model.Record, error) {
	return nil, nil
}
func (workspaceCapabilityTestRepo) Delete(context.Context, model.ID) error { return nil }

type workspaceCapabilityStampedRepo struct{ workspaceCapabilityTestRepo }

func (workspaceCapabilityStampedRepo) CreateAtTransactionTime(
	context.Context,
	model.Record,
) (model.Record, error) {
	return nil, nil
}
func (workspaceCapabilityStampedRepo) CreateWithIDAtTransactionTime(
	context.Context,
	model.ID,
	model.Record,
) (model.Record, error) {
	return nil, nil
}
func (workspaceCapabilityStampedRepo) UpdateAtTransactionTime(
	context.Context,
	model.Record,
) (model.Record, error) {
	return nil, nil
}

type workspaceCapabilityRowLocker struct{}

func (workspaceCapabilityRowLocker) Lock(context.Context, model.ID) (model.Record, error) {
	return nil, nil
}

type workspaceAuditCapabilityTestLog struct {
	appended *model.AuditDraft
}

func (l workspaceAuditCapabilityTestLog) Append(
	_ context.Context,
	draft model.AuditDraft,
) (model.AuditEvent, error) {
	if l.appended != nil {
		*l.appended = draft
	}
	return model.AuditEvent{}, nil
}
func (workspaceAuditCapabilityTestLog) Verify(
	context.Context,
	int64,
) (VerifyReport, error) {
	return VerifyReport{}, nil
}
func (workspaceAuditCapabilityTestLog) Walk(
	context.Context,
	int64,
	func(model.AuditEvent) error,
) error {
	return nil
}
func (workspaceAuditCapabilityTestLog) Head(context.Context) (HeadRef, bool, error) {
	return HeadRef{}, false, nil
}

type workspaceVerifiedAuditCapabilityTestLog struct {
	workspaceAuditCapabilityTestLog
	event model.AuditEvent
	meta  string
	found bool
	err   error
}

type workspaceAppendLockedAuditCapabilityTestLog struct {
	workspaceAuditCapabilityTestLog
	calls *int
}

func (l workspaceAppendLockedAuditCapabilityTestLog) LockAppends(context.Context) error {
	if l.calls != nil {
		*l.calls++
	}
	return nil
}

type workspaceVerifiedAppendLockedAuditCapabilityTestLog struct {
	workspaceVerifiedAuditCapabilityTestLog
	calls *int
}

func (l workspaceVerifiedAppendLockedAuditCapabilityTestLog) LockAppends(context.Context) error {
	if l.calls != nil {
		*l.calls++
	}
	return nil
}

func (l workspaceVerifiedAuditCapabilityTestLog) ReadVerifiedAuditAnchor(
	context.Context,
	int64,
) (model.AuditEvent, string, bool, error) {
	return l.event, l.meta, l.found, l.err
}

func TestConfinedGenericRepoPreservesOptionalCapabilitiesExactly(t *testing.T) {
	base := confinedGenericRepo{raw: workspaceCapabilityTestRepo{}}
	rowOnly := confinedRowLockingGenericRepo{
		confinedGenericRepo: base,
		locker:              workspaceCapabilityRowLocker{},
	}
	stampedOnly := confinedTransactionStampedGenericRepo{
		confinedGenericRepo: base,
		stamped:             workspaceCapabilityStampedRepo{},
	}
	both := confinedTransactionStampedRowLockingGenericRepo{
		confinedTransactionStampedGenericRepo: stampedOnly,
		locker:                                workspaceCapabilityRowLocker{},
	}

	for _, test := range []struct {
		name        string
		repo        any
		wantStamped bool
		wantLocker  bool
	}{
		{name: "base", repo: base},
		{name: "row only", repo: rowOnly, wantLocker: true},
		{name: "stamped only", repo: stampedOnly, wantStamped: true},
		{name: "stamped and row", repo: both, wantStamped: true, wantLocker: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, gotStamped := test.repo.(TransactionStampedGenericRepo)
			_, gotLocker := test.repo.(RowLocker[model.Record])
			if gotStamped != test.wantStamped || gotLocker != test.wantLocker {
				t.Fatalf("capabilities stamped=%t locker=%t, want %t/%t",
					gotStamped, gotLocker, test.wantStamped, test.wantLocker)
			}
		})
	}
}

func TestConfinedAuditLogPreservesAndWorkspaceBindsVerifiedAnchor(t *testing.T) {
	workspace := model.NewID()
	if _, ok := confineAuditLog(workspaceAuditCapabilityTestLog{}, workspace).(VerifiedAuditAnchorReader); ok {
		t.Fatal("confined audit fabricated VerifiedAuditAnchorReader capability")
	}

	event := model.AuditEvent{ID: model.NewID(), Seq: 7}
	for _, test := range []struct {
		name      string
		meta      string
		found     bool
		wantFound bool
		wantErr   bool
	}{
		{
			name:  "same workspace",
			meta:  `{"workspace_id":"` + workspace.String() + `","workspace_binding_version":1,"channel_id":"x"}`,
			found: true, wantFound: true,
		},
		{
			name:  "foreign workspace",
			meta:  `{"workspace_id":"` + model.NewID().String() + `","workspace_binding_version":1}`,
			found: true,
		},
		{name: "missing workspace", meta: `{}`, found: true},
		{name: "non-string workspace", meta: `{"workspace_id":7,"workspace_binding_version":1}`, found: true},
		{name: "missing binding marker", meta: `{"workspace_id":"` + workspace.String() + `"}`, found: true},
		{name: "trailing value", meta: `{"workspace_id":"` + workspace.String() + `","workspace_binding_version":1} {}`, found: true},
		{name: "missing anchor", meta: `{}`, found: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			raw := workspaceVerifiedAuditCapabilityTestLog{
				event: event, meta: test.meta, found: test.found,
			}
			confined := confineAuditLog(raw, workspace)
			reader, ok := confined.(VerifiedAuditAnchorReader)
			if !ok {
				t.Fatal("confined audit lost VerifiedAuditAnchorReader capability")
			}
			gotEvent, gotMeta, gotFound, err := reader.ReadVerifiedAuditAnchor(
				context.Background(), event.Seq,
			)
			if (err != nil) != test.wantErr {
				t.Fatalf("ReadVerifiedAuditAnchor error = %v, wantErr=%t", err, test.wantErr)
			}
			if gotFound != test.wantFound {
				t.Fatalf("ReadVerifiedAuditAnchor found = %t, want %t", gotFound, test.wantFound)
			}
			if test.wantFound && (gotEvent.ID != event.ID || gotMeta != test.meta) {
				t.Fatalf("ReadVerifiedAuditAnchor = (%+v,%q), want (%+v,%q)",
					gotEvent, gotMeta, event, test.meta)
			}
			if !test.wantFound && !test.wantErr &&
				(gotEvent.ID != "" || gotMeta != "") {
				t.Fatalf("hidden audit anchor leaked event=%+v meta=%q", gotEvent, gotMeta)
			}
		})
	}

	want := errors.New("read failed")
	reader := confineAuditLog(workspaceVerifiedAuditCapabilityTestLog{
		err: want,
	}, workspace).(VerifiedAuditAnchorReader)
	if event, meta, found, err := reader.ReadVerifiedAuditAnchor(context.Background(), 1); err != nil || found || event.ID != "" || meta != "" {
		t.Fatalf("foreign/unclassified read error leaked as event=%+v meta=%q found=%t err=%v",
			event, meta, found, err)
	}

	var appended model.AuditDraft
	confined := confineAuditLog(workspaceAuditCapabilityTestLog{appended: &appended}, workspace)
	if _, err := confined.Append(context.Background(), model.AuditDraft{Meta: map[string]any{
		"workspace_id": model.NewID().String(), "caller": "kept",
	}}); err != nil {
		t.Fatalf("confined audit append: %v", err)
	}
	if appended.Meta["workspace_id"] != workspace.String() ||
		appended.Meta["workspace_binding_version"] != confinedAuditCurrentWorkspaceBindingVersion ||
		appended.Meta["caller"] != "kept" {
		t.Fatalf("confined audit metadata was not server-bound: %#v", appended.Meta)
	}
}

func TestConfinedAuditLogPreservesAppendLockCapabilityExactly(t *testing.T) {
	workspace := model.NewID()
	appendLockedCalls := 0
	verifiedAppendLockedCalls := 0
	for _, test := range []struct {
		name         string
		raw          AuditLog
		wantVerified bool
		wantLocker   bool
		calls        *int
	}{
		{name: "base", raw: workspaceAuditCapabilityTestLog{}},
		{name: "verified", raw: workspaceVerifiedAuditCapabilityTestLog{}, wantVerified: true},
		{
			name: "append locked",
			raw: workspaceAppendLockedAuditCapabilityTestLog{
				calls: &appendLockedCalls,
			},
			wantLocker: true, calls: &appendLockedCalls,
		},
		{
			name: "verified and append locked",
			raw: workspaceVerifiedAppendLockedAuditCapabilityTestLog{
				calls: &verifiedAppendLockedCalls,
			},
			wantVerified: true, wantLocker: true,
			calls: &verifiedAppendLockedCalls,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			confined := confineAuditLog(test.raw, workspace)
			_, gotVerified := confined.(VerifiedAuditAnchorReader)
			locker, gotLocker := confined.(AuditAppendLocker)
			if gotVerified != test.wantVerified || gotLocker != test.wantLocker {
				t.Fatalf("capabilities verified=%t locker=%t, want %t/%t",
					gotVerified, gotLocker, test.wantVerified, test.wantLocker)
			}
			if gotLocker {
				if err := locker.LockAppends(context.Background()); err != nil {
					t.Fatalf("LockAppends: %v", err)
				}
				if test.calls == nil || *test.calls != 1 {
					t.Fatalf("raw LockAppends calls = %v, want one", test.calls)
				}
			}
		})
	}
}
