// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package openclaw

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	defaultStateDir      = "~/.openclaw"
	defaultConfigPath    = "~/.openclaw/openclaw.json"
	configFileName       = "openclaw.json"
	legacyStateDirName   = ".clawdbot"
	legacyConfigFileName = "clawdbot.json"
)

type openclawInstall struct {
	agentRef   string
	stateDir   string
	configPath string
	profile    string
	legacy     bool
}

func (s *Source) discoverInstalls() []openclawInstall {
	if strings.TrimSpace(s.configPath) != "" {
		path := expandHome(s.configPath)
		if !fileExists(path) {
			return nil
		}
		stateDir := expandHome(s.stateDir)
		if stateDir == "" {
			stateDir = filepath.Dir(path)
		}
		return []openclawInstall{{
			agentRef:   s.agentRef,
			stateDir:   stateDir,
			configPath: path,
		}}
	}

	if strings.TrimSpace(s.stateDir) != "" {
		dir := expandHome(s.stateDir)
		if !dirExists(dir) && !fileExists(filepath.Join(dir, configFileName)) {
			return nil
		}
		return []openclawInstall{{
			agentRef:   s.agentRef,
			stateDir:   dir,
			configPath: filepath.Join(dir, configFileName),
		}}
	}

	var out []openclawInstall
	seen := map[string]struct{}{}
	add := func(inst openclawInstall) {
		if inst.stateDir == "" {
			inst.stateDir = filepath.Dir(inst.configPath)
		}
		if !fileExists(inst.configPath) && !dirExists(inst.stateDir) {
			return
		}
		key := filepath.Clean(inst.stateDir) + "\x00" + filepath.Clean(inst.configPath)
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		out = append(out, inst)
	}

	if cfg := strings.TrimSpace(os.Getenv("OPENCLAW_CONFIG_PATH")); cfg != "" {
		path := expandHome(cfg)
		stateDir := firstNonEmpty(strings.TrimSpace(os.Getenv("OPENCLAW_STATE_DIR")), strings.TrimSpace(os.Getenv("OPENCLAW_HOME")), filepath.Dir(path))
		add(openclawInstall{agentRef: s.agentRef, stateDir: expandHome(stateDir), configPath: path})
	} else if dir := firstNonEmpty(strings.TrimSpace(os.Getenv("OPENCLAW_STATE_DIR")), strings.TrimSpace(os.Getenv("OPENCLAW_HOME"))); dir != "" {
		stateDir := expandHome(dir)
		add(openclawInstall{agentRef: s.agentRef, stateDir: stateDir, configPath: filepath.Join(stateDir, configFileName)})
	} else if profile := strings.TrimSpace(os.Getenv("OPENCLAW_PROFILE")); profile != "" {
		stateDir := filepath.Join(homeDir(), ".openclaw-"+safeSuffix(profile))
		add(openclawInstall{agentRef: s.agentRef + "/" + safeSuffix(profile), stateDir: stateDir, configPath: filepath.Join(stateDir, configFileName), profile: safeSuffix(profile)})
	}

	home := homeDir()
	defaultDir := filepath.Join(home, ".openclaw")
	add(openclawInstall{agentRef: s.agentRef, stateDir: defaultDir, configPath: filepath.Join(defaultDir, configFileName)})

	legacyDir := filepath.Join(home, legacyStateDirName)
	add(openclawInstall{
		agentRef:   s.agentRef + "/legacy",
		stateDir:   legacyDir,
		configPath: filepath.Join(legacyDir, legacyConfigFileName),
		profile:    "legacy",
		legacy:     true,
	})

	matches, _ := filepath.Glob(filepath.Join(home, ".openclaw-*"))
	sort.Strings(matches)
	for _, dir := range matches {
		info, err := os.Stat(dir)
		if err != nil || !info.IsDir() {
			continue
		}
		profile := strings.TrimPrefix(filepath.Base(dir), ".openclaw-")
		if profile == "" {
			continue
		}
		add(openclawInstall{
			agentRef:   s.agentRef + "/" + safeSuffix(profile),
			stateDir:   dir,
			configPath: filepath.Join(dir, configFileName),
			profile:    safeSuffix(profile),
		})
	}
	return out
}

// maxSystemdUnits bounds the systemd unit scan so a directory full of units
// cannot drag discovery into unbounded work.
const maxSystemdUnits = 64

// systemdUnitDirs returns the directories scanned for OpenClaw service units.
// The test seam s.systemdRoots overrides them; otherwise the standard system
// unit directories plus the invoking user's user-scoped units are scanned.
func (s *Source) systemdUnitDirs() []string {
	if len(s.systemdRoots) > 0 {
		return s.systemdRoots
	}
	dirs := []string{"/etc/systemd/system", "/run/systemd/system", "/usr/lib/systemd/system"}
	if home := homeDir(); home != "" {
		dirs = append(dirs, filepath.Join(home, ".config", "systemd", "user"))
	}
	return dirs
}

// discoverSystemdUnits returns the sorted basenames of systemd unit files that
// look like OpenClaw services (openclaw*.service / clawdbot*.service). A match
// is a read-only host SIGNAL that an install is managed as a long-running
// service (an always-on agent — the profile the ClawHavoc campaign targeted),
// not merely a config file on disk. Bounded; never reads unit contents beyond
// the filename and never executes systemctl.
func (s *Source) discoverSystemdUnits() []string {
	seen := map[string]struct{}{}
	for _, dir := range s.systemdUnitDirs() {
		for _, pat := range []string{"openclaw*.service", "clawdbot*.service"} {
			matches, _ := filepath.Glob(filepath.Join(dir, pat))
			for _, m := range matches {
				if len(seen) >= maxSystemdUnits {
					break
				}
				seen[filepath.Base(m)] = struct{}{}
			}
		}
	}
	out := make([]string, 0, len(seen))
	for n := range seen {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func homeDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return home
}

func expandHome(path string) string {
	if strings.HasPrefix(path, "~/") {
		if home := homeDir(); home != "" {
			return filepath.Join(home, path[2:])
		}
	}
	return path
}

func safeSuffix(s string) string {
	s = strings.TrimSpace(s)
	s = strings.Trim(s, `/\`)
	s = strings.ReplaceAll(s, "/", "-")
	s = strings.ReplaceAll(s, `\`, "-")
	if s == "" {
		return "default"
	}
	return s
}
