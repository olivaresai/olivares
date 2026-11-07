// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/driverfacts"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/skills"
)

// DetectSkills reads only native skill metadata, never credentials or tool processes.
func (m *Module) DetectSkills(ctx context.Context, mc api.ModuleContext) ([]skills.DetectedSkill, error) {
	out := []skills.DetectedSkill{}
	remaining := skills.MaxFiles
	seen := map[string]bool{}
	log := m.log
	if log == nil {
		log = slog.Default()
	}
	warn := func(path string, err error) {
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			log.WarnContext(ctx, "sessions: skipped native skill path", "path", path, "error", err)
		}
	}
	add := func(dir *os.Root, path, source, driver, profile string) {
		key := path + "\x00" + driver + "\x00" + profile
		if seen[key] {
			return
		}
		seen[key] = true
		f, err := openStateFile(dir, "SKILL.md")
		if err != nil {
			warn(path, err)
			return
		}
		defer f.Close()
		info, err := f.Stat()
		if err != nil {
			warn(path, err)
			return
		}
		if !info.Mode().IsRegular() || !singleLink(info) || info.Size() > skills.MaxSkillBytes {
			warn(path, errClaudeStateSpecial)
			return
		}
		body, err := io.ReadAll(io.LimitReader(f, skills.MaxSkillBytes+1))
		if err != nil {
			warn(path, err)
			return
		}
		skill, err := skills.ParseDetectedSkill(filepath.Base(filepath.Dir(path)), body)
		if err != nil {
			warn(path, err)
			return
		}
		skill.Source, skill.Path, skill.Driver, skill.ProfileRef = source, path, driver, profile
		out = append(out, skill)
	}
	// Open every component without following further links, including home aliases
	// swapped after revalidation. Reused for scan roots and in-home pack targets.
	openDir := func(path string) (*os.Root, error) {
		root, err := os.OpenRoot(filepath.VolumeName(path) + string(os.PathSeparator))
		if err != nil {
			return nil, err
		}
		for _, name := range strings.Split(strings.TrimLeft(strings.TrimPrefix(path, filepath.VolumeName(path)), string(os.PathSeparator)), string(os.PathSeparator)) {
			if name == "" {
				continue
			}
			child, err := openRealDir(root, name)
			root.Close()
			if err != nil {
				return nil, err
			}
			root = child
		}
		return root, nil
	}
	var visit func(*os.Root, string, string, string, string, string, int, bool)
	visit = func(dir *os.Root, path, home, source, driver, profile string, depth int, recursive bool) {
		if remaining <= 0 || ctx.Err() != nil {
			return
		}
		entries, err := readDirBounded(ctx, dir, remaining)
		if err != nil {
			warn(path, err)
			if errors.Is(err, errActivityTooLarge) {
				remaining = 0
			}
			return
		}
		remaining -= len(entries)
		for _, entry := range entries {
			if ctx.Err() != nil {
				return
			}
			name := entry.Name()
			if recursive && (name == ".git" || name == "node_modules") {
				continue
			}
			// openRealDir checks Lstat and the opened inode, including a concurrent rename.
			child, err := openRealDir(dir, name)
			linked := entry.Type()&os.ModeSymlink != 0
			if err != nil && linked && (!recursive || filepath.Base(path) == "skills") {
				var target string
				target, err = dir.Readlink(name)
				if err == nil {
					if !filepath.IsAbs(target) {
						target = filepath.Join(path, target)
					}
					target = filepath.Clean(target)
					rel, relErr := filepath.Rel(home, target)
					if relErr != nil || !filepath.IsLocal(rel) {
						err = errClaudeStateSpecial
					} else {
						child, err = openDir(target)
					}
				}
			}
			if err != nil {
				if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
					warn(filepath.Join(path, name), err)
				}
				continue
			}
			childPath := filepath.Join(path, name)
			if !recursive || filepath.Base(path) == "skills" {
				add(child, filepath.Join(childPath, "SKILL.md"), source, driver, profile)
			}
			if recursive && !linked {
				if depth < skills.MaxDepth {
					visit(child, childPath, home, source, driver, profile, depth+1, true)
				} else {
					warn(childPath, errActivityTooLarge)
				}
			}
			child.Close()
		}
	}
	scan := func(home, rel, source, driver, profile string, recursive bool) {
		if home == "" || remaining <= 0 || ctx.Err() != nil {
			return
		}
		remaining--
		if err := revalidateHome("skill_home", home); err != nil {
			warn(home, err)
			return
		}
		root, err := openDir(filepath.Join(home, rel))
		if err != nil {
			warn(filepath.Join(home, rel), err)
			return
		}
		defer root.Close()
		visit(root, filepath.Join(home, rel), home, source, driver, profile, 0, recursive)
	}
	scanHomes := func(home, config, driver, profile string) {
		facts, ok := driverfacts.Lookup(driver)
		if !ok || facts.SkillsDir == "" {
			return
		}
		scan(home, filepath.FromSlash(facts.SkillsDir), "tool_folder", driver, profile, false)
		if config != "" {
			scan(config, "skills", "tool_folder", driver, profile, false)
		}
		if facts.Key == "claude" {
			scan(home, filepath.Join(".claude", "plugins"), "pack", driver, profile, true)
			if config != "" {
				scan(config, "plugins", "pack", driver, profile, true)
			}
		}
	}
	if m.ServerLoginOpen != nil && m.ServerLoginOpen(ctx, mc.Tenant) {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		// HOME may be an operator-configured alias; profiles are already canonical.
		home, err = filepath.EvalSymlinks(home)
		if err != nil {
			return nil, err
		}
		for _, facts := range driverfacts.All() {
			if err := m.requireToolAccess(ctx, mc, facts.Key); err != nil {
				if errors.Is(err, ErrProfileNotAssigned) {
					continue
				}
				return nil, err
			}
			scanHomes(home, "", facts.Key, "")
		}
	}
	if m.WorkAuthorizer != nil && !m.WorkAuthorizer.Authorize(ctx, auth.Request{
		Principal: mc.Principal, Tenant: mc.Tenant, Permission: permProfileRead, Resource: auth.ResourceFor(permProfileRead),
	}).Allow {
		return out, ctx.Err()
	}
	q := model.Query{Limit: 200}
	profilesRead := 0
	for remaining > 0 && profilesRead < skills.MaxFiles && ctx.Err() == nil {
		q.Limit = min(200, skills.MaxFiles-profilesRead)
		profiles, page, err := m.ListProfiles(ctx, mc.Tenant, ProfileActive, q)
		if err != nil {
			return nil, err
		}
		profilesRead += len(profiles)
		profiles, err = visibleTo(ctx, m, mc, profiles, func(p ProviderProfile) (string, string) { return p.Ref, p.Driver })
		if err != nil {
			return nil, err
		}
		for _, profile := range profiles {
			if profile.EnvironmentRef != m.rt.environmentRef {
				continue
			}
			scanHomes(profile.UserHome, profile.ConfigHome, profile.Driver, profile.Ref)
		}
		if !page.HasMore {
			break
		}
		q.Cursor = page.Cursor
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		return a.Path+a.Driver+a.ProfileRef < b.Path+b.Driver+b.ProfileRef
	})
	return out, ctx.Err()
}
