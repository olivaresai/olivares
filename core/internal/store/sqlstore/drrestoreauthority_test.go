// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"reflect"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/dr/opgate"
)

func TestDRControlPublicAuthorityIsPendingOnly(t *testing.T) {
	typ := reflect.TypeFor[PendingRestoreSpec]()
	var fields []string
	for i := 0; i < typ.NumField(); i++ {
		fields = append(fields, typ.Field(i).Name)
	}
	if !reflect.DeepEqual(fields, []string{"OpID", "PlanSHA256"}) {
		t.Fatalf("caller can supply more than operation/plan: %v", fields)
	}
	for _, path := range []string{"../../../engine/drrestore.go", "drrestorecontrol.go", "drrestoreoperation.go"} {
		f, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range f.Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok && fn.Name.IsExported() {
				switch fn.Name.Name {
				case "InstallRestoreControl", "InstallPostgresRestoreControl", "CommitRestoreControl", "CommitPostgresRestoreControl":
					t.Errorf("unaccepted arbitrary-state production facade remains: %s", fn.Name.Name)
				}
				if path == "drrestoreoperation.go" {
					t.Errorf("private operation exported %s", fn.Name.Name)
				}
			}
		}
	}
	// A report/OP_ID and historical booleans cannot assemble live authority.
	for _, op := range []*restoreOperation{nil, {}, {spec: PendingRestoreSpec{OpID: drTestOpA, PlanSHA256: drTestPlanA}, coord: &drCoordination{acquired: true, exclusive: true}}} {
		if _, err := op.transition(context.Background(), restorePredecessor{1, opgate.StatePending}, opgate.StateComplete); !errors.Is(err, ErrRestoreControlConflict) {
			t.Fatalf("manufactured operation accepted: %v", err)
		}
	}
}

func TestDRControlPrivateOperationCannotCompleteOrReopen(t *testing.T) {
	pg := isolatedPGSplit(t)
	cfg := drPGConfig(pg)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	spec := PendingRestoreSpec{OpID: drTestOpA, PlanSHA256: drTestPlanA}
	initial, err := InstallPendingRestoreControl(ctx, cfg, spec)
	if err != nil {
		t.Fatal(err)
	}
	super := drOpenSuper(t, pg.Superuser)
	for _, tc := range []struct {
		name string
		spec PendingRestoreSpec
		pred restorePredecessor
		next string
	}{
		{"complete", spec, restorePredecessor{initial.Revision, opgate.StatePending}, opgate.StateComplete},
		{"stale_revision", spec, restorePredecessor{initial.Revision + 1, opgate.StatePending}, opgate.StateIndeterminate},
		{"wrong_predecessor_state", spec, restorePredecessor{initial.Revision, opgate.StateIndeterminate}, opgate.StateQuarantined},
		{"wrong_operation", PendingRestoreSpec{OpID: drTestOpB, PlanSHA256: drTestPlanA}, restorePredecessor{initial.Revision, opgate.StatePending}, opgate.StateQuarantined},
		{"wrong_plan", PendingRestoreSpec{OpID: drTestOpA, PlanSHA256: drTestPlanB}, restorePredecessor{initial.Revision, opgate.StatePending}, opgate.StateQuarantined},
		{"reopen_pending", spec, restorePredecessor{initial.Revision, opgate.StatePending}, opgate.StatePending},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := ddaSnapshot(t, super)
			if _, err := drTransitionControlForTest(ctx, cfg, tc.spec, tc.pred, tc.next); !errors.Is(err, ErrRestoreControlConflict) {
				t.Fatalf("private transition accepted: %v", err)
			}
			ddaUnchanged(t, super, before)
		})
	}
	var captured *restoreOperation
	_, err = withPostgresRestoreControl(ctx, cfg, spec, func(op *restoreOperation) (RestoreControlReport, error) {
		captured = op
		return op.transition(ctx, restorePredecessor{initial.Revision, opgate.StatePending}, opgate.StateIndeterminate)
	})
	if err != nil {
		t.Fatal(err)
	}
	before := ddaSnapshot(t, super)
	// Closed references remain unusable even with the next correct revision.
	if _, err := captured.transition(ctx, restorePredecessor{initial.Revision + 1, opgate.StateIndeterminate}, opgate.StateQuarantined); !errors.Is(err, ErrRestoreControlConflict) {
		t.Fatalf("retired operation still writable: %v", err)
	}
	ddaUnchanged(t, super, before)
	drSetCompleteReaderRowForTest(t, pg, cfg)
	before = ddaSnapshot(t, super)
	if _, err := InstallPendingRestoreControl(ctx, cfg, spec); !errors.Is(err, ErrRestoreControlConflict) {
		t.Fatalf("initial request reopened complete: %v", err)
	}
	if _, err := drTransitionControlForTest(ctx, cfg, spec, restorePredecessor{initial.Revision + 1, opgate.StateComplete}, opgate.StateQuarantined); !errors.Is(err, ErrRestoreControlConflict) {
		t.Fatalf("private CAS reopened complete: %v", err)
	}
	ddaUnchanged(t, super, before)
}

func TestDRControlPrivateOperationRequiresActualExclusiveLock(t *testing.T) {
	for _, mode := range []string{"unlock", "terminate", "after_transition_unlock"} {
		t.Run(mode, func(t *testing.T) {
			pg := isolatedPG(t)
			cfg := drPGConfig(pg)
			ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
			defer cancel()
			spec := PendingRestoreSpec{OpID: drTestOpA, PlanSHA256: drTestPlanA}
			if _, err := InstallPendingRestoreControl(ctx, cfg, spec); err != nil {
				t.Fatal(err)
			}
			super := drOpenSuper(t, pg.Superuser)
			before := ddaSnapshot(t, super)
			_, err := withPostgresRestoreControl(ctx, cfg, spec, func(op *restoreOperation) (RestoreControlReport, error) {
				var changed RestoreControlReport
				if mode == "after_transition_unlock" {
					var err error
					changed, err = op.transition(ctx, restorePredecessor{1, opgate.StatePending}, opgate.StateQuarantined)
					if err != nil {
						return changed, err
					}
				}
				if mode != "terminate" {
					var released bool
					if err := op.tx.QueryRowContext(ctx, `SELECT pg_advisory_unlock(pg_catalog.hashtextextended('olivares.restore.v1:'||current_database()||':public',0))`).Scan(&released); err != nil || !released {
						t.Fatalf("release actual owned lock: %t %v", released, err)
					}
				} else {
					var pid int
					if err := op.tx.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
						t.Fatal(err)
					}
					var stopped bool
					if err := super.QueryRowContext(ctx, `SELECT pg_terminate_backend($1,3000)`, pid).Scan(&stopped); err != nil || !stopped {
						t.Fatalf("stop actual owned backend: %t %v", stopped, err)
					}
				}
				if mode == "after_transition_unlock" {
					return changed, nil
				}
				return op.transition(ctx, restorePredecessor{1, opgate.StatePending}, opgate.StateQuarantined)
			})
			if !errors.Is(err, ErrRestoreControlConflict) {
				t.Fatalf("lost exclusive authority accepted: %v", err)
			}
			ddaUnchanged(t, super, before)
		})
	}
}
