// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/codex/session"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/sessions"
	"github.com/olivaresai/olivares/sdk/event"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// codexhookpepserver.go mounts the Codex hook PEP on its own loopback socket, following
// the same shape as the Claude one: an operator JSON config selected by an env path,
// NOTHING mounted when it is unset, and a fail-closed startup when it is set but unusable.
//
// An un-wired node therefore serves no Codex enforcement surface at all, which is the safe
// default — a Codex agent on such a node runs ungoverned-but-unobserved exactly as it did
// before this existed, rather than half-governed in a way nobody can characterize.

// defaultCodexHookPEPListen keeps the Codex surface on its own port. It is NOT shared with
// the Claude PEP: the two speak different wire dialects, and one socket answering both
// would mean a misconfigured hooks.json could receive an answer in a shape Codex ignores —
// the exact silent failure this whole connector is built to prevent.
const defaultCodexHookPEPListen = "127.0.0.1:8448"

// codexSignalSource names this producer on the bus envelope.
const codexSignalSource = "codex-hook-pep"

// codexPublishTimeout bounds the emit so a slow bus consumer cannot stall an agent.
const codexPublishTimeout = 2 * time.Second

// codexHookPEPConfig is the operator provisioning. It deliberately declares NO policy of
// its own: the verdict comes from the engine's already-wired PDP under the Codex
// capability, so there is one place where policy lives rather than two that can disagree.
type codexHookPEPConfig struct {
	Listen string `json:"listen"`
	// Tenant is the single tenant this endpoint governs. One socket, one tenant: the
	// inbound hint can then only CONFIRM it, never select it.
	Tenant string `json:"tenant"`
}

// loadCodexHookPEPConfig reads the optional OLIVARES_CODEX_HOOK_PEP_CONFIG JSON. A missing
// path yields an empty config (nothing mounted); a supplied path must be readable and valid
// or startup fails closed.
func loadCodexHookPEPConfig() (codexHookPEPConfig, error) {
	path := os.Getenv("OLIVARES_CODEX_HOOK_PEP_CONFIG")
	if path == "" {
		return codexHookPEPConfig{}, nil
	}
	var cfg codexHookPEPConfig
	if err := loadOperatorJSONConfig("OLIVARES_CODEX_HOOK_PEP_CONFIG", path, &cfg); err != nil {
		return codexHookPEPConfig{}, err
	}
	return cfg, nil
}

// buildCodexHookPEPServer constructs the Codex hook PEP server, or nil when unconfigured.
// It is built AFTER boot so the authenticator, the PDP and the sessions identity plane are
// all live.
func buildCodexHookPEPServer(eng *engine, sess *sessions.Module, log *slog.Logger) (*http.Server, error) {
	cfg, err := loadCodexHookPEPConfig()
	if err != nil {
		return nil, fmt.Errorf("load codex hook PEP operator config: %w", err)
	}
	// the same deny-closed policy every configured-tenant reader now shares. Fail
	// closed rather than mount a surface whose decisions would be filed nowhere sensible:
	// a governed endpoint with a broken tenant is worse than no endpoint. An ABSENT
	// tenant is the un-provisioned case and mounts nothing, without erroring.
	tid, present, err := parseBusinessTenant("codex hook PEP config: tenant", cfg.Tenant)
	if err != nil {
		return nil, err
	}
	if !present {
		return nil, nil // not provisioned: mount nothing
	}
	if sess == nil {
		return nil, fmt.Errorf("codex hook PEP: the session identity plane is not wired; refusing to mount an endpoint that cannot attribute a decision")
	}

	dec := &codexHookDecider{
		tenant:   tid,
		authr:    eng.authr,
		eval:     eng.policyEval,
		sessions: sess,
		store:    eng.store,
		clock:    time.Now,
		log:      log,
	}

	pep := session.NewPEP(dec, codexObserver(eng, tid, log), time.Now)
	pep.OnEmitPanic(func(hookEvent string, cause any) {
		log.Error("codex-hook: the observation for a governed decision was LOST to a panic; the verdict stood but the evidence has a gap",
			"event", hookEvent, "cause", fmt.Sprint(cause))
	})

	listen := strings.TrimSpace(cfg.Listen)
	if listen == "" {
		listen = defaultCodexHookPEPListen
	}
	mux := http.NewServeMux()
	mux.Handle("/", pep)
	log.Info("codex hook PEP mounted", "listen", listen, "tenant", tid.String())
	return &http.Server{
		Addr:              listen,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}, nil
}

// codexObserver lifts each governed Codex fact onto the engine bus, where modules/sessions
// folds it into the live view. It is the ONLY emit path, and it writes nothing to the
// ledger — that is the decider's job and only the decider's (R-01).
func codexObserver(eng *engine, tenant model.TenantID, log *slog.Logger) session.Observer {
	return func(req session.Request, dec session.Decision) {
		emit := func(o sdkmodel.Observation) {
			if eng == nil || eng.bus == nil {
				return
			}
			// Best-effort, and deliberately so: the verdict has already been decided and
			// anchored by this point. A bus that will not take the observation costs us a
			// row in the live view, which is a visible gap; it must not retroactively
			// change what the agent was told.
			// BOUNDED. The bus applies backpressure by design, and this call sits on the
			// hook's critical path: an unbounded publish would let a slow consumer stall
			// the agent's tool-call for as long as it liked. The verdict is already
			// decided and anchored by now, so the right trade here is to lose the
			// observation loudly rather than to hold the agent.
			pctx, cancel := context.WithTimeout(context.Background(), codexPublishTimeout)
			defer cancel()
			if err := eng.bus.Publish(pctx, event.FromObservation(tenant.String(), codexSignalSource, o)); err != nil && log != nil {
				log.Warn("codex-hook: could not publish the session observation; the live view will be missing this fact",
					"event", req.Event, "err", err)
			}
		}
		if edge, ok := session.EdgeFor(req, dec); ok {
			emit(edge)
		}
		if f, ok := session.LifecycleFinding(req, dec); ok {
			emit(f)
		}
		if f, ok := session.DenyFinding(req, dec); ok {
			emit(f)
		}
	}
}
