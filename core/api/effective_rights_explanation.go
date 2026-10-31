// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

type effectiveRightsGrant struct {
	GrantID  string                 `json:"grant_id,omitempty"`
	PolicyID string                 `json:"policy_id,omitempty"`
	Node     effectiveRightsStep    `json:"node"`
	Subject  effectiveRightsSubject `json:"subject"`
}

type effectiveRightsSubject struct {
	Kind  string                `json:"kind"`
	Ref   string                `json:"ref"`
	Chain []effectiveRightsStep `json:"chain,omitempty"`
}

type effectiveRightsRemoval struct {
	Kind string               `json:"kind"`
	ID   string               `json:"id,omitempty"`
	Node *effectiveRightsStep `json:"node,omitempty"`
}

// effectiveRightsAttribution maps the decision's own provenance to the wire.
func effectiveRightsAttribution(tenant model.TenantID, explanation auth.DecisionExplanation) ([]effectiveRightsGrant, []effectiveRightsRemoval) {
	grants := make([]effectiveRightsGrant, 0, len(explanation.Because))
	for _, reason := range explanation.Because {
		node := reason.Node
		if node == (auth.DecisionNode{}) {
			node = auth.DecisionNode{Kind: "tenant", Ref: string(tenant)}
		}
		grants = append(grants, effectiveRightsGrant{
			GrantID:  reason.GrantID,
			PolicyID: reason.PolicyID,
			Node:     effectiveRightsStep{Kind: node.Kind, Ref: node.Ref},
			Subject:  effectiveRightsSubject{Kind: reason.Subject.Kind, Ref: reason.Subject.Ref},
		})
	}
	removals := make([]effectiveRightsRemoval, 0, len(explanation.RemovedBy))
	for _, reason := range explanation.RemovedBy {
		removal := effectiveRightsRemoval{Kind: reason.Kind, ID: reason.ID}
		if reason.Node != (auth.DecisionNode{}) {
			removal.Node = &effectiveRightsStep{Kind: reason.Node.Kind, Ref: reason.Node.Ref}
		}
		removals = append(removals, removal)
	}
	return grants, removals
}

type effectiveRightsDirectory struct {
	groups map[model.ID]model.UserGroup
	cache  map[model.ID]*model.UserGroup
	direct []model.ID
	asOf   time.Time
}

// explainEffectiveRightsDirectory adds group chains to determining grants and
// returns the oldest timestamp of the directory rows used by the principal. A
// concurrent membership change is an error, not a chain for a different decision.
func (s *Server) explainEffectiveRightsDirectory(ctx context.Context, subject auth.Principal, tenant model.TenantID, rights []effectiveRight) (string, error) {
	if len(subject.GroupsIn(tenant)) == 0 {
		return "", nil
	}
	directory := &effectiveRightsDirectory{}
	if err := s.st.AuthView(ctx, func(as store.AuthScope) error {
		return directory.load(ctx, as, subject, tenant)
	}); err != nil {
		return "", err
	}
	for i := range rights {
		for j := range rights[i].Because {
			matched := &rights[i].Because[j].Subject
			if matched.Kind != "group" {
				continue
			}
			chain, ok := directory.chain(subject.UserID, model.ID(matched.Ref))
			if !ok {
				return "", fmt.Errorf("effective-rights: directory group chain unavailable")
			}
			matched.Chain = chain
		}
	}
	if directory.asOf.IsZero() {
		return "", nil
	}
	return directory.asOf.UTC().Format(time.RFC3339Nano), nil
}

func (d *effectiveRightsDirectory) load(ctx context.Context, as store.AuthScope, subject auth.Principal, tenant model.TenantID) error {
	d.groups = map[model.ID]model.UserGroup{}
	d.cache = map[model.ID]*model.UserGroup{}
	var memberships []model.UserGroupMember
	membersQuery := model.Query{Filters: []model.Filter{{Column: "user_id", Op: model.OpEq, Value: subject.UserID.String()}}, Limit: 1000}
	if err := store.WalkPages(ctx, as.GroupMembers().List, membersQuery, 100000, func(rows []model.UserGroupMember) error {
		memberships = append(memberships, rows...)
		return nil
	}); err != nil {
		return err
	}
	directSeen := map[model.ID]bool{}
	resolved := map[string]bool{}
	for _, membership := range memberships {
		group, err := d.group(ctx, as, membership.GroupID, tenant)
		if err != nil {
			return err
		}
		if group == nil {
			continue
		}
		if !directSeen[membership.GroupID] {
			directSeen[membership.GroupID] = true
			d.direct = append(d.direct, membership.GroupID)
		}
		for id, seen := membership.GroupID, map[model.ID]bool{}; !seen[id]; {
			seen[id] = true
			group, err := d.group(ctx, as, id, tenant)
			if err != nil {
				return err
			}
			if group == nil {
				break
			}
			resolved[id.String()] = true
			if group.ParentGroupID.IsZero() {
				break
			}
			id = group.ParentGroupID
		}
	}
	expected := map[string]bool{}
	for _, id := range subject.GroupsIn(tenant) {
		expected[id] = true
	}
	if len(resolved) != len(expected) {
		return fmt.Errorf("effective-rights: directory membership changed while explaining")
	}
	for id := range expected {
		if !resolved[id] {
			return fmt.Errorf("effective-rights: directory membership changed while explaining")
		}
	}
	slices.Sort(d.direct)

	complete := true
	oldest := time.Time{}
	note := func(stamp model.Timestamp) {
		at := stamp.Time()
		if at.IsZero() {
			complete = false
			return
		}
		if oldest.IsZero() || at.Before(oldest) {
			oldest = at
		}
	}
	for _, membership := range memberships {
		if expected[membership.GroupID.String()] {
			note(membership.UpdatedAt)
		}
	}
	for id := range expected {
		group, ok := d.groups[model.ID(id)]
		if !ok {
			return fmt.Errorf("effective-rights: directory group data unavailable")
		}
		note(group.UpdatedAt)
	}
	if complete {
		d.asOf = oldest
	}
	return nil
}

// group reads one directory group through the per-subject cache and refuses a
// missing or cross-tenant parent edge, matching the principal closure rules.
func (d *effectiveRightsDirectory) group(ctx context.Context, as store.AuthScope, id model.ID, tenant model.TenantID) (*model.UserGroup, error) {
	if group, cached := d.cache[id]; cached {
		return group, nil
	}
	group, err := as.Groups().Get(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		d.cache[id] = nil
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if group.TargetTenantID != tenant {
		d.cache[id] = nil
		return nil, nil
	}
	d.cache[id] = &group
	d.groups[id] = group
	return &group, nil
}

// chain returns the shortest user-to-target group path, with stable id order
// breaking ties when more than one direct group reaches the same ancestor.
func (d *effectiveRightsDirectory) chain(user model.ID, target model.ID) ([]effectiveRightsStep, bool) {
	var best []model.ID
	bestKey := ""
	for _, direct := range d.direct {
		var candidate []model.ID
		seen := map[model.ID]bool{}
		for id := direct; !seen[id]; {
			seen[id] = true
			candidate = append(candidate, id)
			if id == target {
				key := strings.Join(idsToStrings(candidate), "\x00")
				if best == nil || len(candidate) < len(best) || (len(candidate) == len(best) && key < bestKey) {
					best, bestKey = candidate, key
				}
				break
			}
			group, ok := d.groups[id]
			if !ok || group.ParentGroupID.IsZero() {
				break
			}
			id = group.ParentGroupID
		}
	}
	if best == nil {
		return nil, false
	}
	chain := make([]effectiveRightsStep, 0, len(best)+1)
	chain = append(chain, effectiveRightsStep{Kind: "user", Ref: user.String()})
	for _, id := range best {
		chain = append(chain, effectiveRightsStep{Kind: "group", Ref: id.String()})
	}
	return chain, true
}

func idsToStrings(ids []model.ID) []string {
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = id.String()
	}
	return out
}
