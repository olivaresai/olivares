// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package finops

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

const (
	policyKindSpendLimit = "spend_limit"
	microUSDPerCent      = int64(10_000)
)

var ErrInvalidSpendLimit = errors.New("invalid spend limit")

// SpendLimitScope is the apps-gateway principal scope.
type SpendLimitScope struct {
	Type        string `json:"type"`
	UserID      string `json:"user_id,omitempty"`
	RBACGroupID string `json:"rbac_group_id,omitempty"`
}

// SpendLimitSpec is the create-or-replace input. Amount is integer USD cents;
// nil means unlimited. Currency is USD when omitted.
type SpendLimitSpec struct {
	Scope    SpendLimitScope `json:"scope"`
	Amount   *string         `json:"amount"`
	Currency string          `json:"currency,omitempty"`
	Period   string          `json:"period"`
}

// SpendLimit is the stable wire representation returned by the admin API.
type SpendLimit struct {
	Type      string          `json:"type"`
	ID        string          `json:"id"`
	CreatedAt string          `json:"created_at"`
	UpdatedAt string          `json:"updated_at"`
	Scope     SpendLimitScope `json:"scope"`
	Amount    *string         `json:"amount"`
	Currency  string          `json:"currency"`
	Period    string          `json:"period"`
}

// SpendLimitListResult is an id-keyset page in creation order.
type SpendLimitListResult struct {
	Data    []SpendLimit
	HasMore bool
}

// SpendLimitSource identifies the policy scope selected for a principal.
type SpendLimitSource struct {
	Type        string `json:"type"`
	RBACGroupID string `json:"rbac_group_id,omitempty"`
}

// SpendLimitActor follows the Admin API SpendSummary actor shape.
type SpendLimitActor struct {
	Type         string  `json:"type"`
	UserID       string  `json:"user_id"`
	Name         *string `json:"name"`
	EmailAddress *string `json:"email_address"`
	Deleted      bool    `json:"deleted"`
}

// SpendLimitEffectiveRow is one principal×period resolved cap and spend row.
type SpendLimitEffectiveRow struct {
	Scope             SpendLimitScope   `json:"scope"`
	Actor             SpendLimitActor   `json:"actor"`
	Amount            *string           `json:"amount"`
	Currency          string            `json:"currency"`
	Period            string            `json:"period"`
	Source            *SpendLimitSource `json:"source"`
	SpendLimitID      *string           `json:"spend_limit_id"`
	PeriodToDateSpend string            `json:"period_to_date_spend"`
	Groups            []string          `json:"groups"`
}

// SpendLimitEffectiveOptions controls the effective-view filters and paging.
type SpendLimitEffectiveOptions struct {
	UserIDs []string
	Periods []string
	Query   string
	Sort    string
	Limit   int
	Page    string
}

// SpendLimitEffectiveResult pages principals; all requested period rows for a
// selected principal are returned together.
type SpendLimitEffectiveResult struct {
	Data     []SpendLimitEffectiveRow
	NextPage string
}

// SpendLimitAuditEvent is the local administrative mutation trail.
type SpendLimitAuditEvent struct {
	Type         string      `json:"type"`
	ID           string      `json:"id"`
	CreatedAt    string      `json:"created_at"`
	Actor        string      `json:"actor"`
	Action       string      `json:"action"`
	SpendLimitID string      `json:"spend_limit_id"`
	Before       *SpendLimit `json:"before"`
	After        *SpendLimit `json:"after"`
}

// SpendLimitAuditResult is a newest-first exact page.
type SpendLimitAuditResult struct {
	Data    []SpendLimitAuditEvent
	HasMore bool
}

// SpendLimitCheck is the per-seat pre-flight result.
type SpendLimitCheck struct {
	Allowed       bool
	Period        string
	SpendLimitID  string
	SpendMicroUSD int64
	LimitMicroUSD int64
}

type storedSpendLimitSpec struct {
	ScopeType      string `json:"scope_type"`
	ScopeKey       string `json:"scope_key,omitempty"`
	AmountMicroUSD int64  `json:"amount_micro_usd"`
	Unlimited      bool   `json:"unlimited"`
	Period         string `json:"period"`
}

func normalizeSpendLimitSpec(in SpendLimitSpec) (storedSpendLimitSpec, error) {
	in.Scope.Type = strings.TrimSpace(in.Scope.Type)
	in.Scope.UserID = strings.TrimSpace(in.Scope.UserID)
	in.Scope.RBACGroupID = strings.TrimSpace(in.Scope.RBACGroupID)
	var key string
	switch in.Scope.Type {
	case "user":
		if in.Scope.UserID == "" || in.Scope.RBACGroupID != "" {
			return storedSpendLimitSpec{}, fmt.Errorf("%w: user scope requires only user_id", ErrInvalidSpendLimit)
		}
		if !(strings.HasPrefix(in.Scope.UserID, "user:") || strings.HasPrefix(in.Scope.UserID, "token:")) || strings.HasSuffix(in.Scope.UserID, ":") {
			return storedSpendLimitSpec{}, fmt.Errorf("%w: user_id must be an Olivares actor ref (user:<id> or token:<id>), not an OIDC subject — see the /protocol divergences", ErrInvalidSpendLimit)
		}
		key = in.Scope.UserID
	case "rbac_group":
		if in.Scope.RBACGroupID == "" || in.Scope.UserID != "" {
			return storedSpendLimitSpec{}, fmt.Errorf("%w: rbac_group scope requires only rbac_group_id", ErrInvalidSpendLimit)
		}
		if _, err := model.ParseID(in.Scope.RBACGroupID); err != nil {
			return storedSpendLimitSpec{}, fmt.Errorf("%w: rbac_group_id must be an Olivares directory-group id, not an IdP group name — see the /protocol divergences", ErrInvalidSpendLimit)
		}
		key = in.Scope.RBACGroupID
	case "organization":
		if in.Scope.UserID != "" || in.Scope.RBACGroupID != "" {
			return storedSpendLimitSpec{}, fmt.Errorf("%w: organization scope accepts no key", ErrInvalidSpendLimit)
		}
	default:
		return storedSpendLimitSpec{}, fmt.Errorf("%w: scope.type must be user, rbac_group, or organization", ErrInvalidSpendLimit)
	}
	if in.Period != "daily" && in.Period != "weekly" && in.Period != "monthly" {
		return storedSpendLimitSpec{}, fmt.Errorf("%w: period must be daily, weekly, or monthly", ErrInvalidSpendLimit)
	}
	if in.Currency != "" && in.Currency != "USD" {
		return storedSpendLimitSpec{}, fmt.Errorf("%w: currency must be USD", ErrInvalidSpendLimit)
	}
	out := storedSpendLimitSpec{ScopeType: in.Scope.Type, ScopeKey: key, Period: in.Period, Unlimited: in.Amount == nil}
	if in.Amount != nil {
		if *in.Amount == "" || strings.HasPrefix(*in.Amount, "+") || strings.HasPrefix(*in.Amount, "-") {
			return storedSpendLimitSpec{}, fmt.Errorf("%w: amount must be a non-negative integer cents string or null", ErrInvalidSpendLimit)
		}
		cents, err := strconv.ParseInt(*in.Amount, 10, 64)
		if err != nil || cents < 0 || cents > (int64(^uint64(0)>>1)/microUSDPerCent) {
			return storedSpendLimitSpec{}, fmt.Errorf("%w: amount must be a non-negative integer cents string or null", ErrInvalidSpendLimit)
		}
		out.AmountMicroUSD = cents * microUSDPerCent
	}
	return out, nil
}

func (s storedSpendLimitSpec) mapValue() map[string]any {
	return map[string]any{
		"scope_type": s.ScopeType, "scope_key": s.ScopeKey,
		"amount_micro_usd": s.AmountMicroUSD, "unlimited": s.Unlimited, "period": s.Period,
	}
}

func parseStoredSpendLimit(p model.Policy) storedSpendLimitSpec {
	return storedSpendLimitSpec{
		ScopeType: specString(p.Spec, "scope_type"), ScopeKey: specString(p.Spec, "scope_key"),
		AmountMicroUSD: specInt64(p.Spec, "amount_micro_usd"), Unlimited: specBool(p.Spec, "unlimited"),
		Period: specString(p.Spec, "period"),
	}
}

func spendLimitWireID(id model.ID) string { return "spl_" + id.String() }

// ParseSpendLimitID validates and strips the wire prefix.
func ParseSpendLimitID(wire string) (model.ID, error) {
	if !strings.HasPrefix(wire, "spl_") {
		return "", store.ErrNotFound
	}
	id, err := model.ParseID(strings.TrimPrefix(wire, "spl_"))
	if err != nil {
		return "", store.ErrNotFound
	}
	return id, nil
}

func spendLimitFromPolicy(p model.Policy) SpendLimit {
	s := parseStoredSpendLimit(p)
	scope := SpendLimitScope{Type: s.ScopeType}
	if s.ScopeType == "user" {
		scope.UserID = s.ScopeKey
	}
	if s.ScopeType == "rbac_group" {
		scope.RBACGroupID = s.ScopeKey
	}
	var amount *string
	if !s.Unlimited {
		v := strconv.FormatInt(s.AmountMicroUSD/microUSDPerCent, 10)
		amount = &v
	}
	return SpendLimit{
		Type: "spend_limit", ID: spendLimitWireID(p.ID), CreatedAt: p.CreatedAt.String(), UpdatedAt: p.UpdatedAt.String(),
		Scope: scope, Amount: amount, Currency: "USD", Period: s.Period,
	}
}

func listSpendLimitPolicies(ctx context.Context, sc store.Scope) ([]model.Policy, error) {
	q := model.Query{Filters: []model.Filter{eq("kind", policyKindSpendLimit)}, Limit: listCap}
	var out []model.Policy
	for {
		rows, page, err := sc.Policies().List(ctx, q)
		if err != nil {
			return nil, err
		}
		out = append(out, rows...)
		if !page.HasMore {
			return out, nil
		}
		q.Cursor = page.Cursor
	}
}

// findSpendLimitPolicies returns EVERY policy row for the logical (scope,
// period) key, lowest id first. The store has no unique constraint on that key
// (the spec is JSON), so two concurrent upserts on Postgres can both insert;
// the caller treats the first row as canonical and heals the rest.
func findSpendLimitPolicies(ctx context.Context, sc store.Scope, spec storedSpendLimitSpec) ([]model.Policy, error) {
	rows, err := listSpendLimitPolicies(ctx, sc)
	if err != nil {
		return nil, err
	}
	var out []model.Policy
	for _, p := range rows {
		s := parseStoredSpendLimit(p)
		if s.ScopeType == spec.ScopeType && s.ScopeKey == spec.ScopeKey && s.Period == spec.Period {
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// SpendLimitUpsert creates or replaces a cap in place and audits atomically.
func (m *Module) SpendLimitUpsert(ctx context.Context, tenant model.TenantID, in SpendLimitSpec, adminActor string) (SpendLimit, bool, error) {
	spec, err := normalizeSpendLimitSpec(in)
	if err != nil {
		return SpendLimit{}, false, err
	}
	if m.data == nil {
		return SpendLimit{}, false, errors.New("finops: data unavailable")
	}
	var out SpendLimit
	created := false
	err = m.data.Mutate(ctx, tenant, func(sc store.Scope) error {
		matches, err := findSpendLimitPolicies(ctx, sc, spec)
		if err != nil {
			return err
		}
		var before *SpendLimit
		action := "create"
		var p model.Policy
		if len(matches) > 0 {
			// The lowest-id row is canonical; any extra rows are duplicates left by a
			// concurrent-insert race and are healed here, inside the same transaction,
			// so exactly one row holds the logical (scope, period) key after commit.
			p = matches[0]
			v := spendLimitFromPolicy(p)
			before = &v
			for _, dup := range matches[1:] {
				if err := sc.Policies().Delete(ctx, dup.ID); err != nil {
					return err
				}
			}
			p.Spec = spec.mapValue()
			p.Enabled = true
			p.Name = spendLimitPolicyName(spec)
			p, err = sc.Policies().Update(ctx, p)
			action = "update"
		} else {
			p, err = sc.Policies().Create(ctx, model.Policy{Name: spendLimitPolicyName(spec), Kind: policyKindSpendLimit, Enabled: true, Spec: spec.mapValue()})
			created = true
		}
		if err != nil {
			return err
		}
		out = spendLimitFromPolicy(p)
		return createSpendLimitAudit(ctx, sc, strings.TrimSpace(adminActor), action, out.ID, before, &out)
	})
	return out, created, err
}

func spendLimitPolicyName(s storedSpendLimitSpec) string {
	key := s.ScopeKey
	if key == "" {
		key = "default"
	}
	return "apps-gateway spend limit " + s.ScopeType + ":" + key + ":" + s.Period
}

// SpendLimitGet returns one spend-limit policy by internal id.
func (m *Module) SpendLimitGet(ctx context.Context, tenant model.TenantID, id model.ID) (SpendLimit, error) {
	if m.data == nil {
		return SpendLimit{}, errors.New("finops: data unavailable")
	}
	var out SpendLimit
	err := m.data.View(ctx, tenant, func(sc store.Scope) error {
		p, err := sc.Policies().Get(ctx, id)
		if err != nil {
			return err
		}
		if p.Kind != policyKindSpendLimit {
			return store.ErrNotFound
		}
		out = spendLimitFromPolicy(p)
		return nil
	})
	return out, err
}

// SpendLimitDelete removes a cap and writes its before snapshot atomically.
func (m *Module) SpendLimitDelete(ctx context.Context, tenant model.TenantID, id model.ID, adminActor string) error {
	if m.data == nil {
		return errors.New("finops: data unavailable")
	}
	return m.data.Mutate(ctx, tenant, func(sc store.Scope) error {
		p, err := sc.Policies().Get(ctx, id)
		if err != nil {
			return err
		}
		if p.Kind != policyKindSpendLimit {
			return store.ErrNotFound
		}
		before := spendLimitFromPolicy(p)
		if err := sc.Policies().Delete(ctx, id); err != nil {
			return err
		}
		return createSpendLimitAudit(ctx, sc, strings.TrimSpace(adminActor), "delete", before.ID, &before, nil)
	})
}

// SpendLimitList returns an exact limit+1 keyset page. before traverses toward
// older ids but the returned rows remain in creation order.
func (m *Module) SpendLimitList(ctx context.Context, tenant model.TenantID, limit int, afterID, beforeID model.ID) (SpendLimitListResult, error) {
	if m.data == nil {
		return SpendLimitListResult{}, errors.New("finops: data unavailable")
	}
	if limit < 1 || limit > 1000 {
		return SpendLimitListResult{}, fmt.Errorf("%w: limit must be between 1 and 1000", ErrInvalidSpendLimit)
	}
	var out SpendLimitListResult
	err := m.data.View(ctx, tenant, func(sc store.Scope) error {
		filters := []model.Filter{eq("kind", policyKindSpendLimit)}
		q := model.Query{Filters: filters, Limit: limit + 1}
		if !afterID.IsZero() {
			q.Filters = append(q.Filters, model.Filter{Column: model.ColID, Op: model.OpGt, Value: afterID.String()})
		}
		if !beforeID.IsZero() {
			q.Filters = append(q.Filters, model.Filter{Column: model.ColID, Op: model.OpLt, Value: beforeID.String()})
			q.Sort = []model.Sort{{Column: model.ColID, Desc: true}}
		}
		rows, storePage, err := sc.Policies().List(ctx, q)
		if err != nil {
			return err
		}
		out.HasMore = storePage.HasMore || len(rows) > limit
		if out.HasMore {
			rows = rows[:limit]
		}
		if !beforeID.IsZero() {
			sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
		}
		out.Data = make([]SpendLimit, 0, len(rows))
		for _, p := range rows {
			out.Data = append(out.Data, spendLimitFromPolicy(p))
		}
		return nil
	})
	return out, err
}

func createSpendLimitAudit(ctx context.Context, sc store.Scope, actor, action, id string, before, after *SpendLimit) error {
	repo, err := sc.Ext(spendLimitAuditKind)
	if err != nil {
		return err
	}
	rec := model.Record{colSpendAuditActor: actor, colSpendAuditAction: action, colSpendAuditLimitID: id}
	if before != nil {
		b, _ := json.Marshal(before)
		rec[colSpendAuditBefore] = string(b)
	}
	if after != nil {
		b, _ := json.Marshal(after)
		rec[colSpendAuditAfter] = string(b)
	}
	_, err = repo.Create(ctx, rec)
	return err
}

// SpendLimitAudit returns exact newest-first mutation history.
func (m *Module) SpendLimitAudit(ctx context.Context, tenant model.TenantID, limit int) (SpendLimitAuditResult, error) {
	if m.data == nil {
		return SpendLimitAuditResult{}, errors.New("finops: data unavailable")
	}
	if limit < 1 || limit > 1000 {
		return SpendLimitAuditResult{}, fmt.Errorf("%w: limit must be between 1 and 1000", ErrInvalidSpendLimit)
	}
	var out SpendLimitAuditResult
	err := m.data.View(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(spendLimitAuditKind)
		if err != nil {
			return err
		}
		recs, storePage, err := repo.List(ctx, model.Query{Limit: limit + 1, Sort: []model.Sort{{Column: model.ColCreatedAt, Desc: true}, {Column: model.ColID, Desc: true}}})
		if err != nil {
			return err
		}
		out.HasMore = storePage.HasMore || len(recs) > limit
		if out.HasMore {
			recs = recs[:limit]
		}
		for _, r := range recs {
			ev := SpendLimitAuditEvent{Type: "spend_limit_audit_event", ID: r.String(model.ColID), CreatedAt: r.String(model.ColCreatedAt), Actor: r.String(colSpendAuditActor), Action: r.String(colSpendAuditAction), SpendLimitID: r.String(colSpendAuditLimitID)}
			if raw := r.String(colSpendAuditBefore); raw != "" {
				var v SpendLimit
				if json.Unmarshal([]byte(raw), &v) == nil {
					ev.Before = &v
				}
			}
			if raw := r.String(colSpendAuditAfter); raw != "" {
				var v SpendLimit
				if json.Unmarshal([]byte(raw), &v) == nil {
					ev.After = &v
				}
			}
			out.Data = append(out.Data, ev)
		}
		return nil
	})
	return out, err
}

type resolvedSpendLimit struct {
	policy *model.Policy
	spec   storedSpendLimitSpec
}

// moreRestrictive reports whether candidate binds tighter than current (a
// numeric amount beats unlimited; a smaller amount beats a larger one). It is
// the tie-break BOTH for multi-group membership (vendor `group_limit_mode:
// min`) and for duplicate rows of one logical key (the concurrent-insert race
// the upsert heals) — under duplicates the tightest cap governs, never a stale
// looser one.
func moreRestrictive(current *storedSpendLimitSpec, candidate storedSpendLimitSpec) bool {
	if current == nil {
		return true
	}
	if candidate.Unlimited {
		return false
	}
	return current.Unlimited || candidate.AmountMicroUSD < current.AmountMicroUSD
}

func resolveSpendLimitForPeriod(policies []model.Policy, actor, period string, groups []string) resolvedSpendLimit {
	var user, group, org *model.Policy
	var userSpec, groupSpec, orgSpec storedSpendLimitSpec
	for i := range policies {
		p := &policies[i]
		s := parseStoredSpendLimit(*p)
		if s.Period != period {
			continue
		}
		switch {
		case s.ScopeType == "user" && s.ScopeKey == actor:
			if user == nil || moreRestrictive(&userSpec, s) {
				user, userSpec = p, s
			}
		case s.ScopeType == "organization":
			if org == nil || moreRestrictive(&orgSpec, s) {
				org, orgSpec = p, s
			}
		case s.ScopeType == "rbac_group" && contains(groups, s.ScopeKey):
			if group == nil || moreRestrictive(&groupSpec, s) {
				group, groupSpec = p, s
			}
		}
	}
	if user != nil {
		return resolvedSpendLimit{policy: user, spec: userSpec}
	}
	if group != nil {
		return resolvedSpendLimit{policy: group, spec: groupSpec}
	}
	if org != nil {
		return resolvedSpendLimit{policy: org, spec: orgSpec}
	}
	return resolvedSpendLimit{}
}

func groupsByActor(ctx context.Context, reader groupAuthReader, tenant model.TenantID, policies []model.Policy) (map[string][]string, error) {
	out := map[string][]string{}
	seen := map[string]bool{}
	for _, p := range policies {
		s := parseStoredSpendLimit(p)
		if s.ScopeType != "rbac_group" || seen[s.ScopeKey] {
			continue
		}
		seen[s.ScopeKey] = true
		if reader == nil {
			return nil, errors.New("finops: group reader unavailable")
		}
		refs, err := userGroupClosureMemberRefs(ctx, reader, tenant, s.ScopeKey)
		if err != nil {
			return nil, err
		}
		for _, ref := range refs {
			out["user:"+ref] = append(out["user:"+ref], s.ScopeKey)
		}
	}
	for actor := range out {
		sort.Strings(out[actor])
	}
	return out, nil
}

// userGroupClosureMemberRefs returns the user refs a group cap governs: the
// group's direct members plus the members of every group NESTED UNDER it. A
// child-group member is a subject of all its ancestors (S256), and enforcement
// matches group caps against that closure via principal.GroupsIn — the
// effective view must see the same population, or it would report a different
// cap than the one that actually binds. The walk never leaves the cap group's
// tenant (target_tenant_id is the isolation column) and is cycle-safe.
func userGroupClosureMemberRefs(ctx context.Context, reader groupAuthReader, tenant model.TenantID, key string) ([]string, error) {
	groupID, err := model.ParseID(key)
	if err != nil {
		return nil, fmt.Errorf("finops: rbac_group key %q must be a group id: %w", key, err)
	}
	var refs []string
	err = reader.AuthView(ctx, func(as store.AuthScope) error {
		root, err := as.Groups().Get(ctx, groupID)
		if err != nil {
			return err
		}
		if root.TargetTenantID != tenant {
			return fmt.Errorf("finops: rbac_group %q is not scoped to tenant %s", key, tenant)
		}
		children := map[model.ID][]model.ID{}
		gq := model.Query{Filters: []model.Filter{{Column: "target_tenant_id", Op: model.OpEq, Value: tenant.String()}}, Limit: listCap}
		for {
			groups, page, err := as.Groups().List(ctx, gq)
			if err != nil {
				return err
			}
			for _, g := range groups {
				if !g.ParentGroupID.IsZero() {
					children[g.ParentGroupID] = append(children[g.ParentGroupID], g.ID)
				}
			}
			if !page.HasMore || page.Cursor == "" {
				break
			}
			gq.Cursor = page.Cursor
		}
		seenGroups := map[model.ID]bool{}
		seenRefs := map[string]bool{}
		queue := []model.ID{groupID}
		for len(queue) > 0 {
			g := queue[0]
			queue = queue[1:]
			if seenGroups[g] {
				continue
			}
			seenGroups[g] = true
			queue = append(queue, children[g]...)
			mq := model.Query{Filters: []model.Filter{eq("group_id", g.String())}, Limit: listCap}
			for {
				members, page, err := as.GroupMembers().List(ctx, mq)
				if err != nil {
					return err
				}
				for _, member := range members {
					if ref := member.UserID.String(); ref != "" && !seenRefs[ref] {
						seenRefs[ref] = true
						refs = append(refs, ref)
					}
				}
				if !page.HasMore || page.Cursor == "" {
					break
				}
				mq.Cursor = page.Cursor
			}
		}
		sort.Strings(refs)
		return nil
	})
	return refs, err
}

func normalizeEffectivePeriods(periods []string) ([]string, error) {
	if len(periods) == 0 {
		return []string{"daily", "weekly", "monthly"}, nil
	}
	seen := map[string]bool{}
	var out []string
	for _, p := range periods {
		if p != "daily" && p != "weekly" && p != "monthly" {
			return nil, fmt.Errorf("%w: invalid period", ErrInvalidSpendLimit)
		}
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	return out, nil
}

// SpendLimitEffective resolves caps and period-to-date spend per principal.
func (m *Module) SpendLimitEffective(ctx context.Context, tenant model.TenantID, opts SpendLimitEffectiveOptions) (SpendLimitEffectiveResult, error) {
	if m.data == nil {
		return SpendLimitEffectiveResult{}, errors.New("finops: data unavailable")
	}
	periods, err := normalizeEffectivePeriods(opts.Periods)
	if err != nil {
		return SpendLimitEffectiveResult{}, err
	}
	if opts.Limit < 1 || opts.Limit > 1000 {
		return SpendLimitEffectiveResult{}, fmt.Errorf("%w: limit must be between 1 and 1000", ErrInvalidSpendLimit)
	}
	if opts.Sort != "" && opts.Sort != "spend_desc" {
		return SpendLimitEffectiveResult{}, fmt.Errorf("%w: invalid sort", ErrInvalidSpendLimit)
	}
	if opts.Sort == "spend_desc" && len(periods) != 1 {
		return SpendLimitEffectiveResult{}, fmt.Errorf("%w: spend_desc requires exactly one period", ErrInvalidSpendLimit)
	}
	var out SpendLimitEffectiveResult
	var policies []model.Policy
	err = m.data.View(ctx, tenant, func(sc store.Scope) error {
		var err error
		policies, err = listSpendLimitPolicies(ctx, sc)
		return err
	})
	if err != nil {
		return out, err
	}
	// The auth partition is deliberately read outside the tenant View transaction:
	// SQLite serializes transactions, so nesting AuthView here would deadlock.
	groupMap, err := groupsByActor(ctx, authGroupReader(m.data), tenant, policies)
	if err != nil {
		return out, err
	}
	err = m.data.View(ctx, tenant, func(sc store.Scope) error {
		principals := map[string]bool{}
		if len(opts.UserIDs) > 0 {
			for _, u := range opts.UserIDs {
				if strings.TrimSpace(u) != "" {
					principals[strings.TrimSpace(u)] = true
				}
			}
		} else {
			for _, p := range policies {
				s := parseStoredSpendLimit(p)
				if s.ScopeType == "user" {
					principals[s.ScopeKey] = true
				}
			}
			for _, period := range periods {
				start, _ := periodStart(period, m.clock.Now().Time())
				end := periodEnd(period, start)
				trunc, err := scanSamples(ctx, sc, []model.Filter{
					estimatedFilter(),
					{Column: colOccurredAt, Op: model.OpGte, Value: model.NewTimestamp(start).String()},
					{Column: colOccurredAt, Op: model.OpLt, Value: model.NewTimestamp(end).String()},
				}, func(r model.Record) {
					if a := r.String(colActor); a != "" {
						principals[a] = true
					}
				})
				if err != nil {
					return err
				}
				// Truncation means the principal enumeration may be partial on a very
				// large estate — say so instead of presenting the page as exhaustive
				// (callers can still target specific principals via user_ids[]).
				if trunc && m.log != nil {
					m.log.Warn("finops: spend-limit effective principal scan truncated; enumeration may be partial", "period", period)
				}
			}
		}
		q := strings.ToLower(strings.TrimSpace(opts.Query))
		actors := make([]string, 0, len(principals))
		for a := range principals {
			if q == "" || strings.Contains(strings.ToLower(a), q) {
				actors = append(actors, a)
			}
		}
		sort.Strings(actors)
		spends := map[string]map[string]int64{}
		for _, actor := range actors {
			spends[actor] = map[string]int64{}
			for _, period := range periods {
				start, _ := periodStart(period, m.clock.Now().Time())
				end := periodEnd(period, start)
				agg, err := aggregatePeriod(ctx, sc, []model.Filter{eq(colActor, actor)}, start, true, end, true)
				if err != nil {
					return err
				}
				spends[actor][period] = agg.Cost
			}
		}
		if opts.Sort == "spend_desc" {
			p := periods[0]
			sort.SliceStable(actors, func(i, j int) bool {
				if spends[actors[i]][p] == spends[actors[j]][p] {
					return actors[i] < actors[j]
				}
				return spends[actors[i]][p] > spends[actors[j]][p]
			})
		}
		startIndex, err := decodeSpendPage(opts.Page)
		if err != nil || startIndex > len(actors) {
			return fmt.Errorf("%w: invalid page", ErrInvalidSpendLimit)
		}
		endIndex := startIndex + opts.Limit
		if endIndex > len(actors) {
			endIndex = len(actors)
		}
		if endIndex < len(actors) {
			out.NextPage = encodeSpendPage(endIndex)
		}
		for _, actor := range actors[startIndex:endIndex] {
			groups := append([]string(nil), groupMap[actor]...)
			if groups == nil {
				groups = []string{}
			}
			for _, period := range periods {
				resolved := resolveSpendLimitForPeriod(policies, actor, period, groups)
				row := SpendLimitEffectiveRow{Scope: SpendLimitScope{Type: "user", UserID: actor}, Actor: SpendLimitActor{Type: "user_actor", UserID: actor}, Currency: "USD", Period: period, PeriodToDateSpend: microUSDToCents(spends[actor][period]), Groups: groups}
				if resolved.policy != nil {
					wire := spendLimitFromPolicy(*resolved.policy)
					row.Amount = wire.Amount
					row.SpendLimitID = &wire.ID
					row.Source = &SpendLimitSource{Type: resolved.spec.ScopeType}
					if resolved.spec.ScopeType == "rbac_group" {
						row.Source.RBACGroupID = resolved.spec.ScopeKey
					}
				}
				out.Data = append(out.Data, row)
			}
		}
		return nil
	})
	return out, err
}

func encodeSpendPage(i int) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.Itoa(i)))
}
func decodeSpendPage(s string) (int, error) {
	if s == "" {
		return 0, nil
	}
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return 0, err
	}
	i, err := strconv.Atoi(string(b))
	if err != nil || i < 0 {
		return 0, errors.New("invalid page")
	}
	return i, nil
}

func microUSDToCents(v int64) string {
	neg := v < 0
	if neg {
		v = -v
	}
	whole, frac := v/microUSDPerCent, v%microUSDPerCent
	out := strconv.FormatInt(whole, 10)
	if frac != 0 {
		out += "." + strings.TrimRight(fmt.Sprintf("%04d", frac), "0")
	}
	if neg {
		out = "-" + out
	}
	return out
}

// CheckSpendLimit enforces the resolved per-seat cap independently per period.
func (m *Module) CheckSpendLimit(ctx context.Context, tenant model.TenantID, actorRef string, groups []string) (SpendLimitCheck, error) {
	allow := SpendLimitCheck{Allowed: true}
	if m.data == nil {
		return allow, nil
	}
	err := m.data.View(ctx, tenant, func(sc store.Scope) error {
		policies, err := listSpendLimitPolicies(ctx, sc)
		if err != nil {
			return err
		}
		for _, period := range []string{"daily", "weekly", "monthly"} {
			resolved := resolveSpendLimitForPeriod(policies, actorRef, period, groups)
			if resolved.policy == nil || resolved.spec.Unlimited {
				continue
			}
			start, _ := periodStart(period, m.clock.Now().Time())
			end := periodEnd(period, start)
			agg, err := aggregatePeriod(ctx, sc, []model.Filter{eq(colActor, actorRef)}, start, true, end, true)
			if err != nil {
				return err
			}
			// A truncated aggregate is only a lower bound, so it forces a deny even
			// when the observed partial cost remains below the configured limit.
			if agg.Truncated || agg.Cost >= resolved.spec.AmountMicroUSD {
				allow = SpendLimitCheck{Allowed: false, Period: period, SpendLimitID: spendLimitWireID(resolved.policy.ID), SpendMicroUSD: agg.Cost, LimitMicroUSD: resolved.spec.AmountMicroUSD}
				return nil
			}
		}
		return nil
	})
	if err != nil {
		return SpendLimitCheck{Allowed: true}, err
	}
	return allow, nil
}
