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
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/compliance"
)

// erasurewiring.go adapts the two RTBF seams only the composition root can
// provide: the ACCOUNT leg (engine user anonymization — user rows live in the
// system tenant behind store.AuthScope, unreachable from any module) and the
// PROVIDER leg (the Anthropic Compliance DELETE passthrough, whose
// dual-control gate needs the approval bridge and whose delete credential is
// operator-provisioned). Both stay honest when unconfigured: compliance keeps its
// not-attempted / not-wired defaults and every erasure receipt records the gap.

// ---- the account leg --------------------------------------------------------------

// accountEraserAdapter anonymizes engine user accounts in the auth partition. It is
// constructed in buildModules (before the store exists) and late-bound to the store
// by boot() — the knowledgeGuard pattern.
type accountEraserAdapter struct {
	mu  sync.RWMutex
	st  store.Store
	log *slog.Logger
}

func (a *accountEraserAdapter) useStore(st store.Store) {
	a.mu.Lock()
	a.st = st
	a.mu.Unlock()
}

var _ compliance.AccountEraser = (*accountEraserAdapter)(nil)

// EraseAccount anonymizes every account matching one of the subject's identifiers
// (an email — matched normalized — or a user id). The rules, in order:
//
//   - a superadmin account is REFUSED (operational safety: an RTBF case against the
//     operator's own root account is a manual, deliberate ceremony);
//   - an account holding memberships in OTHER tenants is REFUSED (one tenant's DSR
//     cannot erase a principal shared with another tenant);
//   - otherwise the account is ANONYMIZED in place, never hard-deleted: the id
//     survives so ledger actors ("user:<id>") resolve to a tombstone — email,
//     display name, SCIM external id and password hash are destroyed, the account
//     is deactivated, its memberships in the requesting tenant are removed, its
//     panel sessions are deleted (they carry a client IP) and its API tokens are
//     revoked with their operator-given names scrubbed.
//
// Everything runs in ONE auth-partition transaction with a system-tenant
// self-audit per anonymized account (ids only — the erased email never appears).
func (a *accountEraserAdapter) EraseAccount(ctx context.Context, tenant model.TenantID, refs []string, requestedBy, requestedByKind string) (compliance.AccountEraseOutcome, error) {
	a.mu.RLock()
	st := a.st
	a.mu.RUnlock()
	if st == nil {
		return compliance.AccountEraseOutcome{}, errors.New("account eraser has no store handle yet (boot incomplete)")
	}
	if requestedByKind == "" {
		requestedByKind = model.ActorSystem
	}
	out := compliance.AccountEraseOutcome{Attempted: true}
	var notes []string
	err := st.AuthMutate(ctx, func(as store.AuthScope) error {
		for _, ref := range refs {
			user, found, err := findUserByRef(ctx, as, ref)
			if err != nil {
				return err
			}
			if !found {
				continue
			}
			if user.IsSuperadmin {
				notes = append(notes, "a matching superadmin account was refused (manual ceremony required)")
				continue
			}
			memberships, err := listAllAuth(ctx, as.Memberships(), eqFilter("user_id", user.ID.String()))
			if err != nil {
				return err
			}
			foreign := false
			for _, mb := range memberships {
				if mb.TargetTenantID != tenant {
					foreign = true
					break
				}
			}
			if foreign {
				notes = append(notes, "a matching account holds memberships in other tenants and was refused")
				continue
			}
			if err := anonymizeUser(ctx, as, user, memberships); err != nil {
				return err
			}
			if _, err := as.Audit().Append(ctx, model.AuditDraft{
				Actor: requestedBy, ActorKind: requestedByKind, Action: "auth.user.erase",
				TargetKind: "core.user", TargetID: user.ID,
				Meta: map[string]any{"tenant": tenant.String(), "reason": "rtbf"},
			}); err != nil {
				return err
			}
			out.Erased++
		}
		return nil
	})
	if err != nil {
		return compliance.AccountEraseOutcome{}, err
	}
	if out.Erased == 0 && len(notes) == 0 {
		notes = append(notes, "no matching engine account (it may have been anonymized by a prior run)")
	}
	out.Detail = strings.Join(notes, "; ")
	return out, nil
}

// eqFilter is a tiny model.Filter helper for the auth queries below.
func eqFilter(col string, val any) model.Filter {
	return model.Filter{Column: col, Op: model.OpEq, Value: val}
}

// listAllAuth pages a typed auth repository fully (the store's default List page
// is 100 rows — a partial read here would be silent under-erasure).
func listAllAuth[T any](ctx context.Context, repo store.Repository[T], filters ...model.Filter) ([]T, error) {
	var out []T
	cursor := ""
	for {
		page, p, err := repo.List(ctx, model.Query{Filters: filters, Limit: 500, Cursor: cursor})
		if err != nil {
			return nil, err
		}
		out = append(out, page...)
		if !p.HasMore || p.Cursor == "" {
			return out, nil
		}
		cursor = p.Cursor
	}
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

// anonymizeUser is the in-place account tombstone + credential revocation.
func anonymizeUser(ctx context.Context, as store.AuthScope, user model.User, memberships []model.Membership) error {
	user.Email = "erased-" + strings.ToLower(user.ID.String()) + "@erased.invalid"
	user.DisplayName = "[erased]"
	user.ExternalID = ""
	user.PasswordHash = ""
	user.Status = model.StatusInactive
	if _, err := as.Users().Update(ctx, user); err != nil {
		return err
	}
	for _, mb := range memberships {
		if err := as.Memberships().Delete(ctx, mb.ID); err != nil {
			return err
		}
	}
	// Panel sessions carry a client IP: delete the rows outright (fully paged —
	// a default List page would silently leave sessions behind).
	sessions, err := listAllAuth(ctx, as.Sessions(), eqFilter("user_id", user.ID.String()))
	if err != nil {
		return err
	}
	for _, s := range sessions {
		if err := as.Sessions().Delete(ctx, s.ID); err != nil {
			return err
		}
	}
	// API tokens stay as revoked rows (the credential's existence is audit-relevant)
	// but their operator-given names — free text a person may be named in — are
	// scrubbed.
	tokens, err := listAllAuth(ctx, as.Tokens(), eqFilter("user_id", user.ID.String()))
	if err != nil {
		return err
	}
	for _, t := range tokens {
		t.Revoked = true
		t.Name = "[erased]"
		if _, err := as.Tokens().Update(ctx, t); err != nil {
			return err
		}
	}
	return nil
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
