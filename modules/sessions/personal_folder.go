// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// personal_folder.go gives each signed-in person a folder of their own, "My files".
// It is an ordinary registered folder (workspace.go) whose Resource records its
// owner, so the jailed, size-limited, DLP-classified and audited file API, the
// terminal and the launch rights all apply to it unchanged. The one addition is
// CheckFolderRight's owner rule below: the content of an owned folder is the
// owner's alone, and anyone else is told it does not exist.

const (
	personalFolderName = "My files"
	// personalFolderMode is the permission of a person's directory.
	personalFolderMode os.FileMode = 0o700
	// personalOwnerPrefix keeps a person apart from a team or a name an operator put
	// in Resource.Owner by hand: only "user:<id>" makes a folder personal.
	personalOwnerPrefix = "user:"
)

// personalFolderOwner is the Resource.Owner a person's folder records: the
// audit-actor form of the user id. Only a signed-in person owns one, as in
// vault/<user-id> (a token carries its owner's id; an agent identity never owns).
func personalFolderOwner(p auth.Principal) string {
	if p.UserID.IsZero() || p.AgentIdentity != "" {
		return ""
	}
	return personalOwnerPrefix + p.UserID.String()
}

// personalFolderAdmits is the owner rule of CheckFolderRight. The rights that
// reach a folder's content are the owner's alone; administering the folder
// (modify, access control) stays with the administrators, who see no content.
func personalFolderAdmits(resource model.Resource, p auth.Principal, right auth.TrusteeRight) error {
	if !strings.HasPrefix(resource.Owner, personalOwnerPrefix) {
		return nil
	}
	switch right {
	case auth.TrusteeBrowse, auth.TrusteeRead, auth.TrusteeWrite, auth.TrusteeCreate, auth.TrusteeErase:
	default:
		return nil
	}
	if personalFolderOwner(p) == resource.Owner {
		return nil
	}
	return &runErr{http.StatusNotFound, "not found"}
}

// UsePersonalFilesRoot late-binds the directory that holds one folder per person
// (scoped by organization and person), wired inside the data directory so
// the backup and the pre-upgrade snapshot cover it. Empty leaves personal folders
// off on this node.
func (m *Module) UsePersonalFilesRoot(dir string) error {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return nil
	}
	clean := filepath.Clean(dir)
	if !filepath.IsAbs(clean) || filepath.Dir(clean) == clean {
		return fmt.Errorf("personal files root %q must be an absolute directory below the filesystem root", dir)
	}
	m.rt.personalFilesRoot = clean
	return nil
}

// ensurePersonalFolder returns the ref of the caller's own folder, creating its
// directory (mode 0700) and its registration the first time it is asked. An empty
// ref with no error means the caller has none here: this node has no root wired,
// or the caller is not a person.
func (m *Module) ensurePersonalFolder(ctx context.Context, tenant model.TenantID, p auth.Principal) (string, error) {
	owner, root := personalFolderOwner(p), m.rt.personalFilesRoot
	if owner == "" || root == "" || m.Data == nil {
		return "", nil
	}
	if on, err := m.personalFoldersEnabled(ctx, tenant); err != nil || !on {
		return "", err
	}
	m.rt.personalMu.Lock()
	defer m.rt.personalMu.Unlock()
	if err := os.MkdirAll(root, personalFolderMode); err != nil {
		return "", fmt.Errorf("create personal files root: %w", err)
	}
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("resolve personal files root: %w", err)
	}
	dir := filepath.Join(canonicalRoot, "tenant-"+tenant.String(), p.UserID.String())
	for generation := 0; generation < ownershipInventoryLimit; generation++ {
		real := dir
		if resolved, err := filepath.EvalSymlinks(dir); err == nil {
			real = resolved
		} else if !os.IsNotExist(err) {
			return "", fmt.Errorf("resolve personal folder: %w", err)
		}
		var ref string
		var transferred model.ID
		registered := false
		err := m.Data.View(ctx, tenant, func(sc store.Scope) error {
			repo, err := sc.Ext(workspaceKind)
			if err != nil {
				return err
			}
			rows, _, err := repo.List(ctx, model.Query{Filters: []model.Filter{eq(colWsRootPath, real)}, Limit: 1})
			if err != nil || len(rows) == 0 {
				return err
			}
			registered = true
			resource, err := sc.Resources().Get(ctx, recordID(rows[0]))
			if err != nil {
				return err
			}
			if resource.Owner != owner {
				if rows[0].Bool(colWsTransferred) && strings.HasPrefix(resource.Owner, personalOwnerPrefix) {
					transferred = resource.ID
					return nil
				}
				return errors.New("the personal folder path is registered as another folder")
			}
			ref = rows[0].String(colWsRef)
			return nil
		})
		if err != nil {
			return "", err
		}
		if !transferred.IsZero() {
			// Siblings keep the successor's folder from enclosing the mover's new files.
			// The prior registration ID makes concurrent nodes choose one stable path.
			dir = filepath.Join(filepath.Dir(dir), p.UserID.String()+"-"+transferred.String())
			continue
		}
		if !registered && real != dir {
			return "", errors.New("a new personal folder must stay in its tenant namespace")
		}
		if err := os.MkdirAll(dir, personalFolderMode); err != nil {
			return "", fmt.Errorf("create personal folder: %w", err)
		}
		resolved, err := filepath.EvalSymlinks(dir)
		if err != nil {
			return "", fmt.Errorf("resolve personal folder: %w", err)
		}
		if !registered && resolved != dir {
			return "", errors.New("a new personal folder must stay in its tenant namespace")
		}
		// Repair modes only on this person's registered folder or on its new,
		// tenant-scoped allocation, never while probing another owner's old path.
		if err := os.Chmod(resolved, personalFolderMode); err != nil {
			return "", fmt.Errorf("make personal folder private: %w", err)
		}
		if ref != "" {
			return ref, nil
		}
		department, _ := p.ConfinedWorkspaceIn(tenant)
		created, err := m.createWorkspace(ctx, tenant, CreateWorkspaceParams{Department: department, Name: personalFolderName, RootPath: resolved, MountMode: mountRW, Owner: owner, Actor: p.Actor(), ActorKind: p.ActorKind()})
		if err != nil {
			return "", err
		}
		return created.WorkspaceRef, nil
	}

	return "", errors.New("personal folder ownership history is too long")
}

// orgSettingPersonalFolders is the tenant's Org.Settings key for personal folders.
// Absent means on; only an explicit false is stored, so a tenant that never touched
// the setting keeps the default.
const orgSettingPersonalFolders = "personal_folders"

type personalFoldersSettingDTO struct {
	Enabled bool `json:"enabled"`
}

// personalFoldersEnabled reports whether this tenant gives each person a folder.
func (m *Module) personalFoldersEnabled(ctx context.Context, tenant model.TenantID) (bool, error) {
	enabled := true
	err := m.Data.View(ctx, tenant, func(sc store.Scope) error {
		org, err := sc.Org(ctx)
		if err != nil {
			return err
		}
		if v, ok := org.Settings[orgSettingPersonalFolders].(bool); ok {
			enabled = v
		}
		return nil
	})
	return enabled, err
}

// handlePersonalFoldersSettings reads or changes whether this organization gives each person a personal folder, My files; changing it takes folder administration and is audited.
// Turning it off creates no new personal folder; the folders already made stay where
// they are and keep their owner.
func (m *Module) handlePersonalFoldersSettings(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	if r.Method == http.MethodGet {
		enabled, err := m.personalFoldersEnabled(r.Context(), mc.Tenant)
		if err != nil {
			writeRunErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, personalFoldersSettingDTO{enabled})
		return
	}
	var body struct {
		Enabled *bool `json:"enabled"`
	}
	if !decodeJSONBody(w, r, &body) {
		return
	}
	if body.Enabled == nil {
		writeJSON(w, http.StatusBadRequest, errorBody("enabled is required"))
		return
	}
	err := m.Data.Mutate(r.Context(), mc.Tenant, func(sc store.Scope) error {
		org, err := sc.Org(r.Context())
		if err != nil {
			return err
		}
		settings := maps.Clone(org.Settings)
		if settings == nil {
			settings = map[string]any{}
		}
		if *body.Enabled {
			delete(settings, orgSettingPersonalFolders)
		} else {
			settings[orgSettingPersonalFolders] = false
		}
		if _, err := sc.SetOrgSettings(r.Context(), settings, org.Version); err != nil {
			return err
		}
		_, err = sc.Audit().Append(r.Context(), model.AuditDraft{Actor: mc.Principal.Actor(), ActorKind: mc.Principal.ActorKind(), Action: "sessions.personal_folders.changed", TargetKind: "sessions.personal_folders", Meta: map[string]any{"enabled": *body.Enabled}})
		return err
	})
	if err != nil {
		m.warnf("sessions: could not save the personal folders setting", "err", redactErr(err))
		writeRunErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, personalFoldersSettingDTO{*body.Enabled})
}
