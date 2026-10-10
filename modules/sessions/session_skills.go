// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"strings"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/skills"
)

// SessionSkillsSource is the composition root's tenant-pinned catalog door. It
// resolves only a persisted admitted run, never caller-supplied target IDs.
type SessionSkillsSource interface {
	ResolveSessionSkills(context.Context, model.TenantID, string) (skills.Selection, error)
	ReadSessionSkillFiles(context.Context, model.TenantID, skills.Selection) ([]skills.DeliveryFile, error)
}

var pdeclSkillsSelection = func() *model.ColumnDecl {
	none := model.None("immutable skill pin provenance, not a principal or launch grant: session_skills.go:70, session_skills.go:316")
	text := model.Scan(model.ClassThirdPartyText)
	return model.Nested(skills.Selection{}, model.ClassEvidence,
		model.Leaf("digest", none), model.Leaf("members[].pack_id", none), model.Leaf("members[].pack_revision_id", none), model.Leaf("members[].manifest_digest", none),
		model.Leaf("members[].name", text), model.Leaf("members[].directory", text), model.Leaf("members[].skill_digest", none), model.Leaf("members[].content_digest", none),
		model.Leaf("members[].origins[].target_kind", none), model.Leaf("members[].origins[].target_id", none), model.Leaf("members[].origins[].assignment_id", none))
}()

// UseSessionSkills wires native discovery without writing into account homes or
// user repositories. A launch with no assignment keeps its existing HOME.
func (m *Module) UseSessionSkills(source SessionSkillsSource, homeRoot string) error {
	root, err := validateSessionWorkspaceRoot(homeRoot, m.dirOwnerCheck())
	if err != nil {
		return err
	}
	if source == nil {
		return errors.New("session skills source is unavailable")
	}
	m.rt.sessionSkills, m.rt.sessionSkillHomesRoot = source, root
	return nil
}

// PinSessionSkillsSelection is called by the trusted composition root in its
// transaction. Inherited pins survive resume; new session-specific pins take
// effect on the next launch. Legacy runs acquire their first
// snapshot on their next launch. Target IDs and lineage come from stored rows.
func (m *Module) PinSessionSkillsSelection(ctx context.Context, sc store.Scope, runRef string, catalog *skills.Module) (skills.Selection, error) {
	repo, err := sc.Ext(runKind)
	if err != nil {
		return skills.Selection{}, err
	}
	rec, err := findRunRec(ctx, repo, runRef)
	if err != nil {
		return skills.Selection{}, err
	}
	if err := fenceWithin(ctx, sc, rec.String(colRunClaimSID), rec.String(colClaimHolder), rec.Int(colClaimFence), m.now()); err != nil {
		return skills.Selection{}, err
	}
	var selection skills.Selection
	if saved := rec.String(colRunSkillsSelection); saved != "" {
		if err := json.Unmarshal([]byte(saved), &selection); err != nil {
			return selection, store.ErrStoreUnavailable
		}
	}
	targets := []skills.StoredTarget{{Target: skills.Target{Kind: "session", ID: runRef}, Version: rec.Int(model.ColVersion), WorkspaceID: model.ID(rec.String(colRunAuthzWorkspaceID))}}
	if selection.Digest == "" {
		targets, err = m.runSkillsTargets(ctx, sc, rec)
		if err != nil {
			return selection, err
		}
	}
	selection, err = catalog.ExtendStoredSelection(ctx, sc, targets, selection)
	if err != nil {
		return selection, err
	}
	encoded, err := json.Marshal(selection)
	if err != nil {
		return selection, err
	}
	rec[colRunSkillsSelection] = string(encoded)
	_, err = repo.Update(ctx, rec)
	return selection, err
}

// RefuseReferencedSkillsPack fences retirement against recorded conversation
// snapshots in the catalog transaction, including conversations that may resume.
func (m *Module) RefuseReferencedSkillsPack(ctx context.Context, sc store.Scope, packID model.ID) error {
	repo, err := sc.Ext(runKind)
	if err != nil {
		return err
	}
	return store.WalkPages(ctx, repo.List, model.Query{Limit: 100}, math.MaxInt, func(rows []model.Record) error {
		for _, row := range rows {
			if saved := row.String(colRunSkillsSelection); saved != "" {
				var selection skills.Selection
				if err := json.Unmarshal([]byte(saved), &selection); err != nil {
					return store.ErrStoreUnavailable
				}
				for _, member := range selection.Members {
					if member.PackID == packID.String() {
						return &skills.ImportError{Code: "pack_in_use", Message: "This skills pack is retained by a recorded conversation."}
					}
				}
			}
		}
		return nil
	})
}

func (m *Module) runSkillsTargets(ctx context.Context, sc store.Scope, rec model.Record) ([]skills.StoredTarget, error) {
	var out []skills.StoredTarget
	workspaceID := model.ID(rec.String(colRunAuthzWorkspaceID))
	if !workspaceID.IsZero() {
		workspace, err := sc.Workspaces().Get(ctx, workspaceID)
		if err != nil {
			return nil, err
		}
		out = append(out, skills.StoredTarget{Target: skills.Target{Kind: "workspace", ID: workspaceID.String()}, Version: workspace.Version, WorkspaceID: workspaceID})
	}
	if template := rec.String(colTemplateID); template != "" {
		stored, err := m.ReadSkillsTarget(ctx, sc, "template", template, false)
		if err != nil {
			return nil, err
		}
		out = append(out, skills.StoredTarget{Target: skills.Target{Kind: "template", ID: template}, Version: stored.Version, WorkspaceID: stored.WorkspaceID})
	}
	if agentRef := rec.String(colRunAgentRef); agentRef != "" {
		agents, page, err := sc.Agents().List(ctx, model.Query{Limit: 2, Filters: []model.Filter{eq("external_id", agentRef)}})
		if err != nil {
			return nil, err
		}
		if page.HasMore || len(agents) > 1 {
			return nil, store.ErrConflict
		}
		// An admitted external identity need not have a catalog agent target.
		if len(agents) == 0 {
			out = append(out, skills.StoredTarget{Target: skills.Target{Kind: "session", ID: rec.String(colRunRef)}, Version: rec.Int(model.ColVersion), WorkspaceID: workspaceID})
			return out, nil
		}
		agent := agents[0]
		effective := agent.WorkspaceID
		if effective.IsZero() {
			def, err := sc.DefaultWorkspace(ctx)
			if err != nil {
				return nil, err
			}
			effective = def.ID
		}
		if effective != workspaceID {
			return nil, store.ErrConflict
		}
		out = append(out, skills.StoredTarget{Target: skills.Target{Kind: "agent", ID: agent.ID.String()}, Version: agent.Version, WorkspaceID: workspaceID})
		members, page, err := sc.AgentGroupMembers().List(ctx, model.Query{Limit: skills.MaxSelectionGroups + 1, Filters: []model.Filter{eq("agent_id", agent.ID.String())}})
		if err != nil {
			return nil, err
		}
		if page.HasMore || len(members) > skills.MaxSelectionGroups {
			return nil, store.ErrConflict
		}
		for _, member := range members {
			group, err := sc.AgentGroups().Get(ctx, member.GroupID)
			if err != nil {
				return nil, err
			}
			effective := group.WorkspaceID
			if effective.IsZero() {
				def, err := sc.DefaultWorkspace(ctx)
				if err != nil {
					return nil, err
				}
				effective = def.ID
			}
			if effective != workspaceID {
				continue
			}
			out = append(out, skills.StoredTarget{Target: skills.Target{Kind: "agent_group", ID: group.ID.String()}, Version: group.Version, WorkspaceID: workspaceID})
		}
	}
	out = append(out, skills.StoredTarget{Target: skills.Target{Kind: "session", ID: rec.String(colRunRef)}, Version: rec.Int(model.ColVersion), WorkspaceID: workspaceID})
	return out, nil
}

func (m *Module) prepareSessionSkills(ctx, runCtx context.Context, tenant model.TenantID, runRef string, p CreateRunParams, spec *LaunchSpec) error {
	if m.rt.sessionSkills == nil {
		return nil
	}
	selection, err := m.rt.sessionSkills.ResolveSessionSkills(ctx, tenant, runRef)
	if err != nil {
		return err
	}
	if len(selection.Members) == 0 {
		return nil
	}
	rec, err := m.loadRun(ctx, tenant, runRef)
	if err != nil {
		return err
	}
	driver := rec.String(colRunProfileDriver)
	if driver != providerDriverClaude && driver != providerDriverCodex {
		return &runErr{503, "assigned skills require Claude Code or Codex native discovery"}
	}
	if spec.Confinement == nil {
		return &runErr{503, "assigned skills require filesystem confinement"}
	}
	if spec.Isolation != IsolationNative {
		return &runErr{503, "assigned skills require a native tool HOME"}
	}
	files, err := m.rt.sessionSkills.ReadSessionSkillFiles(ctx, tenant, selection)
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return errors.New("assigned skills have no verified files")
	}
	root, err := validateSessionWorkspaceRoot(m.rt.sessionSkillHomesRoot, m.dirOwnerCheck())
	if err != nil {
		return err
	}
	if pathsOverlap(root, spec.Dir) {
		return errors.New("skill HOME root overlaps the session workspace")
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		return err
	}
	dir, err := os.MkdirTemp(root, runRef+"-")
	if err != nil {
		return err
	}
	cleanup := func() {
		if err := os.RemoveAll(dir); err != nil {
			m.warnf("sessions: skill HOME cleanup failed", "run_ref", runRef, "error", redactErr(err))
		}
	}
	keep := false
	defer func() {
		if !keep {
			cleanup()
		}
	}()
	home := filepath.Join(dir, "home")
	folder := ".agents"
	if driver == providerDriverClaude {
		folder = ".claude"
	}
	native := filepath.Join(home, folder)
	if err := os.MkdirAll(filepath.Join(native, "skills"), 0700); err != nil {
		return err
	}
	// HOME itself is read-only, so a child cannot replace its discovery directory.
	// Writable caches are siblings; the account's configuration home keeps its
	// existing grant. A writable HOME ancestor would reopen the skill tree.
	for _, cache := range []string{".cache", ".local"} {
		path := filepath.Join(home, cache)
		if err := os.Mkdir(path, 0700); err != nil {
			return err
		}
		spec.Confinement.ReadWrite = append(spec.Confinement.ReadWrite, path)
	}
	for _, file := range files {
		if !fs.ValidPath(file.Path) || strings.Contains(file.Path, "\\") || file.Mode&0222 != 0 {
			return errors.New("invalid verified skill file")
		}
		name := filepath.Join(native, "skills", filepath.FromSlash(file.Path))
		if err := os.MkdirAll(filepath.Dir(name), 0700); err != nil {
			return err
		}
		f, err := os.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, file.Mode.Perm())
		if err != nil {
			return err
		}
		_, writeErr := f.Write(file.Bytes)
		if err := errors.Join(writeErr, f.Close()); err != nil {
			return err
		}
	}
	if driver == providerDriverClaude {
		// Session-only native discovery works even when governed launches disable
		// mutable project settings. The login/configuration HOME stays unchanged.
		if err := os.Mkdir(filepath.Join(native, ".claude-plugin"), 0700); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(native, ".claude-plugin", "plugin.json"), []byte(`{"name":"olivares"}`), 0444); err != nil {
			return err
		}
		spec.Args = append(spec.Args, "--plugin-dir", filepath.Join(home, folder))
	}
	for i := range spec.Env {
		if spec.Env[i].Name == envUserHome {
			spec.Env[i].Value = home
		}
	}
	spec.Confinement.ReadOnly = append(spec.Confinement.ReadOnly, home)
	spec.Confinement.Sealed = append(spec.Confinement.Sealed, native)
	spec.ConfinementRequired, spec.ConfinementRequireTruncateProtection = true, true
	err = m.authorizedMutate(ctx, tenant, Lease{SID: rec.String(colRunClaimSID), Holder: rec.String(colClaimHolder), Fence: rec.Int(colClaimFence)}, func(sc store.Scope) error {
		repo, err := sc.Ext(runKind)
		if err != nil {
			return err
		}
		current, err := findRunRec(ctx, repo, runRef)
		if err != nil {
			return err
		}
		if current.String(colRuntimeLaunchID) != rec.String(colRuntimeLaunchID) {
			return store.ErrConflict
		}
		ev, err := sc.Audit().Append(ctx, model.AuditDraft{Actor: p.Actor, ActorKind: p.ActorKind, Action: "sessions.skills.delivered", TargetKind: runKind, TargetID: model.ID(rec.String(model.ColID)), Meta: map[string]any{"run_ref": runRef, "runtime_launch_id": rec.String(colRuntimeLaunchID), "sha256": selection.Digest, "members": selection.Members}})
		if err == nil && ev.Seq == 0 {
			return errors.New("session skills delivery audit was not persisted")
		}
		return err
	})
	if err != nil {
		return err
	}
	keep = true
	context.AfterFunc(runCtx, cleanup)
	return nil
}
