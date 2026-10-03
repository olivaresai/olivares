// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
package sessions

import (
	"encoding/json"
	"sort"
	"strings"

	"github.com/olivaresai/olivares/connectors/redact"
)

const maxProviderApprovalFactBytes = 1024
const maxProviderApprovalPaths = 32
const maxProviderApprovalFactItems = 64

type codexApprovalFacts struct {
	CommandLine          string
	FilePaths            []string
	Complete             bool
	EffectiveCommandLine string   `json:"-"`
	EffectiveFilePaths   []string `json:"-"`
}

// Only paths/command are decoded. In particular, a FileUpdateChange.diff never
// enters this bounded map. The installed approval schema names only itemId, so
// item/started supplies the paths of the exact owned turn's file-change item.
func (s *codexSession) observeApprovalItem(params json.RawMessage) {
	var n struct {
		ThreadID string `json:"threadId"`
		TurnID   string `json:"turnId"`
		Item     struct {
			ID      string `json:"id"`
			Type    string `json:"type"`
			Command string `json:"command"`
			Changes []struct {
				Path string `json:"path"`
			} `json:"changes"`
		} `json:"item"`
	}
	if json.Unmarshal(params, &n) != nil || n.Item.ID == "" {
		return
	}
	facts := codexApprovalFacts{CommandLine: n.Item.Command, Complete: true}
	for _, change := range n.Item.Changes {
		facts.FilePaths = append(facts.FilePaths, change.Path)
	}
	facts = cleanApprovalFacts(facts)
	s.mu.Lock()
	defer s.mu.Unlock()
	if n.ThreadID != s.threadID || n.TurnID == "" || n.TurnID != s.turnID {
		return
	}
	if s.approvalFacts == nil {
		s.approvalFacts = map[string]codexApprovalFacts{}
	}
	if _, known := s.approvalFacts[n.Item.ID]; !known && len(s.approvalFacts) >= maxProviderApprovalFactItems {
		return
	}
	s.approvalFacts[n.Item.ID] = facts
}

func cleanApprovalFacts(f codexApprovalFacts) codexApprovalFacts {
	bytes := len(f.CommandLine)
	if len(f.FilePaths) > maxProviderApprovalPaths {
		return codexApprovalFacts{}
	}
	for _, p := range f.FilePaths {
		if strings.TrimSpace(p) == "" {
			return codexApprovalFacts{}
		}
		bytes += len(p)
	}
	if bytes > maxProviderApprovalFactBytes {
		return codexApprovalFacts{}
	}
	if f.EffectiveCommandLine == "" {
		f.EffectiveCommandLine = f.CommandLine
	}
	if f.EffectiveFilePaths == nil {
		f.EffectiveFilePaths = append([]string(nil), f.FilePaths...)
	}
	f.FilePaths = append([]string(nil), f.FilePaths...)
	command, err := redact.ReviewableShellCommand(f.CommandLine, redact.Clean(f.CommandLine))
	if err != nil {
		f.CommandLine = "command not reviewable"
		f.Complete = false
	} else {
		f.CommandLine = command
	}
	for i, p := range f.FilePaths {
		f.FilePaths[i] = redact.Clean(p)
	}
	return f
}

func (s *codexSession) approvalRequestFacts(method string, params json.RawMessage) codexApprovalFacts {
	var p struct {
		ItemID      string                     `json:"itemId"`
		Command     json.RawMessage            `json:"command"`
		FileChanges map[string]json.RawMessage `json:"fileChanges"`
	}
	if json.Unmarshal(params, &p) != nil {
		return codexApprovalFacts{}
	}
	s.mu.Lock()
	prior := s.approvalFacts[p.ItemID]
	s.mu.Unlock()
	facts := codexApprovalFacts{}
	switch method {
	case codexReqCommandApproval, codexReqLegacyExecApproval:
		if json.Unmarshal(p.Command, &facts.CommandLine) != nil {
			var argv []string
			if json.Unmarshal(p.Command, &argv) == nil {
				facts.CommandLine = strings.Join(argv, " ")
			}
		}
		if facts.CommandLine == "" {
			facts.CommandLine = prior.CommandLine
			facts.EffectiveCommandLine = prior.EffectiveCommandLine
			facts.Complete = prior.Complete && strings.TrimSpace(facts.CommandLine) != ""
		} else {
			facts.Complete = strings.TrimSpace(facts.CommandLine) != ""
		}
	case codexReqFileChangeApproval, codexReqLegacyPatchApproval:
		facts.FilePaths = append([]string(nil), prior.EffectiveFilePaths...)
		if facts.FilePaths == nil {
			facts.FilePaths = append([]string(nil), prior.FilePaths...)
		}
		for path := range p.FileChanges {
			facts.FilePaths = append(facts.FilePaths, path)
		}
		sort.Strings(facts.FilePaths)
		facts.Complete = len(facts.FilePaths) > 0
	default:
		facts.Complete = true
	}
	return cleanApprovalFacts(facts)
}
