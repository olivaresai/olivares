// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/sdk/event"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

type guardianRollbackData struct{ api.ModuleData }

func (d guardianRollbackData) Mutate(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	return d.ModuleData.Mutate(ctx, tenant, func(sc store.Scope) error {
		if err := fn(sc); err != nil {
			return err
		}
		return errors.New("ledger unavailable")
	})
}

type guardianFailingHost struct{ *capturingHost }

func (h guardianFailingHost) Publish(context.Context, event.Event) error {
	return errors.New("bus unavailable")
}

func TestGuardianRolledBackFireSignalsFailure(t *testing.T) {
	f := newGuardianFixture(t)
	f.createRule("contain", "threat", "high", gaActionStopAgent, gaModeAuto)
	var logs bytes.Buffer
	f.m.log = slog.New(slog.NewTextHandler(&logs, nil))
	f.m.data = guardianRollbackData{f.m.data}
	f.fire("threat", sdkmodel.SeverityHigh, "agent", "agent-1", "rollback")
	if len(f.actions()) != 0 || len(f.stops()) != 0 {
		t.Fatal("failed transaction committed containment")
	}
	if f.findingKinds()[findingGuardianFailed] != 1 {
		t.Fatalf("missing failed finding: %v", f.findingKinds())
	}
	if !strings.Contains(logs.String(), "level=WARN") {
		t.Fatalf("failure invisible at default log level: %s", &logs)
	}
}

func TestGuardianPublishFailureWarns(t *testing.T) {
	f := newGuardianFixture(t)
	var logs bytes.Buffer
	f.m.log = slog.New(slog.NewTextHandler(&logs, nil))
	f.m.host = guardianFailingHost{f.host}
	f.m.emitGuardianFinding(context.Background(), f.tenant, findingGuardianFailed, "agent", "agent-1", sdkmodel.SeverityHigh, "containment failed")
	if !strings.Contains(logs.String(), "level=WARN") {
		t.Fatalf("publish failure invisible: %s", &logs)
	}
}

func TestGuardianTerminalApprovalSignalsFailureOnce(t *testing.T) {
	for _, status := range []string{statusRejected, statusCanceled, statusExpired} {
		t.Run(status, func(t *testing.T) {
			f := newGuardianFixture(t)
			f.createRule("contain", "threat", "high", gaActionStopAgent, gaModeApproval)
			f.fire("threat", sdkmodel.SeverityHigh, "agent", "agent-1", "terminal")
			action := f.actions()[0]
			if err := f.st.Mutate(t.Context(), f.tenant, func(sc store.Scope) error {
				repo, err := sc.Ext(approvalKind)
				if err != nil {
					return err
				}
				rec, err := repo.Get(t.Context(), model.ID(action.String(colGAApprovalID)))
				if err != nil {
					return err
				}
				rec[colStatus] = status
				_, err = repo.Update(t.Context(), rec)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 2; i++ {
				if _, err := f.m.GuardianSweep(t.Context(), f.tenant); err != nil {
					t.Fatal(err)
				}
			}
			if len(f.stops()) != 0 {
				t.Fatal("terminal approval executed containment")
			}
			if f.findingKinds()[findingGuardianFailed] != 1 {
				t.Fatalf("want one failed finding: %v", f.findingKinds())
			}
		})
	}
}
