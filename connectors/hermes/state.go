// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package hermes

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/olivaresai/olivares/connectors/internal/redact"
)

const (
	maxStateEntries = 200
)

type hermesStateFacts struct {
	SkillCount           int
	SkillNames           []string
	PendingSkillCount    int
	CommunityTapCount    int
	CommunityTapNames    []string
	PairingFileCount     int
	PairingApprovedCount int
	MigrationOpenClaw    bool
	MemoriesPresent      bool
	AgentsMD             bool
	Version              string
}

func scanState(stateDir, configPath string) hermesStateFacts {
	var facts hermesStateFacts
	facts.SkillCount, facts.SkillNames = countSkillDir(filepath.Join(stateDir, "skills"))
	facts.PendingSkillCount = countEntries(filepath.Join(stateDir, "pending", "skills"))
	facts.CommunityTapCount, facts.CommunityTapNames = scanFirstTapState(
		filepath.Join(stateDir, "skills", ".hub", "taps.json"),
		filepath.Join(stateDir, ".hub", "taps.json"),
	)
	facts.PairingFileCount, facts.PairingApprovedCount = scanPairings(filepath.Join(stateDir, "pairing"))
	facts.MigrationOpenClaw = dirExists(filepath.Join(stateDir, "migration", "openclaw"))
	facts.MemoriesPresent = fileExists(filepath.Join(stateDir, "memories", "MEMORY.md")) || fileExists(filepath.Join(stateDir, "memories", "USER.md"))
	facts.AgentsMD = agentsMDPresent(stateDir, configPath)
	facts.Version = readHermesVersion(filepath.Join(stateDir, "hermes-agent", "pyproject.toml"))
	return facts
}

func countSkillDir(dir string) (int, []string) {
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return 0, nil
	}
	if fileExists(filepath.Join(dir, "SKILL.md")) {
		return 1, []string{filepath.Base(dir)}
	}
	count := 0
	var names []string
	entries := 0
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		entries++
		if entries > maxStateEntries {
			return filepath.SkipAll
		}
		if path == dir {
			return nil
		}
		if d.IsDir() {
			if d.Name() == ".hub" || strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			if filepath.Dir(path) != dir {
				return filepath.SkipDir
			}
			if fileExists(filepath.Join(path, "SKILL.md")) {
				count++
				names = append(names, d.Name())
			}
			return filepath.SkipDir
		}
		return nil
	})
	sort.Strings(names)
	return count, names
}

func countEntries(dir string) int {
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return 0
	}
	count := 0
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		count++
		if count >= maxStateEntries {
			break
		}
	}
	return count
}

func scanFirstTapState(paths ...string) (int, []string) {
	for _, path := range paths {
		count, names, ok := scanTapState(path)
		if ok {
			return count, names
		}
	}
	return 0, nil
}

func scanTapState(path string) (int, []string, bool) {
	data, err := os.ReadFile(path) //nolint:gosec // local taps metadata; names/trust only.
	if err != nil {
		return 0, nil, false
	}
	var root any
	if err := json.Unmarshal(data, &root); err != nil {
		return 0, nil, true
	}
	offenders := map[string]struct{}{}
	walkTaps(root, "", offenders)
	names := sortedKeys(offenders)
	if len(names) > maxStateEntries {
		names = names[:maxStateEntries]
	}
	return len(names), names, true
}

func walkTaps(v any, inheritedName string, offenders map[string]struct{}) {
	switch x := v.(type) {
	case []any:
		for _, item := range x {
			walkTaps(item, inheritedName, offenders)
		}
	case map[string]any:
		name := firstNonEmpty(stringField(x, "name"), stringField(x, "tap"), stringField(x, "id"), stringField(x, "url"), inheritedName)
		trust := strings.ToLower(firstNonEmpty(stringField(x, "trust"), stringField(x, "trust_level"), stringField(x, "trustLevel")))
		if trust != "" && trust != "builtin" && trust != "official" {
			offenders[redact.Clean(name)] = struct{}{}
		}
		for key, val := range x {
			if key == "name" || key == "tap" || key == "id" || key == "url" || key == "trust" || key == "trust_level" || key == "trustLevel" {
				continue
			}
			walkTaps(val, firstNonEmpty(name, key), offenders)
		}
	}
}

func stringField(m map[string]any, key string) string {
	if s, ok := m[key].(string); ok {
		return strings.TrimSpace(s)
	}
	return ""
}

func scanPairings(dir string) (int, int) {
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return 0, 0
	}
	matches, _ := filepath.Glob(filepath.Join(dir, "*-approved.json"))
	sort.Strings(matches)
	if len(matches) > maxStateEntries {
		matches = matches[:maxStateEntries]
	}
	total := 0
	for _, path := range matches {
		total += countApprovedEntries(path)
	}
	return len(matches), total
}

func countApprovedEntries(path string) int {
	data, err := os.ReadFile(path) //nolint:gosec // local pairing file; count only.
	if err != nil {
		return 0
	}
	var root any
	if err := json.Unmarshal(data, &root); err != nil {
		return 0
	}
	switch x := root.(type) {
	case []any:
		if len(x) > maxStateEntries {
			return maxStateEntries
		}
		return len(x)
	case map[string]any:
		if users, ok := x["users"].([]any); ok {
			if len(users) > maxStateEntries {
				return maxStateEntries
			}
			return len(users)
		}
		if approved, ok := x["approved"].([]any); ok {
			if len(approved) > maxStateEntries {
				return maxStateEntries
			}
			return len(approved)
		}
		if len(x) > maxStateEntries {
			return maxStateEntries
		}
		return len(x)
	default:
		return 0
	}
}

func readHermesVersion(path string) string {
	data, err := os.ReadFile(path) //nolint:gosec // local pyproject metadata; version line only.
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "version") {
			continue
		}
		idx := strings.IndexByte(line, '=')
		if idx < 0 {
			continue
		}
		v := strings.TrimSpace(line[idx+1:])
		v = strings.Trim(v, `"'`)
		if v != "" {
			return v
		}
	}
	return ""
}

func agentsMDPresent(stateDir, configPath string) bool {
	if stateDir != "" && fileExists(filepath.Join(stateDir, "AGENTS.md")) {
		return true
	}
	if configPath != "" && fileExists(filepath.Join(filepath.Dir(configPath), "AGENTS.md")) {
		return true
	}
	return false
}
