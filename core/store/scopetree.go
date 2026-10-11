// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package store

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/olivaresai/olivares/core/model"
)

// The scope tree is workspace → agent group → agent → session, and workspace →
// folder → resource, and a workspace may sit under a parent workspace (a department
// under a department). Ancestors reads what contains a node of it from the node's
// STORED row (its workspace and the workspaces above it, its folder path, its agent's
// memberships), never from the caller, so the lineage cannot be forged. A caller turns
// the answer into its own form: the Cedar scope resolver (modules/governance grants.go)
// into entity parents. Contains asks the other question, whether one node of the tree
// contains another, for the delegation ceiling (modules/governance scopedadmin.go), and
// the sourcescope actor scope (modules/sourcescope resolver.go) reads the same lineage
// for its allow and forbid matches.

// Ancestry is what contains one node of the scope tree.
type Ancestry struct {
	// Workspace is the slug of the workspace the node lives in (see WorkspaceSlug).
	Workspace string
	// WorkspaceAncestors are the slugs of the workspaces above Workspace in the
	// organization tree, root first; empty for a root, the default workspace and a
	// workspace with no row.
	WorkspaceAncestors []string
	// Groups are the agent groups that contain the node: an agent's groups, a session's
	// agent's groups when asked for (AncestryOptions), or an agent group itself. A
	// membership whose group row is gone is skipped.
	Groups []AncestorGroup
	// AgentGroups is, for a session when AncestryOptions.AllAgentGroups asks, every group
	// of the session's agent in any workspace: what a forbid matches, where Groups is what
	// an allow matches. Empty otherwise; an agent's Groups already are all of its groups.
	AgentGroups []AncestorGroup
	// Folders are a resource's proper folder ancestors from its materialized path, root
	// first. A legacy resource with an empty path has none.
	Folders []model.ID
	// Session and Resource are the stored row when the node is a session or a resource,
	// for callers that also read the row's attributes.
	Session  model.Session
	Resource model.Resource
}

// AncestorGroup is one agent group in an Ancestry.
type AncestorGroup struct {
	// Slug is the group's tenant-unique slug.
	Slug string
	// Workspace is the slug of the group's OWN workspace (see WorkspaceSlug), which may
	// differ from its members' workspaces.
	Workspace string
	// WorkspaceAncestors are the slugs of the workspaces above Workspace, root first.
	WorkspaceAncestors []string
}

// AncestryOptions selects the opt-in parts of an Ancestry.
type AncestryOptions struct {
	// SessionAgentGroups adds, for a session, the groups of its owning agent that live
	// in the session's own workspace. A group of another workspace is never added: the
	// membership API checks only that both ids exist, and inheriting such a group was
	// measured as a cross-workspace allow (modules/governance grants.go readScope).
	SessionAgentGroups bool
	// AllAgentGroups adds, for a session, every group of its owning agent, whatever the
	// group's workspace, as Ancestry.AgentGroups. A forbid is absolute: it still matches a
	// group the session does not inherit from (sourcescope resolveActorScope). The groups
	// are read through the session's agent id, never through the agent row.
	AllAgentGroups bool
}

// Ancestors reads what contains the node of kind ("agent", "session", "resource" or
// "agent_group") with id, from its stored row. found is false when id is zero, kind is
// none of those, or no row has id; the caller then has no stored lineage to trust. A
// store error is returned so the caller fails closed.
func Ancestors(ctx context.Context, sc Scope, kind string, id model.ID, opts AncestryOptions) (Ancestry, bool, error) {
	if id.IsZero() {
		return Ancestry{}, false, nil
	}
	switch kind {
	case "agent":
		a, err := sc.Agents().Get(ctx, id)
		if errors.Is(err, ErrNotFound) {
			return Ancestry{}, false, nil
		}
		if err != nil {
			return Ancestry{}, false, err
		}
		ws, above, err := WorkspaceLineage(ctx, sc, a.WorkspaceID)
		if err != nil {
			return Ancestry{}, false, err
		}
		rows, err := agentGroupRows(ctx, sc, id)
		if err != nil {
			return Ancestry{}, false, err
		}
		_, groups, err := ancestorGroups(ctx, sc, rows, model.ID(""), false, true)
		if err != nil {
			return Ancestry{}, false, err
		}
		return Ancestry{Workspace: ws, WorkspaceAncestors: above, Groups: groups}, true, nil
	case "session":
		s, err := sc.Sessions().Get(ctx, id)
		if errors.Is(err, ErrNotFound) {
			return Ancestry{}, false, nil
		}
		if err != nil {
			return Ancestry{}, false, err
		}
		ws, above, err := WorkspaceLineage(ctx, sc, s.WorkspaceID)
		if err != nil {
			return Ancestry{}, false, err
		}
		anc := Ancestry{Workspace: ws, WorkspaceAncestors: above, Session: s}
		if (opts.SessionAgentGroups || opts.AllAgentGroups) && !s.AgentID.IsZero() {
			rows, err := agentGroupRows(ctx, sc, s.AgentID)
			if err != nil {
				return Ancestry{}, false, err
			}
			anc.Groups, anc.AgentGroups, err = ancestorGroups(ctx, sc, rows, s.WorkspaceID, opts.SessionAgentGroups, opts.AllAgentGroups)
			if err != nil {
				return Ancestry{}, false, err
			}
		}
		return anc, true, nil
	case "resource":
		res, err := sc.Resources().Get(ctx, id)
		if errors.Is(err, ErrNotFound) {
			return Ancestry{}, false, nil
		}
		if err != nil {
			return Ancestry{}, false, err
		}
		ws, above, err := WorkspaceLineage(ctx, sc, res.WorkspaceID)
		if err != nil {
			return Ancestry{}, false, err
		}
		return Ancestry{Workspace: ws, WorkspaceAncestors: above, Folders: folderAncestors(res.Path, id), Resource: res}, true, nil
	case "agent_group":
		g, err := sc.AgentGroups().Get(ctx, id)
		if errors.Is(err, ErrNotFound) {
			return Ancestry{}, false, nil
		}
		if err != nil {
			return Ancestry{}, false, err
		}
		ws, above, err := WorkspaceLineage(ctx, sc, g.WorkspaceID)
		if err != nil {
			return Ancestry{}, false, err
		}
		// The group is among its own containers, so a grant on it reaches an action on it
		// and a confined operator is bound by the group's TRUE workspace (F3).
		return Ancestry{Workspace: ws, WorkspaceAncestors: above,
			Groups: []AncestorGroup{{Slug: g.Slug, Workspace: ws, WorkspaceAncestors: above}}}, true, nil
	default:
		return Ancestry{}, false, nil
	}
}

// WorkspaceSlug resolves a workspace id to its slug: zero is the reserved default slug,
// with no store read (the NULL→default resolution). A dangling id (no such
// workspace) resolves to the raw id as a synthetic slug, so it can never match a
// human-authored workspace slug (deny-closed). A real store error is returned.
func WorkspaceSlug(ctx context.Context, sc Scope, wsID model.ID) (string, error) {
	if wsID.IsZero() {
		return model.DefaultWorkspaceSlug, nil
	}
	ws, err := sc.Workspaces().Get(ctx, wsID)
	switch {
	case errors.Is(err, ErrNotFound):
		return wsID.String(), nil // synthetic: matches no authored slug (deny-closed)
	case err != nil:
		return "", err
	default:
		return ws.Slug, nil
	}
}

// WorkspaceLineage resolves a workspace id to its slug (as WorkspaceSlug does) and the
// slugs of the workspaces above it in the organization tree, root first. The rows are
// read through sc, so a scope that observes a lineage generation before each read
// observes the workspace generation before the first row. A workspace-confined scope
// hides tree positions and reports a root, so callers pass the tenant scope.
//
// A node whose own workspace has no row keeps its synthetic slug and no ancestors. A tree
// the store would never write is an error: nothing ties parent_id or path to a row, and an
// ancestor skipped would read as a shorter lineage, the same as a root.
func WorkspaceLineage(ctx context.Context, sc Scope, wsID model.ID) (string, []string, error) {
	if wsID.IsZero() {
		return model.DefaultWorkspaceSlug, nil, nil
	}
	ws, err := sc.Workspaces().Get(ctx, wsID)
	switch {
	case errors.Is(err, ErrNotFound):
		return wsID.String(), nil, nil // synthetic: matches no authored slug (deny-closed)
	case err != nil:
		return "", nil, err
	}
	above, err := workspaceAncestors(ctx, sc, ws)
	if err != nil {
		return "", nil, err
	}
	return ws.Slug, above, nil
}

// workspaceAncestors walks ws's parents to the root. Each step must satisfy the invariant
// the workspace repository keeps, path = parent.path + "/" + id, where a root with no path
// (the default workspace, or a row written before core v25) stands for "/" + id. Each
// accepted step shortens the path, so the walk ends.
func workspaceAncestors(ctx context.Context, sc Scope, ws model.Workspace) ([]string, error) {
	var above []string
	for cur := ws; ; {
		if cur.ParentID.IsZero() {
			if cur.Path != "" && cur.Path != "/"+cur.ID.String() {
				return nil, workspaceTreeBroken(cur, "a root whose path is not its own id")
			}
			break
		}
		parent, err := sc.Workspaces().Get(ctx, cur.ParentID)
		if errors.Is(err, ErrNotFound) {
			return nil, workspaceTreeBroken(cur, "its parent has no row")
		}
		if err != nil {
			return nil, err
		}
		if cur.Path != placedPath(parent)+"/"+cur.ID.String() {
			return nil, workspaceTreeBroken(cur, "its path is not its parent's path and its id")
		}
		above = append(above, parent.Slug)
		cur = parent
	}
	slices.Reverse(above)
	return above, nil
}

// placedPath is a workspace's materialized path; a root the store has not placed yet
// stands for "/" + id.
func placedPath(ws model.Workspace) string {
	if ws.Path == "" {
		return "/" + ws.ID.String()
	}
	return ws.Path
}

func workspaceTreeBroken(ws model.Workspace, why string) error {
	return fmt.Errorf("%w: workspace %s: %s", ErrLineageUnavailable, ws.ID, why)
}

// agentGroupRows reads the agent groups an agent belongs to, in membership order. Groups
// are listed once and indexed by id; an orphan membership (group row gone) is skipped.
func agentGroupRows(ctx context.Context, sc Scope, agentID model.ID) ([]model.AgentGroup, error) {
	members, err := listAll(ctx, sc.AgentGroupMembers().List, model.Query{
		Filters: []model.Filter{{Column: "agent_id", Op: model.OpEq, Value: agentID.String()}}, Limit: maxListPage,
	})
	if err != nil {
		return nil, err
	}
	if len(members) == 0 {
		return nil, nil
	}
	groups, err := listAll(ctx, sc.AgentGroups().List, model.Query{Limit: maxListPage})
	if err != nil {
		return nil, err
	}
	byID := make(map[model.ID]model.AgentGroup, len(groups))
	for _, g := range groups {
		byID[g.ID] = g
	}
	var out []model.AgentGroup
	for _, m := range members {
		if g, ok := byID[m.GroupID]; ok {
			out = append(out, g)
		}
	}
	return out, nil
}

// ancestorGroups turns group rows into AncestorGroups with their own workspace slug (the
// "fold by agent_id"), reading each slug once. inWorkspace keeps the groups whose
// workspace id is ws (zero is the default workspace and matches groups with a zero
// workspace, the same resolution WorkspaceSlug uses); every keeps all of them. A group
// that neither answer wants is not read.
func ancestorGroups(ctx context.Context, sc Scope, rows []model.AgentGroup, ws model.ID, inWorkspace, every bool) (in, all []AncestorGroup, err error) {
	for _, g := range rows {
		here := inWorkspace && g.WorkspaceID == ws
		if !here && !every {
			continue
		}
		slug, above, err := WorkspaceLineage(ctx, sc, g.WorkspaceID)
		if err != nil {
			return nil, nil, err
		}
		ag := AncestorGroup{Slug: g.Slug, Workspace: slug, WorkspaceAncestors: above}
		if here {
			in = append(in, ag)
		}
		if every {
			all = append(all, ag)
		}
	}
	return in, all, nil
}

// ScopeNode names one node of the scope tree by kind and reference: "workspace" and
// "agent_group" by slug, "folder" by the folder resource's id. Any other kind is no node
// of the tree.
type ScopeNode struct {
	Kind string
	Ref  string
}

// Contains reports whether outer contains inner along the scope tree: a workspace
// contains itself and the agent groups whose workspace it is; an agent group contains
// only itself; a folder contains itself and its descendant folders. A workspace never
// contains a folder, and a narrower node never contains a broader one (no upward
// escalation). A workspace or agent group with no row is contained only by an equal ref;
// a blank folder ref contains nothing, and a folder with no row or no path contains nothing
// but itself (deny-closed). A store error is returned, not answered as "not contained".
func Contains(ctx context.Context, sc Scope, outer, inner ScopeNode) (bool, error) {
	switch outer.Kind {
	case nodeWorkspace:
		switch inner.Kind {
		case nodeWorkspace:
			return outer.Ref == inner.Ref, nil
		case nodeAgentGroup:
			ws, err := agentGroupWorkspaceSlug(ctx, sc, inner.Ref)
			if err != nil {
				return false, err
			}
			return ws != "" && ws == outer.Ref, nil
		default:
			// A workspace admin deliberately CANNOT sub-delegate a folder grant (review):
			// a Resource's workspace_id is decoupled from its tree position (the store lets a
			// child carry a different workspace than its parent), so a folder anchored in
			// workspace W can enclose descendants in OTHER workspaces, and the folder permit
			// (`resource in Resource::"<id>"`) carries no workspace predicate. Allowing a
			// W-scoped admin to delegate such a grant would let it reach resources outside W
			// (a cross-workspace confinement escape). Only a tenant admin (tenant ⊇ folder) or a
			// folder admin (folder ⊇ descendant, tree-based authority) may delegate folders.
			return false, nil
		}
	case nodeAgentGroup:
		return inner.Kind == nodeAgentGroup && outer.Ref == inner.Ref, nil
	case nodeFolder:
		// A folder admin may sub-delegate only WITHIN its subtree: the inner grant must also
		// be a folder equal to or a descendant of the outer folder. It can never reach up to a
		// workspace/agent-group/tenant scope (no upward escalation).
		if inner.Kind != nodeFolder {
			return false, nil
		}
		return folderContains(ctx, sc, outer.Ref, inner.Ref)
	default:
		return false, nil
	}
}

// The ScopeNode kinds.
const (
	nodeWorkspace  = "workspace"
	nodeAgentGroup = "agent_group"
	nodeFolder     = "folder"
)

// folderContains reports whether folder outerID contains folder innerID along the resource
// tree: inner is outer itself, or a descendant of it by the materialized Path
// ("/<root>/…/<self>"). It compares the anchors' LIVE Paths (outer's Path a proper,
// segment-boundary prefix of inner's), so a move is reflected immediately. A missing folder
// on either side, or an empty Path, is not-contained (deny-closed — no delegation across a
// dangling anchor).
func folderContains(ctx context.Context, sc Scope, outerID, innerID string) (bool, error) {
	if outerID == "" || innerID == "" {
		return false, nil
	}
	if outerID == innerID {
		return true, nil // the same folder is trivially within its own subtree
	}
	outer, ok, err := findResourceByID(ctx, sc, outerID)
	if err != nil || !ok {
		return false, err
	}
	inner, ok, err := findResourceByID(ctx, sc, innerID)
	if err != nil || !ok {
		return false, err
	}
	if outer.Path == "" || inner.Path == "" {
		return false, nil
	}
	// The trailing "/" makes the prefix test respect segment boundaries, so folder "/a/b"
	// contains "/a/b/c" but never a sibling "/a/bc".
	return strings.HasPrefix(inner.Path, outer.Path+"/"), nil
}

// findResourceByID returns the tenant's Resource (a folder-scope anchor) by id, and whether
// it exists. A blank id or a missing row is (zero, false) — deny-closed for containment (a
// folder grant never rides a dangling anchor).
func findResourceByID(ctx context.Context, sc Scope, id string) (model.Resource, bool, error) {
	if strings.TrimSpace(id) == "" {
		return model.Resource{}, false, nil
	}
	res, err := sc.Resources().Get(ctx, model.ID(id))
	if errors.Is(err, ErrNotFound) {
		return model.Resource{}, false, nil
	}
	if err != nil {
		return model.Resource{}, false, err
	}
	return res, true, nil
}

// agentGroupWorkspaceSlug resolves an agent-group slug to its workspace slug (the
// reserved default slug when the group is workspace-unscoped). An unknown group yields
// "" (matches no workspace — deny-closed for containment).
func agentGroupWorkspaceSlug(ctx context.Context, sc Scope, groupSlug string) (string, error) {
	gs, _, err := sc.AgentGroups().List(ctx, model.Query{
		Filters: []model.Filter{{Column: "slug", Op: model.OpEq, Value: groupSlug}}, Limit: 1,
	})
	if err != nil || len(gs) == 0 {
		return "", err
	}
	g := gs[0]
	if g.WorkspaceID.IsZero() {
		return model.DefaultWorkspaceSlug, nil
	}
	ws, err := sc.Workspaces().Get(ctx, g.WorkspaceID)
	if errors.Is(err, ErrNotFound) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return ws.Slug, nil
}

// maxListPage is the page size of a lineage listing; it matches the store's own maximum.
const maxListPage = 1000

// listAll reads every page of a listing, so a lineage is never silently truncated by
// pagination.
func listAll[T any](ctx context.Context, list func(context.Context, model.Query) ([]T, model.Page, error), q model.Query) ([]T, error) {
	var out []T
	if err := WalkPages(ctx, list, q, UnboundedRows, func(rows []T) error {
		out = append(out, rows...)
		return nil
	}); err != nil {
		return nil, err
	}
	return out, nil
}

// folderAncestors parses a materialized path "/<root>/…/<self>" into the folder ids
// above self, root first (empty for a NULL/empty legacy path). self is dropped: it is
// not its own ancestor.
func folderAncestors(path string, self model.ID) []model.ID {
	var ids []model.ID
	for _, p := range strings.Split(path, "/") {
		if p != "" {
			ids = append(ids, model.ID(p))
		}
	}
	if n := len(ids); n > 0 && ids[n-1] == self {
		ids = ids[:n-1]
	}
	return ids
}
