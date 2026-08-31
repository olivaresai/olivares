// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package hermes

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	defaultHermesHome = "~/.hermes"
	defaultConfigPath = "~/.hermes/config.yaml"
	defaultManagedDir = "/etc/hermes"
	configFileName    = "config.yaml"
)

type hermesInstall struct {
	agentRef   string
	stateDir   string
	configPath string
	managedDir string
	profile    string
}

func (s *Source) discoverInstalls() []hermesInstall {
	managedDir := s.resolvedManagedDir()
	if strings.TrimSpace(s.configPath) != "" {
		path := expandHome(s.configPath)
		if !fileExists(path) {
			return nil
		}
		stateDir := expandHome(s.hermesHome)
		if stateDir == "" {
			stateDir = filepath.Dir(path)
		}
		return []hermesInstall{{
			agentRef:   s.agentRef,
			stateDir:   stateDir,
			configPath: path,
			managedDir: managedDir,
		}}
	}

	if strings.TrimSpace(s.hermesHome) != "" {
		dir := expandHome(s.hermesHome)
		if !dirExists(dir) && !fileExists(filepath.Join(dir, configFileName)) {
			return nil
		}
		return []hermesInstall{{
			agentRef:   s.agentRef,
			stateDir:   dir,
			configPath: filepath.Join(dir, configFileName),
			managedDir: managedDir,
		}}
	}

	var out []hermesInstall
	seen := map[string]struct{}{}
	add := func(inst hermesInstall) {
		if inst.stateDir == "" {
			inst.stateDir = filepath.Dir(inst.configPath)
		}
		if inst.managedDir == "" {
			inst.managedDir = managedDir
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

	if dir := strings.TrimSpace(os.Getenv("HERMES_HOME")); dir != "" {
		stateDir := expandHome(dir)
		add(hermesInstall{agentRef: s.agentRef, stateDir: stateDir, configPath: filepath.Join(stateDir, configFileName), managedDir: managedDir})
	}

	home := homeDir()
	defaultDir := filepath.Join(home, ".hermes")
	add(hermesInstall{agentRef: s.agentRef, stateDir: defaultDir, configPath: filepath.Join(defaultDir, configFileName), managedDir: managedDir})

	matches, _ := filepath.Glob(filepath.Join(defaultDir, "profiles", "*"))
	sort.Strings(matches)
	for _, dir := range matches {
		info, err := os.Stat(dir)
		if err != nil || !info.IsDir() {
			continue
		}
		profile := safeSuffix(filepath.Base(dir))
		add(hermesInstall{
			agentRef:   s.agentRef + "/" + profile,
			stateDir:   dir,
			configPath: filepath.Join(dir, configFileName),
			managedDir: managedDir,
			profile:    profile,
		})
	}
	return out
}

func (s *Source) resolvedManagedDir() string {
	if strings.TrimSpace(s.managedDir) != "" {
		return expandHome(s.managedDir)
	}
	if dir := strings.TrimSpace(os.Getenv("HERMES_MANAGED_DIR")); dir != "" {
		return expandHome(dir)
	}
	return defaultManagedDir
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
