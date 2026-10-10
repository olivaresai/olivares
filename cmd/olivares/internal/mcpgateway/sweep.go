// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package mcpgateway

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"

	mcpc "github.com/olivaresai/olivares/connectors/mcp"
	"github.com/olivaresai/olivares/core/envconfig"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/governance"
)

// envMCPTaskKillSwitchSweep controls the MCP durable-task cancellation sweep cadence.
// Empty uses the session kill-switch sweep default; "0" disables active cancellation.
const envMCPTaskKillSwitchSweep = "OLIVARES_MCP_TASK_KILLSWITCH_SWEEP"

// KillSwitch is the narrow slice of the governance module the sweep reads.
// *governance.Module satisfies it.
type KillSwitch interface {
	KillSwitchState(ctx context.Context, tenant model.TenantID) (governance.StopState, error)
}

var _ KillSwitch = (*governance.Module)(nil)

// StartTaskKillSwitchSweep cancels the active durable MCP tasks of a stopped estate
// or agent on the gateway's own server lifetime. defaultInterval is the session
// kill-switch sweep cadence the composition root owns.
func StartTaskKillSwitchSweep(srv *http.Server, rs *mcpc.ResourceServer, guard KillSwitch, tenant model.TenantID, defaultInterval time.Duration, log *slog.Logger) {
	if srv == nil || rs == nil || guard == nil || tenant.IsZero() {
		return
	}
	interval := loadMCPTaskKillSwitchSweepInterval(envconfig.Get, defaultInterval, log)
	if interval == 0 {
		if log != nil {
			log.Info("mcp gateway: task kill-switch sweep disabled", "env", envMCPTaskKillSwitchSweep)
		}
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	srv.RegisterOnShutdown(cancel)
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				sweepMCPTasksForKillSwitch(ctx, rs, guard, tenant, log)
			case <-ctx.Done():
				return
			}
		}
	}()
	if log != nil {
		log.Info("mcp gateway: task kill-switch sweep wired", "tenant", tenant.String(), "interval", interval.String())
	}
}

func loadMCPTaskKillSwitchSweepInterval(getenv func(string) string, defaultStopSweepInterval time.Duration, log *slog.Logger) time.Duration {
	raw := strings.TrimSpace(getenv(envMCPTaskKillSwitchSweep))
	if raw == "" {
		return defaultStopSweepInterval
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d < 0 {
		if log != nil {
			log.Warn("mcp gateway: invalid task kill-switch sweep interval; using the default",
				"env", envMCPTaskKillSwitchSweep, "value", raw, "default", defaultStopSweepInterval.String())
		}
		return defaultStopSweepInterval
	}
	return d
}

func sweepMCPTasksForKillSwitch(ctx context.Context, rs *mcpc.ResourceServer, guard KillSwitch, tenant model.TenantID, log *slog.Logger) {
	st, err := guard.KillSwitchState(ctx, tenant)
	if err != nil {
		if log != nil {
			log.Error("mcp gateway: task kill-switch sweep could not read stop state", "tenant", tenant.String(), "err", err)
		}
		return
	}
	if !st.Any() {
		return
	}
	tenantKey := tenant.String()
	if st.EstateStopped {
		reason := "kill-switch estate stop " + st.EstateStopID.String()
		n, cerr := rs.CancelActiveTasks(ctx, func(rec mcpc.TaskRecord) bool {
			return rec.Tenant == tenantKey
		}, reason)
		logMCPSweepResult(log, tenant, "estate", "", n, cerr)
		return
	}
	for subject, stopID := range st.AgentRefs {
		subject := strings.TrimSpace(subject)
		if subject == "" {
			continue
		}
		reason := "kill-switch agent stop " + stopID.String()
		n, cerr := rs.CancelActiveTasks(ctx, func(rec mcpc.TaskRecord) bool {
			return rec.Tenant == tenantKey && rec.Subject == subject
		}, reason)
		logMCPSweepResult(log, tenant, "agent", subject, n, cerr)
	}
}

func logMCPSweepResult(log *slog.Logger, tenant model.TenantID, scope, subject string, canceled int, err error) {
	if log == nil || (canceled == 0 && err == nil) {
		return
	}
	attrs := []any{"tenant", tenant.String(), "scope", scope, "cancelled", canceled} //nolint:misspell // the emitted log key; renaming it changes operator-visible output
	if subject != "" {
		attrs = append(attrs, "subject", subject)
	}
	if err != nil {
		attrs = append(attrs, "err", err)
		log.Warn("mcp gateway: task kill-switch sweep completed with cancellation errors", attrs...)
		return
	}
	log.Info("mcp gateway: task kill-switch sweep canceled active tasks", attrs...)
}
