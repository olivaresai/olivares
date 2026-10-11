// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"encoding/json"
	"slices"
	"strings"
)

// UnmarshalJSON reads only native command/path facts, never titles, descriptions,
// file contents, patches or output. Unsupported metadata leaves the projection
// incomplete; it must not change the existing permission request's validity.
func (call *acpToolCall) UnmarshalJSON(raw []byte) error {
	type identity acpToolCall
	var decoded identity
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return err
	}
	*call = acpToolCall(decoded)
	var input struct {
		RawInput struct {
			Command     *string  `json:"command"`
			Cmd         string   `json:"cmd"`
			FilePath    string   `json:"filePath"`
			Filepath    string   `json:"filepath"`
			Path        string   `json:"path"`
			ParentDir   string   `json:"parentDir"`
			Directories []string `json:"directories"`
			Files       []struct {
				FilePath string `json:"filePath"`
				MovePath string `json:"movePath"`
			} `json:"files"`
		} `json:"rawInput"`
		Locations []struct {
			Path string `json:"path"`
		} `json:"locations"`
	}
	complete := json.Unmarshal(raw, &input) == nil
	facts := codexApprovalFacts{CommandLine: input.RawInput.Cmd}
	if input.RawInput.Command != nil {
		facts.CommandLine = *input.RawInput.Command
	}
	addPath := func(path string) {
		if path != "" && strings.TrimSpace(path) == "" {
			complete = false
			return
		}
		if path != "" && len(facts.FilePaths) <= maxProviderApprovalPaths && !slices.Contains(facts.FilePaths, path) {
			facts.FilePaths = append(facts.FilePaths, path)
		}
	}
	for _, location := range input.Locations {
		if strings.TrimSpace(location.Path) == "" {
			complete = false
		}
		addPath(location.Path)
	}
	for _, path := range []string{input.RawInput.FilePath, input.RawInput.Filepath, input.RawInput.Path, input.RawInput.ParentDir} {
		addPath(path)
	}
	for _, path := range input.RawInput.Directories {
		if strings.TrimSpace(path) == "" {
			complete = false
		}
		addPath(path)
	}
	for _, file := range input.RawInput.Files {
		if strings.TrimSpace(file.FilePath) == "" {
			complete = false
		}
		addPath(file.FilePath)
		addPath(file.MovePath)
	}
	// A tool that runs a command and names none has no fact worth reviewing, whatever
	// paths it also lists.
	runsAnUnnamedCommand := call.Kind == "execute" && strings.TrimSpace(facts.CommandLine) == ""
	facts.Complete = complete && !runsAnUnnamedCommand && (strings.TrimSpace(facts.CommandLine) != "" || len(facts.FilePaths) > 0)
	call.facts = cleanApprovalFacts(facts)
	return nil
}
