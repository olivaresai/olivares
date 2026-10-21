// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package skills

import (
	"path"
	"strings"
)

// componentKind recognizes native Markdown members only at the pack root.
// Configuration, hooks, executable files and arbitrary repository trees do not
// become instruction members by being next to an agent or command.
func componentKind(name string) string {
	parts := strings.Split(name, "/")
	if len(parts) < 2 || !strings.HasSuffix(name, ".md") {
		return ""
	}
	switch parts[0] {
	case "agents":
		if len(parts) == 2 {
			return "agent"
		}
	case "commands":
		if len(parts) == 2 {
			return "command"
		}
	case "rules":
		return "rule"
	}
	return ""
}

func (m Member) owns(name string) bool {
	if m.Kind != "" {
		return name == m.Directory
	}
	return strings.HasPrefix(name, m.Directory+"/")
}

func parseComponent(name string, b []byte, kind string) (Member, error) {
	empty := Member{}
	if strings.TrimSpace(string(b)) == "" {
		return empty, refuse("invalid_skill", "instruction encoding/size")
	}
	nativeName := strings.TrimSuffix(strings.SplitN(name, "/", 2)[1], ".md")
	for _, part := range strings.Split(nativeName, "/") {
		if !portableName.MatchString(part) || len(part) > 64 {
			return empty, refuse("invalid_skill", "instruction name")
		}
	}
	// Reuse the instruction parser for optional metadata and active-content detection.
	m, err := parseInstruction(path.Base(nativeName), b, kind)
	if err != nil {
		return empty, err
	}
	m.Name, m.Directory = kind+":"+nativeName, name
	return m, nil
}
