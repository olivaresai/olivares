// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	claudecompliance "github.com/olivaresai/olivares/connectors/claude-compliance"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/compliance"
)

// erasurewiring.go adapts the two RTBF seams only the composition root can
// provide: the ACCOUNT leg (the tenant's own hold on engine users — user rows live
// in the system tenant behind store.AuthScope, unreachable from any module) and the
// PROVIDER leg (the Anthropic Compliance DELETE passthrough, whose
// dual-control gate needs the approval bridge and whose delete credential is
// operator-provisioned). Both stay honest when unconfigured: compliance keeps its
// not-attempted / not-wired defaults and every erasure receipt records the gap.

// ---- the account leg --------------------------------------------------------------

// accountEraserAdapter removes a tenant's hold on engine user accounts in the auth
// partition. It is constructed in buildModules (before the store exists) and
// late-bound to the store by boot() — the knowledgeGuard pattern.
type accountEraserAdapter struct {
	mu    sync.RWMutex
	st    store.Store
	authr *auth.Authenticator
	log   *slog.Logger
}

// use binds the adapter to the opened store and to the engine's authenticator,
// whose offboard wakes the engine's retirement pump.
func (a *accountEraserAdapter) use(st store.Store, authr *auth.Authenticator) {
	a.mu.Lock()
	a.st, a.authr = st, authr
	a.mu.Unlock()
}

var _ compliance.AccountEraser = (*accountEraserAdapter)(nil)

// EraseAccount acts on every account matching one of the subject's identifiers
// (an email — matched normalized — or a user id). The rules, in order:
//
//   - a superadmin account is REFUSED (operational safety: an RTBF case against the
//     operator's own root account is a manual, deliberate ceremony);
//   - every other account keeps its global record, whatever its custody: the
//     tenant's erasure removes only what the tenant holds (the scoped offboard:
//     its membership, group rows, tenant tokens and tenant sessions here, with
//     the exclusion), and the receipt says the global record waits for the
//     deployment's erasure ceremony. One tenant cannot prove on its own that no
//     other tenant still relies on an account, so no tenant erasure anonymizes
//     one.
//
// Each account runs in its own auth-partition transaction with a system-tenant
// self-audit (ids only — the erased email never appears). When a later
// identifier fails, the outcome returned with the error still reports what the
// earlier ones committed; a re-execution repeats none of them, because an
// offboard of an account already removed writes nothing.
func (a *accountEraserAdapter) EraseAccount(ctx context.Context, tenant model.TenantID, refs []string, requestedBy, requestedByKind string) (compliance.AccountEraseOutcome, error) {
	a.mu.RLock()
	st, authr := a.st, a.authr
	a.mu.RUnlock()
	if st == nil || authr == nil {
		return compliance.AccountEraseOutcome{}, errors.New("account eraser has no store handle yet (boot incomplete)")
	}
	if requestedByKind == "" {
		requestedByKind = model.ActorSystem
	}
	offboarder, err := auth.NewSystemOperator("erasure", "a tenant's erasure request")
	if err != nil {
		return compliance.AccountEraseOutcome{}, err
	}
	out := compliance.AccountEraseOutcome{Attempted: true}
	var notes []string
	finish := func() compliance.AccountEraseOutcome {
		if len(notes) == 0 {
			notes = append(notes, "no matching engine account")
		}
		out.Detail = strings.Join(notes, "; ")
		return out
	}
	for _, ref := range refs {
		var note string
		err := st.AuthMutate(ctx, func(as store.AuthScope) error {
			note = ""
			user, found, err := findUserByRef(ctx, as, ref)
			if err != nil || !found {
				return err
			}
			if user.IsSuperadmin {
				note = "a matching superadmin account was refused (manual ceremony required)"
				return nil
			}
			if _, err := authr.OffboardFromTenant(ctx, as, offboarder, user.ID, tenant, "erasure"); err != nil {
				return err
			}
			if _, err := as.Audit().Append(ctx, model.AuditDraft{
				Actor: requestedBy, ActorKind: requestedByKind, Action: "auth.user.erase.tenant_hold",
				TargetKind: "core.user", TargetID: user.ID,
				Meta: map[string]any{"tenant": tenant.String(), "reason": "rtbf"},
			}); err != nil {
				return err
			}
			note = accountContainmentNote
			return nil
		})
		if err != nil {
			return finish(), err
		}
		if note != "" {
			notes = append(notes, note)
		}
	}
	return finish(), nil
}

// accountContainmentNote is what a tenant's erasure receipt says about a
// matching account: the tenant's own hold on it is gone, and its global record
// is left for the deployment's erasure ceremony.
const accountContainmentNote = "a matching account's membership in this tenant was removed; " +
	"its global record waits for the deployment's erasure ceremony"

// eqFilter is a tiny model.Filter helper for the auth queries below.
func eqFilter(col string, val any) model.Filter {
	return model.Filter{Column: col, Op: model.OpEq, Value: val}
}

// findUserByRef resolves an identifier to a user: by normalized email, by id, and
// by the ledger actor-ref form "user:<id>" (the alias shape the runbook documents).
func findUserByRef(ctx context.Context, as store.AuthScope, ref string) (model.User, bool, error) {
	email := strings.ToLower(strings.TrimSpace(ref))
	users, _, err := as.Users().List(ctx, model.Query{
		Filters: []model.Filter{eqFilter("email", email)}, Limit: 1,
	})
	if err != nil {
		return model.User{}, false, err
	}
	if len(users) > 0 {
		return users[0], true, nil
	}
	id := strings.TrimSpace(ref)
	if rest, ok := strings.CutPrefix(id, "user:"); ok {
		id = rest
	}
	user, err := as.Users().Get(ctx, model.ID(id))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return model.User{}, false, nil
		}
		return model.User{}, false, err
	}
	return user, true, nil
}

// ---- the provider leg -------------------------------------------------------------

// claudeEraserConfig is the operator provisioning of the RTBF actuator
// (OLIVARES_CLAUDE_ERASER_CONFIG, an operator-secret JSON file — the
// loadNHIActuatorsConfig pattern). delete_key is the delete:compliance_user_data
// Compliance Access Key; read_key the read:compliance_user_data key the
// enumeration uses; allowlist the deny-by-default (target, subjects) grants the
// connector PEP enforces (subjects "*" grants any subject of that target).
type claudeEraserConfig struct {
	BaseURL   string                            `json:"base_url"`
	Version   string                            `json:"version"`
	DeleteKey string                            `json:"delete_key"`
	ReadKey   string                            `json:"read_key"`
	Allowlist []claudecompliance.EraseAllowRule `json:"allowlist"`
}

func loadClaudeEraserConfig(_ *slog.Logger) (claudeEraserConfig, error) {
	path := os.Getenv("OLIVARES_CLAUDE_ERASER_CONFIG")
	if path == "" {
		return claudeEraserConfig{}, nil
	}
	var cfg claudeEraserConfig
	if err := loadOperatorJSONConfig("OLIVARES_CLAUDE_ERASER_CONFIG", path, &cfg); err != nil {
		return claudeEraserConfig{}, err
	}
	return cfg, nil
}

// providerEraserAdapter orchestrates the passthrough: enumerate the subject's
// provider-side content (minimal-data references), then route every deletion
// through the connector's own dual-control PEP (allowlist → PlanHash → CRITICAL
// "compliance.content.erase" gate → quorum re-check → DELETE). Each deletion holds
// its own approval — a first execute typically reports them PENDING; a re-execute
// consumes the approved grants. Deletions are paced (the Compliance API shares
// 600 req/min per parent org).
type providerEraserAdapter struct {
	cfg    claudeEraserConfig
	bridge *approvalBridge
	log    *slog.Logger
}

func newProviderEraserAdapter(cfg claudeEraserConfig, bridge *approvalBridge, log *slog.Logger) *providerEraserAdapter {
	if strings.TrimSpace(cfg.DeleteKey) == "" || bridge == nil {
		return nil
	}
	return &providerEraserAdapter{cfg: cfg, bridge: bridge, log: log}
}

var _ compliance.ProviderEraser = (*providerEraserAdapter)(nil)

// erasePace keeps a bulk RTBF fan-out far under the shared 600 req/min ceiling.
const erasePace = 250 * time.Millisecond

func (p *providerEraserAdapter) EraseProviderContent(ctx context.Context, tenant model.TenantID, req compliance.ProviderEraseRequest) (compliance.ProviderEraseOutcome, error) {
	out := compliance.ProviderEraseOutcome{Wired: true}
	if len(req.SubjectUserIDs) == 0 {
		out.Detail = "no provider user ids supplied; nothing to enumerate"
		return out, nil
	}
	chats, projects, err := p.enumerate(ctx, req.SubjectUserIDs)
	if err != nil {
		return compliance.ProviderEraseOutcome{}, err
	}
	out.Enumerated = len(chats) + len(projects)
	if out.Enumerated == 0 {
		out.Detail = "no provider-side content found for the supplied user ids"
		return out, nil
	}

	eraser := claudecompliance.NewEraser(claudecompliance.EraserConfig{
		BaseURL:   p.cfg.BaseURL,
		Version:   p.cfg.Version,
		DeleteKey: p.cfg.DeleteKey,
		Allowlist: claudecompliance.NewEraseAllowlist(p.cfg.Allowlist),
		Gate:      p.bridge.eraseGate(tenant),
		Auditor:   slogEraseAuditor{log: p.log},
	})
	spec := claudecompliance.EraseSpec{Tenant: tenant.String(), RequestedBy: req.RequestedBy, CaseRef: req.CaseRef}

	// Chats first: a project DELETE 409s while chats remain attached.
	for _, ref := range chats {
		p.count(eraser.EraseChat(ctx, ref.ID, spec), &out)
		if err := pace(ctx); err != nil {
			return out, err
		}
	}
	for _, ref := range projects {
		p.count(eraser.EraseProject(ctx, ref.ID, spec), &out)
		if err := pace(ctx); err != nil {
			return out, err
		}
	}
	return out, nil
}

// count folds one deletion result into the outcome: a PENDING gate verdict is
// honest partial progress (its approval is gathering), and an EXPIRED one is
// recoverable the same way (the next execute re-opens a fresh approval) — both
// defer the shred without counting as failures. Every other deny and any
// transport error is a FAILURE the receipt must not gloss; the orchestrator
// refuses to shred while a wired leg reports failures.
func (p *providerEraserAdapter) count(err error, out *compliance.ProviderEraseOutcome) {
	if err == nil {
		out.Erased++
		return
	}
	var deny *claudecompliance.EraseDenyError
	if errors.As(err, &deny) {
		switch deny.Status {
		case claudecompliance.ErasePending, claudecompliance.EraseExpired:
			out.Pending++
			return
		}
	}
	out.Failed++
	p.log.Warn("erasure: provider-side deletion failed", "err", err)
}

// enumerate lists the subject's provider-side content references through a
// read-scoped Source (deny-closed: no read key ⇒ no enumeration ⇒ honest error —
// claiming "0 items" without looking would fabricate completeness).
func (p *providerEraserAdapter) enumerate(ctx context.Context, userIDs []string) (chats, projects []claudecompliance.ContentRef, err error) {
	if strings.TrimSpace(p.cfg.ReadKey) == "" {
		return nil, nil, errors.New("provider eraser has no read_key; cannot enumerate the subject's provider-side content (configure read:compliance_user_data)")
	}
	src, err := claudecompliance.NewContentEnumerator(p.cfg.BaseURL, p.cfg.Version, p.cfg.ReadKey, nil)
	if err != nil {
		return nil, nil, err
	}
	if chats, err = src.EnumerateChats(ctx, userIDs); err != nil {
		return nil, nil, err
	}
	if projects, err = src.EnumerateProjects(ctx, userIDs); err != nil {
		return nil, nil, err
	}
	return chats, projects, nil
}

// pace sleeps the inter-delete interval, honoring cancellation.
func pace(ctx context.Context) error {
	t := time.NewTimer(erasePace)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// slogEraseAuditor records every connector-side erase decision to the operator log.
// The durable evidence is compliance's own custody trail + receipt; this is the
// operational echo (minimal data by construction — EraseRecord carries ids only).
type slogEraseAuditor struct{ log *slog.Logger }

func (a slogEraseAuditor) Record(_ context.Context, rec claudecompliance.EraseRecord) {
	a.log.Info("erasure: provider-side erase decision",
		"target", string(rec.Target), "subject", rec.SubjectRef, "allowed", rec.Allowed,
		"dual_control", rec.DualControl, "approvers", rec.ApproverCount, "reason", rec.Reason)
}
