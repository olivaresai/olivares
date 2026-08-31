// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/runtime"
)

// errDriftList is the internal sentinel for a failed deployment-list page during a
// drift sweep (logged and the tenant skipped for this tick — never fatal).
var errDriftList = errors.New("deploy-drift: deployment list failed")

// This file is the deploy DRIFT LOOP: a first-class, periodic desired-vs-real
// reconciliation. It runs on the runtime's EXISTING periodic scheduler
// (runtime.SchedulePeriodic — the brief: "no inventes uno paralelo"), and for each
// active deployment it drives the module's verify path IN-PROCESS over the engine
// handler. verify calls the real executor's Observe(), updates the core Deployment
// snapshot (active|drifted) and records an operation — so the delta reaches the
// access map exactly through the module's own seam, never a side channel.
//
// PER-UNIT POLICY (mirror of Argo selfHeal vs OutOfSync):
//   - alert-only (default, safest): detect + record drift; a human/dashboard acts.
//   - auto-heal: ALSO re-open the governed apply (phase 1) for a drifted unit, which
//     idempotently keeps an HITL approval open for the drifted plan. This is GOVERNED
//     self-heal — it nudges the two-phase gate, it NEVER mutates silently (a real
//     mutation still needs the human approval the module enforces).
//
// HONEST GAPS: where the executor reports a unit as unobservable, the module's verify
// records a non-sync change (see deployexec.go Verify) — the loop never fabricates an
// in-sync result for a unit it cannot read.
//
// Note on timers (the brief's warning): the loop interval here is the controller
// RECONCILE interval (minutes), distinct from a GitOps tool's own self-heal retry
// (seconds). They are different concerns; this loop does not conflate them.

// driftTenantCfg maps a tenant to the service token the loop verifies/heals as
// (deploy:deployment:write scope) and its per-tenant auto-heal policy.
type driftTenantCfg struct {
	Tenant   string `json:"tenant"`
	Token    string `json:"token"`
	AutoHeal bool   `json:"auto_heal"`
}

// deployDriftLoop drives periodic verify (and governed auto-heal) per active
// deployment for each configured tenant.
type deployDriftLoop struct {
	tenants  []driftTenant
	interval time.Duration
	log      *slog.Logger

	handlerMu sync.RWMutex
	handler   http.Handler
}

type driftTenant struct {
	id       model.TenantID
	token    string
	autoHeal bool
}

// newDeployDriftLoop builds the loop from config. interval<=0 disables the periodic
// run (the loop still registers but the runtime treats a non-positive interval as a
// one-shot; we clamp to a sane default). Returns nil when no usable tenant exists.
func newDeployDriftLoop(tenants []driftTenantCfg, interval time.Duration, log *slog.Logger) *deployDriftLoop {
	out := []driftTenant{}
	for _, tc := range tenants {
		tid, present, err := parseBusinessTenant("deploy-drift config: tenant", tc.Tenant)
		if err != nil || !present || strings.TrimSpace(tc.Token) == "" {
			log.Warn("deploy-drift: tenant entry invalid or missing token; skipped", "tenant", tc.Tenant)
			continue
		}
		out = append(out, driftTenant{id: tid, token: tc.Token, autoHeal: tc.AutoHeal})
	}
	if len(out) == 0 {
		return nil
	}
	if interval <= 0 {
		interval = 5 * time.Minute
	}
	log.Info("deploy-drift: drift loop wired", "tenants", len(out), "interval", interval.String())
	return &deployDriftLoop{tenants: out, interval: interval, log: log}
}

// useHandler late-binds the engine handler (boot.go, after api.New).
func (d *deployDriftLoop) useHandler(h http.Handler) {
	d.handlerMu.Lock()
	d.handler = h
	d.handlerMu.Unlock()
}

func (d *deployDriftLoop) currentHandler() http.Handler {
	d.handlerMu.RLock()
	defer d.handlerMu.RUnlock()
	return d.handler
}

// register schedules the periodic drift run on the runtime's own scheduler.
func (d *deployDriftLoop) register(rt *runtime.Runtime) error {
	return rt.SchedulePeriodic("deploy.drift", d.interval, false, d.runOnce)
}

// runOnce verifies every active deployment for every configured tenant. A per-unit
// failure is logged and skipped (a drift sweep is best-effort and never aborts the
// whole run for one bad unit).
func (d *deployDriftLoop) runOnce(ctx context.Context) error {
	h := d.currentHandler()
	if h == nil {
		return nil // handler not yet bound (boot race) — the next tick will run
	}
	for _, t := range d.tenants {
		ids, err := d.activeDefinitions(ctx, t)
		if err != nil {
			d.log.Warn("deploy-drift: cannot list deployments for tenant; skipping this tick", "tenant", t.id.String())
			continue
		}
		for _, id := range ids {
			if err := ctx.Err(); err != nil {
				return err
			}
			drift := d.verify(ctx, t, id)
			if drift && t.autoHeal {
				d.heal(ctx, t, id)
			}
		}
	}
	return nil
}

// activeDefinitions lists the ids of a tenant's non-retired deployment definitions.
func (d *deployDriftLoop) activeDefinitions(ctx context.Context, t driftTenant) ([]string, error) {
	var ids []string
	cursor := ""
	for i := 0; i < 1000; i++ {
		path := "/v1/m/deploy/definitions?limit=200"
		if cursor != "" {
			path += "&cursor=" + url.QueryEscape(cursor)
		}
		code, raw := d.call(ctx, t, http.MethodGet, path, nil)
		if code != http.StatusOK {
			return nil, errDriftList
		}
		var resp struct {
			Items []struct {
				ID            string `json:"id"`
				DesiredStatus string `json:"desired_status"`
				AppliedVer    int64  `json:"applied_version"`
			} `json:"items"`
			Cursor  string `json:"cursor"`
			HasMore bool   `json:"has_more"`
		}
		if err := json.Unmarshal(raw, &resp); err != nil {
			return nil, errDriftList
		}
		for _, it := range resp.Items {
			// Only verify what is actually deployed (applied) and not retired.
			if it.DesiredStatus != "retired" && it.AppliedVer > 0 {
				ids = append(ids, it.ID)
			}
		}
		if !resp.HasMore || resp.Cursor == "" {
			break
		}
		cursor = resp.Cursor
	}
	return ids, nil
}

// verify drives the module's verify path for one deployment and reports whether
// drift was observed (in_sync == false).
func (d *deployDriftLoop) verify(ctx context.Context, t driftTenant, id string) bool {
	code, raw := d.call(ctx, t, http.MethodPost, "/v1/m/deploy/definitions/"+url.PathEscape(id)+"/verify", map[string]any{})
	if code != http.StatusOK {
		return false
	}
	var resp struct {
		InSync bool `json:"in_sync"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return false
	}
	if !resp.InSync {
		d.log.Info("deploy-drift: drift detected", "tenant", t.id.String(), "definition", id, "auto_heal", t.autoHeal)
	}
	return !resp.InSync
}

// heal re-opens the GOVERNED apply (phase 1) for a drifted deployment. It mutates
// NOTHING: phase 1 only (idempotently) opens/refreshes the HITL approval bound to the
// drifted plan, so a human still authorizes the actual reconciliation. This is the
// honest "auto-heal" — it surfaces drift into the governed flow, never bypasses it.
func (d *deployDriftLoop) heal(ctx context.Context, t driftTenant, id string) {
	code, _ := d.call(ctx, t, http.MethodPost, "/v1/m/deploy/definitions/"+url.PathEscape(id)+"/apply", map[string]any{})
	d.log.Info("deploy-drift: governed self-heal requested (HITL approval opened for the drifted plan)", "tenant", t.id.String(), "definition", id, "phase1_status", code)
}

// call performs one in-process governed API call as the tenant's drift service
// principal over the engine handler (the captureWriter mechanism).
func (d *deployDriftLoop) call(ctx context.Context, t driftTenant, method, path string, body any) (int, []byte) {
	h := d.currentHandler()
	if h == nil {
		return 0, nil
	}
	rdr := strings.NewReader("")
	if body != nil {
		bs, err := json.Marshal(body)
		if err != nil {
			return 0, nil
		}
		rdr = strings.NewReader(string(bs))
	}
	req, err := http.NewRequestWithContext(loopbackContext(ctx), method, path, rdr)
	if err != nil {
		return 0, nil
	}
	req.Header.Set("Authorization", "Bearer "+t.token)
	req.Header.Set("X-Olivares-Tenant", t.id.String())
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := &captureWriter{header: http.Header{}, status: http.StatusOK}
	h.ServeHTTP(rec, req)
	return rec.status, rec.body.Bytes()
}
