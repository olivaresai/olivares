// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/olivaresai/olivares/cmd/olivares/internal/approvalbridge"
	"github.com/olivaresai/olivares/connectors/claude"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/sessions"
	"github.com/olivaresai/olivares/modules/sessions/hookpep"
)

// claudehookpep.go wires the governed Claude Code hooks PEP (modules/sessions/hookpep)
// in the composition root: the operator config, the engine planes and the loopback listener.

func loadHookPEPConfig(_ *slog.Logger) (hookpep.Config, error) {
	path := osGetenv("OLIVARES_HOOK_PEP_CONFIG")
	if path == "" {
		return hookpep.Config{Listen: defaultHookPEPListen}, nil
	}
	var cfg hookpep.Config
	if err := loadOperatorJSONConfig("OLIVARES_HOOK_PEP_CONFIG", path, &cfg); err != nil {
		return hookpep.Config{}, err
	}
	return cfg, nil
}

// Each engine receives its own loopback socket; the acquired address is retained
// in memory and pinned into the protected settings for each launched run.
const defaultHookPEPListen = "127.0.0.1:0"

// buildClaudeHookPEPServer constructs the default governed hooks server after
// boot. runEngine publishes its acquired listener address to the launch
// provisioner before the announcement or any HTTP server starts.
func buildClaudeHookPEPServer(eng *engine, log *slog.Logger) (*http.Server, error) {
	cfg, err := loadHookPEPConfig(log)
	if err != nil {
		return nil, fmt.Errorf("load hook PEP operator config: %w", err)
	}
	tenants := map[model.TenantID]hookpep.ResolvedTenant{}
	for _, tc := range cfg.Tenants {
		tid, present, err := parseBusinessTenant("hook-PEP config: tenant", tc.Tenant)
		if err != nil || !present {
			return nil, errors.New("hook PEP tenant id is invalid")
		}
		if _, dup := tenants[tid]; dup {
			log.Warn("hook-pep: duplicate tenant entry; later definition ignored", "tenant", tc.Tenant)
			continue
		}
		rt, err := hookpep.ResolveTenant(tid, tc, time.Now(), log)
		if err != nil {
			return nil, err
		}
		tenants[tid] = rt
	}

	dec := newClaudeHookDecider(&hookpep.Decider{
		Tenants:       tenants,
		DefaultPolicy: &hookpep.PolicyDoc{Default: claude.DecisionAllow},
		Authr:         eng.hookCredentials(),
		Eval:          eng.policyEval,
		Scoped:        eng.scopedGrants,
		Tracer:        eng.tracer,
		Authz:         eng.authz,
		NHIEnforcer:   eng.nhiEnforcer,
		Stops:         eng.killSwitch,
		StopDeny:      eng.stopDeny.record,
		Store:         eng.store,
		Approvals:     eng.engineApprovals,
		// Hook content firewall: the real inspector under -tags enterprise WITH a config,
		// else nil (no deep inspection, unchanged). Findings/metering ride the engine bus.
		Inspector: thisEdition.hookContentInspector.get(osGetenv, log),
		Bus:       eng.bus,
		Clock:     time.Now,
		Log:       log,
	})
	if eng.sessionsMod != nil {
		dec.ApprovalWait = eng.sessionsMod.BeginApprovalWait
		dec.SessionTrace = eng.sessionsMod.SessionTrace
		dec.RedactSecrets = eng.sessionsMod.RedactSessionSecretsWithSpans
		dec.SessionObservation = eng.sessionsMod.RecordSessionObservation
	}
	// Assign the bridge only when one is configured: storing a nil *approvalBridge in the
	// interface would make d.Bridge != nil (typed-nil) and defeat the deny-closed guard.
	if eng.approvalBridge != nil {
		dec.Bridge = eng.approvalBridge
	}

	var sessionMCP http.Handler
	if eng.sessionMCP != nil {
		sessionMCP = http.HandlerFunc(eng.sessionMCP.ServeSessionHTTP)
	}
	addr := strings.TrimSpace(cfg.Listen)
	if addr == "" {
		addr = defaultHookPEPListen
	}
	if !hostIsLoopback(addr) {
		log.Warn("hook-pep: bound to a NON-loopback address; front it with your ingress — its security is fail-closed token verification + the governed decision, not network isolation", "addr", addr)
	}
	srv := eng.api.NewHTTPServer(addr)
	srv.Handler = dec.Handler(sessionMCP)
	srv.WriteTimeout = sessions.ClaudeHookPEPClientTimeout + 10*time.Second
	log.Info("hook-pep: governed Claude Code hooks PEP mounted",
		"addr", addr, "tenants", len(tenants), "hitl_bridge", eng.approvalBridge != nil, "pdp", eng.policyEval != nil)
	return srv, nil
}

// newClaudeHookDecider binds the plane the composition root owns: the bridge's
// plan-bound subject_ref encoding.
func newClaudeHookDecider(d *hookpep.Decider) *hookpep.Decider {
	d.SubjectRef = approvalbridge.EncodeSubjectRef
	return d
}

// principalAuthenticator resolves an inbound bearer to a real principal. *auth.Authenticator
// satisfies it; tests inject a fake. It is the firm-identity + audit-actor source.
type principalAuthenticator interface {
	Authenticate(ctx context.Context, token string) (auth.Principal, error)
}

// hookApprovalOpener is the subset of the bridge a PEP uses: open (or idempotently
// find/reuse) a governed approval bound to the plan hash and report its effective status
// in one shot, plus SPEND that approval single-use once a human approved it. *approvalBridge
// satisfies it, and so does hookpep.ApprovalOpener (the same two methods).
type hookApprovalOpener interface {
	GateOnce(ctx context.Context, tenant model.TenantID, action, subjectKind, subjectRef, planHash, reason, requestedBy string) (ref, status, boundHash string, err error)
	// ConsumeApproval spends an APPROVED approval exactly once, keyed to the exact
	// caller (consumerID = the tool_use_id): granted for the first/idempotent-same
	// consumer, replay=true for a NEW caller reusing an already-spent grant (F-02).
	ConsumeApproval(ctx context.Context, tenant model.TenantID, ref, consumerID, policyVersion string) (granted, replay bool, err error)
}

func firstNonEmptyStr(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}

func sessionRunAdmitted(ctx context.Context, authz *auth.Authorizer, p auth.Principal, tenant model.TenantID, perm auth.Permission, run model.ID, workspace model.ID) auth.Decision {
	return hookpep.SessionRunAdmitted(ctx, authz, p, tenant, perm, run, workspace)
}
