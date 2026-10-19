// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build unix

package sessions

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/store"
)

// Exercise the native stdout pump, bridge, attach ring and recorder. These
// replies contain a complete correlation id but an invalid envelope or an EOF
// prefix; no whole-envelope decoder can be the confidentiality gate.
func TestToolCommands_MalformedRepliesStayPrivate(t *testing.T) {
	for _, driver := range []string{providerDriverCodex, providerDriverClaude} {
		for _, variant := range []string{"bad envelope", "id last", "partial EOF", "null type", "missing type", "duplicate foreign first", "duplicate foreign last", "duplicate response", "case keys", "escaped id", "scalar before response", "array before response", "result with method", "other kind"} {
			claudeOnly := map[string]bool{"null type": true, "missing type": true, "duplicate response": true, "escaped id": true, "scalar before response": true, "array before response": true, "other kind": true}
			if (driver == providerDriverCodex && claudeOnly[variant]) || (driver == providerDriverClaude && variant == "result with method") {
				continue // each variant is an envelope of one driver's protocol
			}
			for _, late := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/late=%v", driver, variant, late), func(t *testing.T) {
					f := newPreviewFixture(t, store.Config{Engine: store.EngineSQLite, DSN: ":memory:"})
					lr, _ := f.m.rt.getLive(f.tenant, f.runRef)
					recorder := &capturingRecorder{}
					f.m.rt.Recorder = recorder
					lr.recordIO = true
					lr.finalizedCh = make(chan struct{})
					lr.context = t.Context()
					lr.cancel = func() {}
					response := `{"id":%s,"method":42,"result":{"path":"/private/skills/SKILL.md"}}`
					idPattern := `s/.*"id":\([0-9]*\).*/\1/p`
					if variant == "id last" {
						response = `{"method":42,"result":{"path":"/private/skills/SKILL.md"},"id":%s}`
					} else if variant == "partial EOF" {
						response = `{"id":%s,"result":{"data":[{"cwd":"/private/project","skills":[{"path":"/private/skills/SKILL.md"`
					}
					if driver == providerDriverClaude {
						idPattern = `s/.*"request_id":"\([^"]*\)".*/\1/p`
						response = `{"type":42,"response":{"request_id":"%s","account":"/private/account"}}`
						if variant == "id last" {
							response = `{"response":{"account":"/private/account","request_id":"%s"},"type":42}`
						} else if variant == "partial EOF" {
							response = `{"type":"control_response","response":{"request_id":"%s","response":{"account":"/private/account"`
						}
					}
					switch variant {
					case "null type":
						response = `{"type":null,"response":{"request_id":"%s","account":"/private/account"}}`
					case "missing type":
						response = `{"response":{"request_id":"%s","account":"/private/account"}}`
					case "duplicate foreign first":
						response = `{"id":999999,"id":%s,"result":{"path":"/private/skills/SKILL.md"}}`
						if driver == providerDriverClaude {
							response = `{"type":"control_response","response":{"request_id":"olv-commands-foreign","request_id":"%s","account":"/private/account"}}`
						}
					case "duplicate foreign last":
						response = `{"id":%s,"id":999999,"result":{"path":"/private/skills/SKILL.md"}}`
						if driver == providerDriverClaude {
							response = `{"type":"control_response","response":{"request_id":"%s","request_id":"olv-commands-foreign","account":"/private/account"}}`
						}
					case "duplicate response":
						response = `{"type":"control_response","response":{"request_id":"olv-commands-foreign"},"response":{"request_id":"%s","account":"/private/account"}}`
					case "scalar before response":
						response = `{"type":"control_response","response":"x","response":{"request_id":"%s","account":"/private/account"}}`
					case "array before response":
						response = `{"type":"control_response","response":[{"request_id":"olv-commands-foreign"}],"response":{"request_id":"%s","account":"/private/account"}}`
					case "result with method":
						response = `{"id":%s,"method":"item/tool/call","result":{"path":"/private/skills/SKILL.md"}}`
					case "other kind":
						response = `{"type":"assistant","response":{"request_id":"%s","account":"/private/account"}}`
					case "case keys":
						response = strings.ReplaceAll(strings.ReplaceAll(strings.ReplaceAll(response, `"id":`, `"ID":`), `"response":`, `"RESPONSE":`), `"request_id":`, `"REQUEST_ID":`)
					case "escaped id":
						response = strings.ReplaceAll(response, `"%s"`, `"\u006f%s"`)
					}
					// Two stdin reads form a deterministic barrier: cancel the caller
					// after its request was written, then release the native reply.
					// The reply is printed from %s arguments: no shell interprets its escapes.
					head, tail, _ := strings.Cut(response, "%s")
					script := "#!/bin/sh\nIFS= read -r line\nprintf '%s\\n' '{\"type\":\"system\",\"subtype\":\"ready\"}'\nid=$(printf '%s' \"$line\" | sed -n '" + idPattern + "')\nIFS= read -r release\nprintf '%s%s%s' '" + head + "' \"$id\" '" + tail + "'\n"
					if variant == "escaped id" {
						script = strings.ReplaceAll(script, `"$id"`, `"${id#o}"`)
					}
					if variant != "partial EOF" {
						script += "printf '\\n%s\\n' '{\"type\":\"system\",\"subtype\":\"marker\"}'\n"
					}
					proc, err := NewProcRunner().Launch(t.Context(), LaunchSpec{Program: writeOwnedChild(t, script), Isolation: IsolationNative, WaitDelay: time.Second})
					if err != nil {
						t.Fatal(err)
					}
					lr.proc = proc
					if driver == providerDriverCodex {
						lr.session = codexDriver{}.OpenSession(DriverSessionConfig{Send: proc.Send})
					}
					bridged := make(chan struct{})
					go func() { f.m.bridge(lr); close(bridged) }()
					t.Cleanup(func() {
						_ = proc.Stop(context.Background())
						select {
						case <-bridged:
						case <-time.After(3 * time.Second):
							t.Error("bridge did not end")
						}
					})
					ctx, cancel := context.WithCancel(t.Context())
					defer cancel()
					done := make(chan error, 1)
					go func() {
						var err error
						if s, ok := lr.session.(*codexSession); ok {
							_, _, err = s.commandCatalog(ctx)
						} else {
							_, err = f.m.askClaudeCommands(ctx, lr)
						}
						done <- err
					}()
					waitFor(t, "native child read the request", func() bool {
						for _, frame := range lr.ring.readFrom(0).frames {
							if bytes.Contains(frame.Data, []byte(`"ready"`)) {
								return true
							}
						}
						return false
					})
					if late {
						cancel()
						if err := <-done; !errors.Is(err, context.Canceled) {
							t.Fatalf("caller cancellation = %v", err)
						}
					}
					if err := proc.Send(t.Context(), []byte(`release`)); err != nil {
						t.Fatal(err)
					}
					select {
					case <-bridged:
					case <-time.After(3 * time.Second):
						t.Fatal("native EOF did not reach the bridge")
					}
					for _, frame := range lr.ring.readFrom(0).frames {
						if bytes.Contains(frame.Data, []byte("/private/")) {
							t.Error("catalog discovery data reached attach output")
						}
					}
					for _, frame := range recorder.frames {
						if bytes.Contains(frame.Data, []byte("/private/")) {
							t.Error("catalog discovery data reached recorded I/O")
						}
					}
					if !late {
						select {
						case err := <-done:
							if statusOf(err) != http.StatusBadGateway || strings.Contains(err.Error(), "/private/") {
								t.Errorf("catalog waiter = %v, want sanitized 502", err)
							}
						case <-time.After(time.Second):
							t.Error("malformed response did not fail the waiter")
						}
					}
					if variant != "partial EOF" && (len(lr.ring.readFrom(0).frames) != 2 || len(recorder.frames) != 2) {
						t.Error("ordinary marker did not reach both attach and recording")
					}
				})
			}
		}
	}
}

// A frame that is not the answer to an issued request keeps its way to the
// ring, and the request keeps waiting: the gate withholds by issued id only.
func TestToolCommands_UnrelatedFramesAreNotWithheld(t *testing.T) {
	t.Run("codex", func(t *testing.T) {
		s := codexDriver{}.OpenSession(DriverSessionConfig{Send: func(context.Context, []byte) error { return nil }}).(*codexSession)
		s.catalogRequests = map[string]struct{}{"7": {}}
		for name, frame := range map[string]string{
			// Server requests have their own id space: id 7 here is not our skills/list.
			"server request with our id": `{"id":7,"method":"item/commandExecution/requestApproval","params":{"command":"ls"}}`,
			"response to another id":     `{"id":8,"result":{"data":[]}}`,
			"notification":               `{"method":"skills/changed","params":{}}`,
			"not json":                   `starting codex`,
		} {
			if s.takeCatalogResponse([]byte(frame)) {
				t.Errorf("%s was withheld", name)
			}
		}
		if _, ok := s.catalogRequests["7"]; !ok {
			t.Fatal("an unrelated frame cleared the issued request")
		}
		if !s.takeCatalogResponse([]byte(`{"id":7,"result":{"data":[]}}`)) {
			t.Error("the answer to the issued request was not withheld")
		}
		if s.takeCatalogResponse([]byte(`{"id":7,"result":{"data":[]}}`)) {
			t.Error("a second answer to the same request was withheld")
		}
	})
	t.Run("claude", func(t *testing.T) {
		lr := &liveRun{}
		frame := func(id string) []byte {
			return []byte(`{"type":"control_response","response":{"request_id":"` + id + `","response":{"commands":[]}}}`)
		}
		if lr.takeCommandsResponse(frame("olv-commands-owned")) {
			t.Fatal("an answer was withheld with no request in flight")
		}
		answer := lr.commands.expect("olv-commands-owned")
		if lr.takeCommandsResponse(frame("olv-commands-foreign")) {
			t.Error("an id this engine never issued was withheld")
		}
		if lr.takeCommandsResponse([]byte(`{"type":"assistant","message":"olv-commands-owned"}`)) {
			t.Error("an id in text was taken for an answer")
		}
		select {
		case <-answer:
			t.Fatal("an unrelated frame answered the request")
		default:
		}
		if !lr.takeCommandsResponse(frame("olv-commands-owned")) {
			t.Fatal("the answer to the issued request was not withheld")
		}
		if got := <-answer; !bytes.Contains(got, []byte(`"commands":[]`)) {
			t.Errorf("waiter got %s, want the tool's own answer", got)
		}
	})
}
