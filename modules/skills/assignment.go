// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package skills

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

const AssignmentKind model.Kind = "skills.assignment"

// Target identifies a stored native target, never a vendor conversation ID.
type Target struct {
	Kind string `json:"target_kind"`
	ID   string `json:"target_id"`
}
type StoredTarget struct {
	Target
	Version     int64
	WorkspaceID model.ID
}

// TargetAuthority is supplied by the native session owner. It must read the
// actual tenant-pinned target and authorize its native read/write permission.
// On writes it must fence that row and the authorization facts in sc until
// commit. A missing adapter refuses; a caller's fields are never authority.
type TargetAuthority interface {
	ResolveSkillsTarget(context.Context, store.Scope, auth.Principal, Target, bool) (StoredTarget, error)
}

// AssignmentAuthority prepares native authorization outside the store transaction,
// then corroborates the exact target again inside it. Its catalog door returns
// only the requested revision and fences its enabled pack in that same transaction;
// it never hands a shared catalog repository to a confined handler.
type AssignmentAuthority interface {
	AssignmentTargetRefs() map[string]api.EntityRef
	PrepareSkillsTarget(context.Context, api.ModuleContext, Target, bool) (context.Context, context.CancelFunc, error)
	MutateSkillsAssignment(context.Context, api.ModuleContext, string, func(store.Scope, AssignmentRevisionReader) error) error
}

type AssignmentRevisionReader func(context.Context, string) (Revision, error)

func (m *Module) prepareTarget(r *http.Request, mc api.ModuleContext, target Target, write bool) (*http.Request, context.CancelFunc, error) {
	if authority, ok := m.opts.Targets.(AssignmentAuthority); ok {
		ctx, release, err := authority.PrepareSkillsTarget(r.Context(), mc, target, write)
		return r.WithContext(ctx), release, err
	}
	return r, func() {}, nil
}

func (m *Module) mutateAssignment(ctx context.Context, mc api.ModuleContext, revisionID string, fn func(store.Scope, AssignmentRevisionReader) error) error {
	if authority, ok := m.opts.Targets.(AssignmentAuthority); ok {
		return authority.MutateSkillsAssignment(ctx, mc, revisionID, fn)
	}
	return mc.Data.Mutate(ctx, func(sc store.Scope) error {
		return fn(sc, func(ctx context.Context, id string) (Revision, error) {
			return FenceAssignmentRevision(ctx, sc, id)
		})
	})
}

type Assignment struct {
	ID string `json:"id"`
	Target
	PackID        string   `json:"pack_id"`
	RevisionID    string   `json:"pack_revision_id"`
	Members       []string `json:"members"`
	Version       int64    `json:"version"`
	TargetVersion int64    `json:"target_version"`
	CreatedBy     string   `json:"created_by"`
}
type AssignmentRequest struct {
	Target
	RevisionID string   `json:"pack_revision_id"`
	Members    []string `json:"members,omitempty"`
}
type AssignmentResult struct {
	State      string     `json:"state"`
	Assignment Assignment `json:"assignment"`
}
type SelectionOrigin struct {
	Target
	TargetVersion     int64  `json:"target_version"`
	AssignmentID      string `json:"assignment_id"`
	AssignmentVersion int64  `json:"assignment_version"`
}
type SelectedMember struct {
	PackID         string            `json:"pack_id"`
	RevisionID     string            `json:"pack_revision_id"`
	ManifestDigest string            `json:"manifest_digest"`
	Name           string            `json:"name"`
	Directory      string            `json:"directory"`
	SkillDigest    string            `json:"skill_digest"`
	ContentDigest  string            `json:"content_digest"`
	Origins        []SelectionOrigin `json:"origins"`
}

// Selection is the serializable immutable run/intent snapshot. The runtime
// owner persists it with admission before spawning and retains it on resume.
// Digest includes stored assignment and target versions, not just file hashes.
// No path, native discovery claim or client-asserted authority is included.
type Selection struct {
	Digest  string           `json:"digest"`
	Members []SelectedMember `json:"members"`
}

// Target kinds in tree order: a workspace is the department until departments
// have their own node, then the groups the agent belongs to, then the agent.
const (
	targetWorkspace  = "workspace"
	targetTemplate   = "template"
	targetAgentGroup = "agent_group"
	targetAgent      = "agent"
	targetSession    = "session"
)

var targetRank = map[string]int{targetWorkspace: 0, targetTemplate: 1, targetAgentGroup: 2, targetAgent: 3, targetSession: 4}

// goneAfterDelete reports whether kind is one the core deletes outright, unlike
// a workspace, a template or a session, which are retired and keep their row.
func goneAfterDelete(kind string) bool { return kind == targetAgentGroup || kind == targetAgent }

func validTarget(t Target) bool {
	_, known := targetRank[t.Kind]
	return known && canonicalID(t.ID)
}
func canonicalID(value string) bool {
	id, err := model.ParseID(value)
	return err == nil && !id.IsZero() && id.String() == value
}
func (m *Module) target(ctx context.Context, sc store.Scope, p auth.Principal, t Target, write bool) (StoredTarget, error) {
	if !validTarget(t) {
		return StoredTarget{}, errInvalidRequest
	}
	if m.opts.Targets == nil {
		return StoredTarget{}, refuse("target_authority_unavailable", "native target authority not connected")
	}
	stored, err := m.opts.Targets.ResolveSkillsTarget(ctx, sc, p, t, write)
	if err != nil {
		return StoredTarget{}, err
	}
	if stored.Target != t || stored.Version < 1 {
		return StoredTarget{}, store.ErrStoreUnavailable
	}
	return stored, nil
}
func assignmentDTO(row model.Record) (Assignment, error) {
	a := Assignment{ID: row.String(model.ColID), Target: Target{Kind: row.String("target_kind"), ID: row.String("target_id")}, PackID: row.String("pack_id"), RevisionID: row.String("revision_id"), Version: row.Int(model.ColVersion), TargetVersion: row.Int("target_version"), CreatedBy: row.String("created_by")}
	if err := json.Unmarshal([]byte(row.String("members")), &a.Members); err != nil {
		return a, store.ErrStoreUnavailable
	}
	return a, nil
}
func pinnedRevision(ctx context.Context, sc store.Scope, id string) (Revision, error) {
	revisions, err := sc.Ext(RevisionKind)
	if err != nil {
		return Revision{}, err
	}
	row, err := revisions.Get(ctx, model.ID(id))
	if err != nil {
		return Revision{}, err
	}
	rev, err := revisionDTO(row)
	if err != nil {
		return Revision{}, err
	}
	packs, err := sc.Ext(PackKind)
	if err != nil {
		return Revision{}, err
	}
	pack, err := packs.Get(ctx, model.ID(rev.PackID))
	if err != nil {
		return Revision{}, err
	}
	if pack.String("state") != "enabled" {
		return Revision{}, store.ErrConflict
	}
	return rev, nil
}

// ReadAssignmentRevision returns only one immutable revision and checks its pack
// is enabled. The composition root authorizes that stored pack before pinning.
func ReadAssignmentRevision(ctx context.Context, sc store.Scope, id string) (Revision, error) {
	return pinnedRevision(ctx, sc, id)
}

// FenceAssignmentRevision reads an immutable revision and OCC-touches its enabled
// pack to serialize pinning with retirement. Only the composition root may supply
// an unconfined scope; assignments continue to use their own confined scope.
func FenceAssignmentRevision(ctx context.Context, sc store.Scope, id string) (Revision, error) {
	rev, err := pinnedRevision(ctx, sc, id)
	if err != nil {
		return Revision{}, err
	}
	packs, err := sc.Ext(PackKind)
	if err != nil {
		return Revision{}, err
	}
	pack, err := packs.Get(ctx, model.ID(rev.PackID))
	if err != nil {
		return Revision{}, err
	}
	if pack.String("state") != "enabled" {
		return Revision{}, store.ErrConflict
	}
	if _, err := packs.Update(ctx, pack); err != nil {
		return Revision{}, err
	}
	return rev, nil
}
func selectedNames(rev Revision, requested []string) ([]string, error) {
	available := map[string]bool{}
	for _, member := range rev.Members {
		available[member.Name] = true
	}
	if len(requested) == 0 {
		for name := range available {
			requested = append(requested, name)
		}
	}
	if len(requested) == 0 || len(requested) > MaxSelectedMembers {
		return nil, refuse("import_limit", "selected member count")
	}
	seen := map[string]bool{}
	for _, name := range requested {
		if !available[name] || seen[name] {
			return nil, errInvalidRequest
		}
		seen[name] = true
	}
	out := append([]string(nil), requested...)
	sort.Strings(out)
	return out, nil
}
func decodeAssignment(w http.ResponseWriter, r *http.Request) (AssignmentRequest, error) {
	var body AssignmentRequest
	if err := api.DecodeRequestBody(w, r, &body, api.RequestBodySpec{MaxBytes: 16 << 10}); err != nil {
		return body, errors.Join(errInvalidRequest, err)
	}
	if !validTarget(body.Target) || !canonicalID(body.RevisionID) {
		return body, errInvalidRequest
	}
	return body, nil
}
func expectedVersion(r *http.Request) (int64, error) {
	v := r.Header.Get("If-Match")
	if strings.HasPrefix(v, "\"") && strings.HasSuffix(v, "\"") {
		v = strings.Trim(v, "\"")
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n < 1 || strconv.FormatInt(n, 10) != v {
		return 0, errInvalidRequest
	}
	return n, nil
}

// saveAssignment creates or updates a target's pin to an immutable skills
// revision and selected members for new conversations.
// Updates require the expected assignment version and preserve the target.
func (m *Module) saveAssignment(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	// The transaction cannot outlive the finite authority retained by preflight.
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	r = r.WithContext(ctx)
	body, err := decodeAssignment(w, r)
	if err != nil {
		respondError(w, err)
		return
	}
	id := model.ID(chi.URLParam(r, "id"))
	version := int64(0)
	if !id.IsZero() {
		version, err = expectedVersion(r)
		if err != nil {
			respondError(w, err)
			return
		}
	}
	var result Assignment
	r, release, err := m.prepareTarget(r, mc, body.Target, true)
	defer release()
	if err != nil {
		respondError(w, err)
		return
	}
	err = m.mutateAssignment(r.Context(), mc, body.RevisionID, func(sc store.Scope, revision AssignmentRevisionReader) error {
		target, err := m.target(r.Context(), sc, mc.Principal, body.Target, true)
		if err != nil {
			return err
		}
		rev, err := revision(r.Context(), body.RevisionID)
		if err != nil {
			return err
		}
		members, err := selectedNames(rev, body.Members)
		if err != nil {
			return err
		}
		encoded, _ := json.Marshal(members)
		repo, err := sc.Ext(AssignmentKind)
		if err != nil {
			return err
		}
		row := model.Record{"target_kind": target.Kind, "target_id": target.ID, "target_version": target.Version, "pack_id": rev.PackID, "revision_id": rev.ID, "members": string(encoded), "created_by": mc.Principal.Actor()}
		if !target.WorkspaceID.IsZero() {
			row["workspace_id"] = target.WorkspaceID.String()
		}
		if id.IsZero() {
			row, err = repo.Create(r.Context(), row)
		} else {
			old, e := repo.Get(r.Context(), id)
			if e != nil {
				return e
			}
			// An update changes a pin/member set, never relocates its authority.
			if old.Int(model.ColVersion) != version || old.String("target_kind") != body.Kind || old.String("target_id") != body.ID || old.String("pack_id") != rev.PackID || old.String("workspace_id") != target.WorkspaceID.String() && !(old.String("workspace_id") == "" && target.WorkspaceID.IsZero()) {
				return store.ErrConflict
			}
			old["revision_id"], old["members"], old["target_version"] = rev.ID, string(encoded), target.Version
			row, err = repo.Update(r.Context(), old)
		}
		if err != nil {
			return err
		}
		result, err = assignmentDTO(row)
		if err != nil {
			return err
		}
		return catalogAudit(r.Context(), sc, mc, "skills.assignment.pin", AssignmentKind, model.ID(result.ID), map[string]any{"target_kind": target.Kind, "target_id": target.ID, "target_version": target.Version, "revision_id": rev.ID, "assignment_version": result.Version, "manifest_digest": rev.ManifestDigest})
	})
	if err != nil {
		respondError(w, err)
		return
	}
	// The pin is committed: every selected member is now loaded for the target's
	// next conversations — the GenAI load_skill moment Olivares can observe (the
	// agent's own in-context load happens inside the tool). A session target
	// names its conversation (the run reference); workspace and template targets
	// serve future conversations and carry none. Telemetry only, never fails.
	if m.opts.Tracer != nil {
		conversation := ""
		if result.Target.Kind == "session" {
			conversation = result.Target.ID
		}
		for _, member := range result.Members {
			m.opts.Tracer.LoadSkill(conversation, member).End()
		}
	}
	status := http.StatusCreated
	if version > 0 {
		status = http.StatusOK
	}
	respond(w, status, AssignmentResult{State: "assignment_effective_for_new_conversations", Assignment: result})
}

// deleteAssignment removes a skills assignment from its authorized target at
// the expected version so new conversations no longer inherit that pin.
func (m *Module) deleteAssignment(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	// The transaction cannot outlive the finite authority retained by preflight.
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	r = r.WithContext(ctx)
	version, err := expectedVersion(r)
	if err != nil {
		respondError(w, err)
		return
	}
	id := model.ID(chi.URLParam(r, "id"))
	var prepared Assignment
	err = mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(AssignmentKind)
		if err != nil {
			return err
		}
		row, err := repo.Get(r.Context(), id)
		if err != nil {
			return err
		}
		prepared, err = assignmentDTO(row)
		return err
	})
	if err == nil {
		var release context.CancelFunc
		r, release, err = m.prepareTarget(r, mc, prepared.Target, true)
		defer release()
		if goneAfterDelete(prepared.Kind) && errors.Is(err, store.ErrNotFound) {
			err = nil
		}
	}
	if err != nil {
		respondError(w, err)
		return
	}
	err = mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(AssignmentKind)
		if err != nil {
			return err
		}
		row, err := repo.Get(r.Context(), id)
		if err != nil {
			return err
		}
		a, err := assignmentDTO(row)
		if err != nil {
			return err
		}
		if _, err := m.target(r.Context(), sc, mc.Principal, a.Target, true); err != nil {
			// An agent group or agent can be deleted, which leaves a pin nothing
			// can authorize against and that would hold its pack in use for good.
			// The route's assignment permission and the pin's own workspace
			// lineage, read through the request's scope, still gate the unpin.
			if !goneAfterDelete(a.Kind) || !errors.Is(err, store.ErrNotFound) {
				return err
			}
		}
		if a.Version != version {
			return store.ErrConflict
		}
		// OCC-touch before Delete: GenericRepo.Delete has no version argument.
		// The successful touch locks the exact row through this transaction.
		if _, err := repo.Update(r.Context(), row); err != nil {
			return err
		}
		if err := repo.Delete(r.Context(), id); err != nil {
			return err
		}
		return catalogAudit(r.Context(), sc, mc, "skills.assignment.unpin", AssignmentKind, id, map[string]any{"target_kind": a.Kind, "target_id": a.Target.ID, "revision_id": a.RevisionID, "assignment_version": a.Version})
	})
	if err != nil {
		respondError(w, err)
		return
	}
	respond(w, http.StatusOK, map[string]string{"state": "unassigned_for_new_conversations"})
}

// listAssignments lists pinned skills assignments for an authorized workspace,
// template, agent group, agent, or session target with cursor pagination.
func (m *Module) listAssignments(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	// The transaction cannot outlive the finite authority retained by preflight.
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	r = r.WithContext(ctx)
	target := Target{Kind: r.URL.Query().Get("target_kind"), ID: r.URL.Query().Get("target_id")}
	q, err := query(r)
	if err != nil || !validTarget(target) {
		respondError(w, errInvalidRequest)
		return
	}
	q.Filters = targetFilters(target)
	out := api.ListResponse[Assignment]{}
	r, release, err := m.prepareTarget(r, mc, target, false)
	defer release()
	if err != nil {
		respondError(w, err)
		return
	}
	err = mc.Data.View(r.Context(), func(sc store.Scope) error {
		if _, err := m.target(r.Context(), sc, mc.Principal, target, false); err != nil {
			return err
		}
		repo, err := sc.Ext(AssignmentKind)
		if err != nil {
			return err
		}
		rows, page, err := repo.List(r.Context(), q)
		if err != nil {
			return err
		}
		for _, row := range rows {
			a, err := assignmentDTO(row)
			if err != nil {
				return err
			}
			out.Items = append(out.Items, a)
		}
		out.Cursor, out.HasMore = page.Cursor, page.HasMore
		return nil
	})
	if err != nil {
		respondError(w, err)
		return
	}
	respond(w, http.StatusOK, out)
}

// listPackAssignments lists the pinned skills assignments of an authorized pack
// whose targets the caller can read natively, with cursor pagination.
func (m *Module) listPackAssignments(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	// The request's scope hides pins outside a confined workspace, and a pin is
	// listed only when its target passes a target listing's own read check;
	// denied ones are skipped, never named, and the cursor is always a listed
	// pin. A pin whose agent or agent group was deleted stays listed, because
	// the same rule lets deleteAssignment unpin it.
	// ponytail: one authority check per pin within the request's 30s; batch the PDP question when packs carry thousands of hidden pins.
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	r = r.WithContext(ctx)
	q, err := query(r)
	if err != nil {
		respondError(w, err)
		return
	}
	q.Filters = []model.Filter{{Column: "pack_id", Op: model.OpEq, Value: chi.URLParam(r, "id")}}
	out := api.ListResponse[Assignment]{}
	var last model.Page
	// Each page is read in its own transaction: the target checks consult the
	// PDP, which reads the store too, so they run between pages.
	list := func(ctx context.Context, q model.Query) ([]Assignment, model.Page, error) {
		var rows []Assignment
		err := mc.Data.View(ctx, func(sc store.Scope) error {
			repo, err := sc.Ext(AssignmentKind)
			if err != nil {
				return err
			}
			records, page, err := repo.List(ctx, q)
			if err != nil {
				return err
			}
			last = page
			for _, row := range records {
				a, err := assignmentDTO(row)
				if err != nil {
					return err
				}
				rows = append(rows, a)
			}
			return nil
		})
		return rows, last, err
	}
	err = store.WalkPages(r.Context(), list, q, math.MaxInt, func(rows []Assignment) error {
		for i, a := range rows {
			readable, err := m.readableTarget(r, mc, a.Target)
			if err != nil {
				return err
			}
			if !readable {
				continue
			}
			out.Items = append(out.Items, a)
			if len(out.Items) == q.Limit {
				if out.HasMore = i < len(rows)-1 || last.HasMore; out.HasMore {
					out.Cursor = a.ID
				}
				return errPageFull
			}
		}
		return nil
	})
	if err != nil && !errors.Is(err, errPageFull) {
		respondError(w, err)
		return
	}
	respond(w, http.StatusOK, out)
}

var errPageFull = errors.New("skills page full")

// readableTarget runs a target listing's own read check on one pin's target.
func (m *Module) readableTarget(r *http.Request, mc api.ModuleContext, t Target) (bool, error) {
	r, release, err := m.prepareTarget(r, mc, t, false)
	defer release()
	if err == nil {
		err = mc.Data.View(r.Context(), func(sc store.Scope) error {
			_, err := m.target(r.Context(), sc, mc.Principal, t, false)
			return err
		})
	}
	switch {
	case err == nil, goneAfterDelete(t.Kind) && errors.Is(err, store.ErrNotFound):
		return true, nil
	case errors.Is(err, store.ErrNotFound), errors.Is(err, auth.ErrRouteDenied):
		return false, nil
	}
	return false, err
}

func targetFilters(t Target) []model.Filter {
	return []model.Filter{{Column: "target_kind", Op: model.OpEq, Value: t.Kind}, {Column: "target_id", Op: model.OpEq, Value: t.ID}}
}

// orderedSelectionTargets puts a launch's targets in tree order, groups by ID,
// so the same launch always digests the same. A launch names each target once,
// at most one of each kind but the agent groups, and a bounded group list.
func orderedSelectionTargets(targets []Target) ([]Target, error) {
	groups := 0
	seenKind := map[string]bool{}
	seen := map[Target]bool{}
	for _, t := range targets {
		if !validTarget(t) || seen[t] || t.Kind != targetAgentGroup && seenKind[t.Kind] {
			return nil, errInvalidRequest
		}
		seen[t], seenKind[t.Kind] = true, true
		if t.Kind == targetAgentGroup {
			groups++
		}
	}
	if groups > MaxSelectionGroups {
		return nil, errInvalidRequest
	}
	ordered := append([]Target(nil), targets...)
	sort.Slice(ordered, func(i, j int) bool {
		if ri, rj := targetRank[ordered[i].Kind], targetRank[ordered[j].Kind]; ri != rj {
			return ri < rj
		}
		return ordered[i].ID < ordered[j].ID
	})
	return ordered, nil
}

// ResolveSelection runs within the runtime owner's admission transaction.
// Targets must come from the actual governed launch, including inherited
// workspace/template targets and every agent group the agent belongs to, all in
// the launch's own workspace: a group of another workspace is the launch
// caller's to exclude. The native adapter fences them; never pass a
// smaller caller-supplied target list to subtract inherited instructions.
func (m *Module) ResolveSelection(ctx context.Context, sc store.Scope, p auth.Principal, targets []Target) (Selection, error) {
	return m.resolveSelection(ctx, sc, targets, func(t Target) (StoredTarget, error) {
		return m.target(ctx, sc, p, t, false)
	})
}

// ResolveStoredSelection is the trusted runtime door. The session owner supplies
// targets read from its admitted run and their stored lineage in this transaction;
// HTTP callers must use ResolveSelection with native target authorization instead.
func (m *Module) ResolveStoredSelection(ctx context.Context, sc store.Scope, targets []StoredTarget) (Selection, error) {
	return m.ExtendStoredSelection(ctx, sc, targets, Selection{})
}

// ExtendStoredSelection retains recorded pins while admitting newly assigned
// session members at the next launch. Name conflicts and limits are unchanged.
func (m *Module) ExtendStoredSelection(ctx context.Context, sc store.Scope, targets []StoredTarget, prior Selection) (Selection, error) {
	facts := make(map[Target]StoredTarget, len(targets))
	var refs []Target
	for _, target := range targets {
		if target.Version < 1 {
			return Selection{}, store.ErrConflict
		}
		facts[target.Target] = target
		refs = append(refs, target.Target)
	}
	return m.resolveSelectionWithSnapshot(ctx, sc, refs, func(t Target) (StoredTarget, error) { return facts[t], nil }, prior)
}

func (m *Module) resolveSelection(ctx context.Context, sc store.Scope, targets []Target, target func(Target) (StoredTarget, error)) (Selection, error) {
	return m.resolveSelectionWithSnapshot(ctx, sc, targets, target, Selection{})
}

func (m *Module) resolveSelectionWithSnapshot(ctx context.Context, sc store.Scope, targets []Target, target func(Target) (StoredTarget, error), prior Selection) (Selection, error) {
	ordered, err := orderedSelectionTargets(targets)
	if err != nil {
		return Selection{}, err
	}
	selected := map[string]*SelectedMember{}
	names := map[string]string{}
	if prior.Digest != "" {
		encoded, err := json.Marshal(prior.Members)
		if err != nil || prior.Digest != digest(encoded) || len(prior.Members) > MaxSelectedMembers {
			return Selection{}, refuse("source_changed", "selection")
		}
		for _, member := range prior.Members {
			selected[member.RevisionID+"\x00"+member.Name] = &member
			names[member.Name] = member.ContentDigest
		}
	}
	for _, t := range ordered {
		stored, err := target(t)
		if err != nil {
			return Selection{}, err
		}
		repo, err := sc.Ext(AssignmentKind)
		if err != nil {
			return Selection{}, err
		}
		rows, _, err := repo.List(ctx, model.Query{Limit: MaxSelectedMembers + 1, Filters: targetFilters(t)})
		if err != nil {
			return Selection{}, err
		}
		if len(rows) > MaxSelectedMembers {
			return Selection{}, refuse("import_limit", "assignment count")
		}
		sort.Slice(rows, func(i, j int) bool { return rows[i].String(model.ColID) < rows[j].String(model.ColID) })
		for _, row := range rows {
			a, err := assignmentDTO(row)
			if err != nil {
				return Selection{}, err
			}
			lineage := row.String("workspace_id")
			if lineage != stored.WorkspaceID.String() && !(lineage == "" && stored.WorkspaceID.IsZero()) {
				return Selection{}, store.ErrConflict
			}
			rev, err := pinnedRevision(ctx, sc, a.RevisionID)
			if err != nil {
				return Selection{}, err
			}
			members, err := selectedNames(rev, a.Members)
			if err != nil {
				return Selection{}, err
			}
			if len(a.Members) == 0 {
				return Selection{}, store.ErrStoreUnavailable
			}
			if a.PackID != rev.PackID {
				return Selection{}, store.ErrStoreUnavailable
			}
			for _, name := range members {
				var member Member
				for _, candidate := range rev.Members {
					if candidate.Name == name {
						member = candidate
						break
					}
				}
				key := rev.ID + "\x00" + name
				if existing, ok := names[name]; ok && existing != member.ContentDigest {
					return Selection{}, refuse("skill_name_conflict", name)
				}
				names[name] = member.ContentDigest
				if selected[key] == nil {
					selected[key] = &SelectedMember{PackID: rev.PackID, RevisionID: rev.ID, ManifestDigest: rev.ManifestDigest, Name: name, Directory: member.Directory, SkillDigest: member.SkillDigest, ContentDigest: member.ContentDigest, Origins: []SelectionOrigin{}}
				}
				origin := SelectionOrigin{Target: t, TargetVersion: stored.Version, AssignmentID: a.ID, AssignmentVersion: a.Version}
				found := false
				for _, old := range selected[key].Origins {
					if old.AssignmentID == a.ID && old.AssignmentVersion == a.Version {
						found = true
					}
				}
				if !found {
					selected[key].Origins = append(selected[key].Origins, origin)
				}
			}
		}
	}
	if len(selected) > MaxSelectedMembers {
		return Selection{}, refuse("import_limit", "selected member count")
	}
	out := Selection{Members: []SelectedMember{}}
	for _, member := range selected {
		out.Members = append(out.Members, *member)
	}
	sort.Slice(out.Members, func(i, j int) bool {
		a, b := out.Members[i], out.Members[j]
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		return a.RevisionID < b.RevisionID
	})
	encoded, err := json.Marshal(out.Members)
	if err != nil {
		return Selection{}, err
	}
	out.Digest = digest(encoded)
	return out, nil
}
