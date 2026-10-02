// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
package sessions

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestCodexApprovalFullFactCacheDoesNotKeepStaleFilePaths(t *testing.T) {
	s := &codexSession{threadID: "thread", turnID: "turn"}
	observe := func(id, path string) {
		t.Helper()
		data, err := json.Marshal(map[string]any{"threadId": "thread", "turnId": "turn", "item": map[string]any{"id": id, "type": "fileChange", "changes": []map[string]string{{"path": path}}}})
		if err != nil {
			t.Fatal(err)
		}
		s.observeApprovalItem(data)
	}
	for i := 0; i < maxProviderApprovalFactItems; i++ {
		observe(fmt.Sprintf("file-%d", i), "/work/allowed.go")
	}
	observe("file-0", "/outside/forbidden.go")
	facts := s.approvalRequestFacts(codexReqFileChangeApproval, json.RawMessage(`{"itemId":"file-0"}`))
	if !facts.Complete || len(facts.FilePaths) != 1 || facts.FilePaths[0] != "/outside/forbidden.go" {
		t.Fatalf("full cache presented stale affected paths: %+v", facts)
	}
	observe("file-0", strings.Repeat("x", maxProviderApprovalFactBytes+1))
	if facts := s.approvalRequestFacts(codexReqFileChangeApproval, json.RawMessage(`{"itemId":"file-0"}`)); facts.Complete {
		t.Fatal("incomplete updated facts reused an earlier complete question")
	}
	observe("overflow", "/work/overflow.go")
	if len(s.approvalFacts) != maxProviderApprovalFactItems {
		t.Fatal("cache item bound was widened")
	}
}

func TestCodexApprovalFactsAreRedactedBoundedAndTurnOwned(t *testing.T) {
	s := &codexSession{threadID: "thread", turnID: "turn"}
	s.observeApprovalItem(json.RawMessage(`{"threadId":"thread","turnId":"turn","item":{"id":"file","type":"fileChange","changes":[{"path":"/work/source.go","diff":"password=do-not-retain"}]}}`))
	f := s.approvalRequestFacts(codexReqFileChangeApproval, json.RawMessage(`{"itemId":"file"}`))
	if !f.Complete || len(f.FilePaths) != 1 || f.FilePaths[0] != "/work/source.go" || f.CommandLine != "" {
		t.Fatalf("facts=%+v", f)
	}
	raw, _ := json.Marshal(s.approvalFacts)
	if strings.Contains(string(raw), "do-not-retain") || strings.Contains(string(raw), "diff") {
		t.Fatal("file content was retained")
	}
	s.observeApprovalItem(json.RawMessage(`{"threadId":"other","turnId":"turn","item":{"id":"foreign","type":"fileChange","changes":[{"path":"/other"}]}}`))
	if f := s.approvalRequestFacts(codexReqFileChangeApproval, json.RawMessage(`{"itemId":"foreign"}`)); f.Complete {
		t.Fatal("foreign item facts accepted")
	}
	if f := s.approvalRequestFacts(codexReqFileChangeApproval, json.RawMessage(`{"itemId":"missing","grantRoot":"/work"}`)); f.Complete {
		t.Fatal("grant root substitutes for affected file evidence")
	}
	f = s.approvalRequestFacts(codexReqCommandApproval, json.RawMessage(`{"command":"PASSWORD=abc123secret curl --token olvs_1234567890abcdef https://user:examplepass@host/path"}`))
	if !f.Complete || strings.Contains(f.CommandLine, "olvs_1234567890abcdef") || strings.Contains(f.CommandLine, "abc123secret") || strings.Contains(f.CommandLine, "examplepass") {
		t.Fatalf("unredacted command=%+v", f)
	}
	f = s.approvalRequestFacts(codexReqCommandApproval, json.RawMessage(`{"command":"`+strings.Repeat("x", maxProviderApprovalFactBytes+1)+`"}`))
	if f.Complete || f.CommandLine != "" {
		t.Fatal("oversized facts retained")
	}
	paths := make([]string, maxProviderApprovalPaths+1)
	if f := cleanApprovalFacts(codexApprovalFacts{FilePaths: paths, Complete: true}); f.Complete {
		t.Fatal("too many paths retained")
	}
	s.clearTurn("turn")
	if len(s.approvalFacts) != 0 {
		t.Fatal("finished turn retains approval facts")
	}
}

func TestCodexApprovalMaskingKeepsExecutionVisibleOrMarksFactsIncomplete(t *testing.T) {
	const command = "password=$(printf codex-review-proof)"
	for _, shape := range []string{"command string", "legacy argv", "item cache"} {
		t.Run(shape, func(t *testing.T) {
			s := &codexSession{threadID: "thread", turnID: "turn"}
			method := codexReqCommandApproval
			params := map[string]any{"command": command}
			switch shape {
			case "legacy argv":
				method = codexReqLegacyExecApproval
				params["command"] = []string{"sh", "-c", command}
			case "item cache":
				item, err := json.Marshal(map[string]any{"threadId": "thread", "turnId": "turn", "item": map[string]any{"id": "command", "type": "commandExecution", "command": command}})
				if err != nil {
					t.Fatal(err)
				}
				s.observeApprovalItem(item)
				params = map[string]any{"itemId": "command"}
			}
			wire, err := json.Marshal(params)
			if err != nil {
				t.Fatal(err)
			}
			facts := s.approvalRequestFacts(method, wire)
			if facts.Complete {
				if !strings.Contains(facts.CommandLine, "$(printf codex-review-proof)") {
					t.Fatal("complete masked facts hide an executable substitution from the policy and reviewer")
				}
			} else if facts.CommandLine != "" && facts.CommandLine != "command not reviewable" {
				t.Fatal("unreviewable facts retain a partial command instead of a raw-free fact")
			}
		})
	}
}

func TestCodexApprovalEffectiveFactsStayBoundedAndOutOfSerializedEvidence(t *testing.T) {
	const command = "PASSWORD=literal-review-secret curl https://example.invalid"
	for _, shape := range []string{"modern", "legacy", "cache"} {
		t.Run(shape, func(t *testing.T) {
			s := &codexSession{threadID: "thread", turnID: "turn"}
			method := codexReqCommandApproval
			input := map[string]any{"command": command}
			if shape == "legacy" {
				method = codexReqLegacyExecApproval
				input["command"] = strings.Fields(command)
			}
			if shape == "cache" {
				params, _ := json.Marshal(map[string]any{"threadId": "thread", "turnId": "turn", "item": map[string]string{"id": "command", "type": "commandExecution", "command": command}})
				s.observeApprovalItem(params)
				input = map[string]any{"itemId": "command"}
			}
			data, _ := json.Marshal(input)
			f := s.approvalRequestFacts(method, data)
			if f.EffectiveCommandLine != command {
				t.Fatal("complete effective command was replaced by its display")
			}
			evidence, _ := json.Marshal(ProviderApprovalRequest{CommandLine: f.CommandLine, EffectiveCommandLine: f.EffectiveCommandLine, FactsComplete: f.Complete})
			if strings.Contains(string(evidence), "literal-review-secret") {
				t.Fatal("process-only original command reached serialized evidence")
			}
			s.clearTurn("turn")
			if len(s.approvalFacts) != 0 {
				t.Fatal("finished turn retained original facts")
			}
		})
	}
	s := &codexSession{threadID: "thread", turnID: "turn"}
	const path = "/project/olvs_abcdefgh extra-segment/file.go"
	data, _ := json.Marshal(map[string]any{"threadId": "thread", "turnId": "turn", "item": map[string]any{"id": "file", "type": "fileChange", "changes": []map[string]string{{"path": path}}}})
	s.observeApprovalItem(data)
	f := s.approvalRequestFacts(codexReqFileChangeApproval, json.RawMessage(`{"itemId":"file"}`))
	if len(f.EffectiveFilePaths) != 1 || f.EffectiveFilePaths[0] != path {
		t.Fatal("exact-value redaction lost part of the original path")
	}
	data, _ = json.Marshal(f)
	if strings.Contains(string(data), "olvs_abcdefgh") {
		t.Fatal("original path escaped through evidence serialization")
	}
	tooLong, _ := json.Marshal(map[string]string{"command": strings.Repeat("x", maxProviderApprovalFactBytes+1)})
	if f := s.approvalRequestFacts(codexReqCommandApproval, tooLong); f.Complete || f.EffectiveCommandLine != "" {
		t.Fatal("effective input widened the fact bound")
	}
}
