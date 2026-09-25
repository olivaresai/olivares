// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"

	claudeapi "github.com/olivaresai/olivares/connectors/claude-api"
	"github.com/olivaresai/olivares/modules/inferenceproxy"
)

// This inspector matches only an exact synthetic fixture, not production detector
// policy. Authorize/Finalize must deliver the decoded channel for it to refuse.
type decodedFixtureInspector struct {
	text, kind, role string
	calls            int
}

func (f *decodedFixtureInspector) Inspect(_ context.Context, in claudeapi.ContentInspectionInput) claudeapi.ContentInspectionDecision {
	f.calls++
	for _, ch := range in.Channels {
		if ch.Scannable && ch.Text == f.text && ch.Kind == f.kind && ch.Role == f.role {
			return claudeapi.ContentInspectionDecision{
				Forward: false, Block: true, Status: http.StatusForbidden,
				ErrorType: "permission_error", Reason: "synthetic decoded fixture refused",
			}
		}
	}
	return claudeapi.ContentInspectionDecision{Forward: true}
}

func TestProxyInspectorDecodedOverDepthRemainsDenyClosed(t *testing.T) {
	a, mg, bg, kg, _ := allowAll()
	pol := dlpProxyPolicy(map[string]string{"*": "allow"})
	pol.pol.GateBudget = true
	d := newTestDecider(a, mg, bg, kg, pol)
	fake := &decodedFixtureInspector{text: "absent"}
	d.inspector = fake
	input := strings.Repeat("[", 7) + `"safe!"` + strings.Repeat("]", 7)
	b := blockFromJSONT(t, `{"type":"tool_use","name":"fixture","input":`+input+`}`)
	dec := d.Authorize(context.Background(), reqWithContent(b), "bearer")
	if dec.Allow || dec.Status != http.StatusForbidden || fake.calls != 0 || bg.calls != 0 {
		t.Fatal("unscanned argument depth did not stop before the inspector and budget")
	}
}

func TestProxyInspectorDecodedContentControlsRealCaller(t *testing.T) {
	const plain = "fixture-line-1\nfixture-line-2"
	for _, kind := range []string{claudeapi.ChannelToolUse, claudeapi.ChannelServerToolUse, claudeapi.ChannelToolResult} {
		for _, response := range []bool{false, true} {
			name := kind + "/request"
			if response {
				name = kind + "/response"
			}
			t.Run(name, func(t *testing.T) {
				wire := map[string]any{"type": kind, "name": "fixture-tool", "input": map[string]any{"nested": []any{map[string]any{"command": plain}}}}
				if kind == claudeapi.ChannelToolResult {
					wire = map[string]any{"type": kind, "content": base64.StdEncoding.EncodeToString([]byte(plain))}
				}
				raw, err := json.Marshal(wire)
				if err != nil {
					t.Fatal(err)
				}
				b := blockFromJSONT(t, string(raw))
				a, mg, bg, kg, pol := allowAll()
				d := newTestDecider(a, mg, bg, kg, pol)
				fake := &decodedFixtureInspector{text: plain, kind: kind, role: "user"}
				d.inspector = fake
				if response {
					fake.role = "assistant"
					sess := &proxySession{tenant: proxyTestTenant, modelRef: "claude-opus-4-8", pol: pol.pol}
					verdict := d.Finalize(context.Background(), sess, claudeapi.ProxyForwardResult{
						Response: claudeapi.MessageResponse{Content: []claudeapi.ContentBlock{b}}, UpstreamStatus: 200,
					})
					if !verdict.Block || verdict.Status != http.StatusForbidden {
						t.Fatal("decoded response never reached the withholding decision")
					}
				} else {
					verdict := d.Authorize(context.Background(), reqWithContent(b), "bearer")
					if verdict.Allow || verdict.Status != http.StatusForbidden {
						t.Fatal("decoded request never reached the refusal decision")
					}
					if bg.calls != 0 || !verdict.Prepared.IsZero() {
						t.Fatal("decoded refusal reached budget or produced an upstream artifact")
					}
				}
				if fake.calls != 1 {
					t.Fatalf("inspector calls = %d, want 1", fake.calls)
				}
			})
		}
	}
}

func TestProxyInspectorDecodedCompatibilityAndEarlierDeny(t *testing.T) {
	b := blockFromJSONT(t, `{"type":"tool_use","name":"fixture-tool","input":{"command":"benign first\nbenign second"}}`)
	for _, mode := range []string{"nil", "benign", "prior_deny"} {
		t.Run(mode, func(t *testing.T) {
			a, mg, bg, kg, pol := allowAll()
			d := newTestDecider(a, mg, bg, kg, pol)
			fake := &decodedFixtureInspector{text: "absent", kind: claudeapi.ChannelToolUse, role: "user"}
			if mode != "nil" {
				d.inspector = fake
			}
			if mode == "prior_deny" {
				mg.v.Allowed = false
			}
			req := reqWithContent(b)
			before, err := json.Marshal(req)
			if err != nil {
				t.Fatal(err)
			}
			dec := d.Authorize(context.Background(), req, "bearer")
			if mode == "prior_deny" {
				if dec.Allow || fake.calls != 0 || bg.calls != 0 {
					t.Fatal("an earlier deny reached the inspector or budget")
				}
				return
			}
			if !dec.Allow || bg.calls != 1 {
				t.Fatal("nil/benign inspector changed admission")
			}
			after, err := json.Marshal(req)
			if err != nil || string(before) != string(after) {
				t.Fatal("inspection rewrote the request")
			}
			var forwarded claudeapi.MessageRequest
			if err := json.Unmarshal(dec.Prepared.Body(), &forwarded); err != nil {
				t.Fatalf("missing prepared upstream request: %v", err)
			}
			if len(forwarded.Messages) != 1 || len(forwarded.Messages[0].Content) != 1 {
				t.Fatal("prepared request lost the original content block")
			}
			originalBlock, err := json.Marshal(b)
			if err != nil {
				t.Fatal(err)
			}
			forwardedBlock, err := json.Marshal(forwarded.Messages[0].Content[0])
			if err != nil || string(originalBlock) != string(forwardedBlock) {
				t.Fatal("inspection changed the prepared upstream content")
			}
		})
	}
}

// rawToolFixture builds a wire tool block with exact input bytes. It also returns the
// pre-existing name+input channel text the inspector and DLP must still receive.
func rawToolFixture(t *testing.T, kind, input string) (claudeapi.ContentBlock, string) {
	t.Helper()
	b := blockFromJSONT(t, `{"type":"`+kind+`","id":"fixture-id","name":"fixture-tool","input":`+input+`}`)
	return b, "fixture-tool " + input
}

func TestProxyInspectorDecodedRawFixtureSurvivesArgumentLimits(t *testing.T) {
	const secret = "AKIAIOSFODNN7EXAMPLE"
	tokens := "[" + strings.Repeat("0,", 270000) + "0]"
	for _, kind := range []string{claudeapi.ChannelToolUse, claudeapi.ChannelServerToolUse} {
		for _, limit := range []string{"over_depth", "exhausted"} {
			for _, policy := range []string{"inspector_only", "unscanned_allow"} {
				for _, response := range []bool{false, true} {
					name := kind + "/" + limit + "/" + policy + "/" + map[bool]string{false: "request", true: "response"}[response]
					t.Run(name, func(t *testing.T) {
						var blocks []claudeapi.ContentBlock
						// Over depth, the secret is below the argument bound; exhausted, a first
						// block of the other tool kind spends the 262,144-token added-work bound.
						// Only the raw channel carries the secret.
						input := `{"command":"` + secret + `"}`
						if limit == "over_depth" {
							input = `{"pad":` + strings.Repeat("[", 8) + `"` + secret + `"` + strings.Repeat("]", 8) + `}`
						} else {
							other := claudeapi.ChannelToolUse
							if kind == claudeapi.ChannelToolUse {
								other = claudeapi.ChannelServerToolUse
							}
							exhausting, _ := rawToolFixture(t, other, tokens)
							blocks = append(blocks, exhausting)
						}
						target, raw := rawToolFixture(t, kind, input)
						blocks = append(blocks, target)
						role := "user"
						if response {
							role = "assistant"
						}
						a, mg, bg, kg, pol := allowAll()
						if policy == "unscanned_allow" {
							// The seeded secret deny stays; only the unscanned class is allowed.
							pol = dlpProxyPolicy(map[string]string{"secret.credential": "deny", "*": "allow", "unscanned": "allow"})
							pol.pol.ResponseDLPMode = inferenceproxy.ResponseDLPBuffer
						}
						d := newTestDecider(a, mg, bg, kg, pol)
						var fake *decodedFixtureInspector
						if policy == "inspector_only" {
							fake = &decodedFixtureInspector{text: raw, kind: kind, role: role}
							d.inspector = fake
						}
						if response {
							sess := &proxySession{tenant: proxyTestTenant, modelRef: "claude-opus-4-8", pol: pol.pol}
							verdict := d.Finalize(context.Background(), sess, claudeapi.ProxyForwardResult{
								Response: claudeapi.MessageResponse{Content: blocks}, UpstreamStatus: 200,
							})
							if !verdict.Block {
								t.Fatal("the raw tool fixture was released after an argument limit")
							}
						} else {
							dec := d.Authorize(context.Background(), reqWithContent(blocks...), "bearer")
							if dec.Allow || dec.Status != http.StatusForbidden || bg.calls != 0 {
								t.Fatal("the raw tool fixture was admitted after an argument limit")
							}
						}
						if fake != nil && fake.calls != 1 {
							t.Fatalf("inspector calls = %d, want 1", fake.calls)
						}
					})
				}
			}
		}
	}
}

// contentToolFixture builds a tool_use block whose only argument is content.
func contentToolFixture(t *testing.T, content string) claudeapi.ContentBlock {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"type": "tool_use", "id": "fixture-id", "name": "fixture-tool", "input": map[string]any{"content": content},
	})
	if err != nil {
		t.Fatal(err)
	}
	return blockFromJSONT(t, string(raw))
}

func TestProxyInspectorDecodedBenignCapacityUnderStockDLP(t *testing.T) {
	const line = "benign fixture line\n"
	document := base64.StdEncoding.EncodeToString([]byte(strings.Repeat(line, 45000)))
	doc := blockFromJSONT(t, `{"type":"document","source":{"type":"base64","media_type":"text/plain","data":"`+document+`"}}`)
	tool := contentToolFixture(t, strings.Repeat(line, 30000))
	// A valid percent escape, or printable base64, in an ordinary 600,000-byte argument.
	percent := contentToolFixture(t, "benign%20fixture line\n"+strings.Repeat(line, 29999))
	encoded := contentToolFixture(t, base64.StdEncoding.EncodeToString([]byte(strings.Repeat(line, 22500))))
	// nil rules keep the seeded stock posture: secret and unscanned deny.
	stock := dlpProxyPolicy(nil)
	stock.pol.ResponseDLPMode = inferenceproxy.ResponseDLPBuffer
	for name, blocks := range map[string][]claudeapi.ContentBlock{
		"document_900000": {doc}, "argument_600000": {tool},
		"percent_argument_600000": {percent}, "base64_argument_600000": {encoded},
		// Two ordinary 600,000-byte arguments are resent history, not hostile repetition.
		"repeated_argument": {tool, tool},
	} {
		t.Run(name, func(t *testing.T) {
			a, mg, bg, kg, _ := allowAll()
			d := newTestDecider(a, mg, bg, kg, stock)
			dec := d.Authorize(context.Background(), reqWithContent(blocks...), "bearer")
			if !dec.Allow {
				t.Fatalf("benign %s refused under the stock posture: status=%d", name, dec.Status)
			}
			sess := &proxySession{tenant: proxyTestTenant, modelRef: "claude-opus-4-8", pol: stock.pol}
			verdict := d.Finalize(context.Background(), sess, claudeapi.ProxyForwardResult{
				Response: claudeapi.MessageResponse{Content: blocks}, UpstreamStatus: 200,
			})
			if verdict.Block {
				t.Fatalf("benign %s response withheld under the stock posture", name)
			}
		})
	}
}

func TestProxyInspectorDecodedURLChannelsRealCaller(t *testing.T) {
	const rawURL = "https://fixture.invalid/fixture-path?q=fixture+value"
	for _, kind := range []string{claudeapi.ChannelDocument, claudeapi.ChannelImage} {
		for _, match := range []bool{true, false} {
			t.Run(kind+map[bool]string{true: "/refused", false: "/benign"}[match], func(t *testing.T) {
				b := blockFromJSONT(t, `{"type":"`+kind+`","source":{"type":"url","url":"`+rawURL+`"}}`)
				a, mg, bg, kg, pol := allowAll()
				d := newTestDecider(a, mg, bg, kg, pol)
				fake := &decodedFixtureInspector{text: "absent", kind: kind, role: "user"}
				if match {
					fake.text = rawURL
				}
				d.inspector = fake
				dec := d.Authorize(context.Background(), reqWithContent(b), "bearer")
				if fake.calls != 1 {
					t.Fatalf("inspector calls = %d, want 1", fake.calls)
				}
				if match {
					if dec.Allow || dec.Status != http.StatusForbidden || bg.calls != 0 {
						t.Fatal("the original URL string never reached the refusal decision")
					}
					return
				}
				if !dec.Allow {
					t.Fatal("a benign inspector changed admission of a remote URL source")
				}
				var forwarded claudeapi.MessageRequest
				if err := json.Unmarshal(dec.Prepared.Body(), &forwarded); err != nil {
					t.Fatalf("missing prepared upstream request: %v", err)
				}
				original, err := json.Marshal(b)
				if err != nil || len(forwarded.Messages) != 1 || len(forwarded.Messages[0].Content) != 1 {
					t.Fatal("prepared request lost the original URL block")
				}
				got, err := json.Marshal(forwarded.Messages[0].Content[0])
				if err != nil || string(original) != string(got) {
					t.Fatal("inspection changed the prepared upstream URL block")
				}
			})
		}
	}
}

// agentHistoryRequest resends n earlier tool calls, each an assistant tool_use block
// answered by a user tool_result, the way a long agent session sends its conversation.
func agentHistoryRequest(t *testing.T, n int, name string, input func(i int) map[string]any) claudeapi.MessageRequest {
	t.Helper()
	req := claudeapi.MessageRequest{Model: "claude-opus-4-8", MaxTokens: 16}
	for i := range n {
		id := "fixture-call-" + strconv.Itoa(i)
		call, err := json.Marshal(map[string]any{"type": "tool_use", "id": id, "name": name, "input": input(i)})
		if err != nil {
			t.Fatal(err)
		}
		result, err := json.Marshal(map[string]any{"type": "tool_result", "tool_use_id": id, "content": "fixture result"})
		if err != nil {
			t.Fatal(err)
		}
		req.Messages = append(req.Messages,
			claudeapi.Message{Role: "assistant", Content: []claudeapi.ContentBlock{blockFromJSONT(t, string(call))}},
			claudeapi.Message{Role: "user", Content: []claudeapi.ContentBlock{blockFromJSONT(t, string(result))}})
	}
	return req
}

// A long agent session resends its whole history on every turn; ordinary history must
// not be refused under the stock posture, and the admitted request is forwarded unchanged.
func TestProxyInspectorDecodedLongHistoryUnderStockDLP(t *testing.T) {
	const line = "benign fixture line\n"
	for name, req := range map[string]claudeapi.MessageRequest{
		"edit_1200": agentHistoryRequest(t, 1200, "Edit", func(i int) map[string]any {
			return map[string]any{"file_path": "fixture." + strconv.Itoa(i) + ".txt",
				"old_string": strings.Repeat(line, 20), "new_string": strings.Repeat(line, 25)}
		}),
		"bash_2100": agentHistoryRequest(t, 2100, "Bash", func(i int) map[string]any {
			return map[string]any{"command": "echo benign fixture " + strconv.Itoa(i),
				"description": "Print a benign fixture"}
		}),
	} {
		t.Run(name, func(t *testing.T) {
			a, mg, bg, kg, _ := allowAll()
			stock := dlpProxyPolicy(nil)
			stock.pol.GateBudget = true
			d := newTestDecider(a, mg, bg, kg, stock)
			before, err := json.Marshal(req.Messages)
			if err != nil {
				t.Fatal(err)
			}
			dec := d.Authorize(context.Background(), req, "bearer")
			if !dec.Allow || bg.calls != 1 {
				t.Fatalf("resent ordinary history was refused under the stock posture: status=%d", dec.Status)
			}
			var forwarded claudeapi.MessageRequest
			if err := json.Unmarshal(dec.Prepared.Body(), &forwarded); err != nil {
				t.Fatalf("missing prepared upstream request: %v", err)
			}
			after, err := json.Marshal(forwarded.Messages)
			if err != nil || string(before) != string(after) {
				t.Fatal("inspection changed the prepared upstream history")
			}
		})
	}
}

// Exhausting only added work keeps what the pre-existing collector already provided: the
// DLP decoding of a later base64 text or text/plain document, and the inspector's view of
// the original text and the decoded document. Every refusal happens before any effect.
func TestProxyInspectorDecodedLegacyCoverageSurvivesAddedExhaustion(t *testing.T) {
	const textSecret, docSecret = "AKIAIOSFODNN7EXAMPLE", "AKIAI44QH8DHBEXAMPLE"
	exhausting, _ := rawToolFixture(t, claudeapi.ChannelToolUse, "["+strings.Repeat("0,", 270000)+"0]")
	encodedText := base64.StdEncoding.EncodeToString([]byte("deploy key " + textSecret))
	docText := "legacy document key " + docSecret
	doc := blockFromJSONT(t, `{"type":"document","source":{"type":"base64","media_type":"text/plain","data":"`+
		base64.StdEncoding.EncodeToString([]byte(docText))+`"}}`)
	for _, later := range []string{"text", "document"} {
		for _, policy := range []string{"stock", "unscanned_allow", "inspector_only"} {
			for _, response := range []bool{false, true} {
				name := later + "/" + policy + "/" + map[bool]string{false: "request", true: "response"}[response]
				t.Run(name, func(t *testing.T) {
					blocks := []claudeapi.ContentBlock{exhausting, claudeapi.TextBlock(encodedText)}
					kind, text := claudeapi.ChannelMessageText, encodedText
					if later == "document" {
						blocks = []claudeapi.ContentBlock{exhausting, doc}
						kind, text = claudeapi.ChannelDocument, docText
					}
					role := "user"
					if response {
						role = "assistant"
					}
					a, mg, bg, kg, pol := allowAll()
					switch policy {
					case "stock":
						pol = dlpProxyPolicy(nil)
					case "unscanned_allow":
						pol = dlpProxyPolicy(map[string]string{"secret.credential": "deny", "*": "allow", "unscanned": "allow"})
					}
					pol.pol.GateBudget = true
					pol.pol.ResponseDLPMode = inferenceproxy.ResponseDLPBuffer
					d := newTestDecider(a, mg, bg, kg, pol)
					var fake *decodedFixtureInspector
					if policy == "inspector_only" {
						// The channel the collector gave the inspector before decoded channels.
						fake = &decodedFixtureInspector{text: text, kind: kind, role: role}
						d.inspector = fake
					}
					if response {
						sess := &proxySession{tenant: proxyTestTenant, modelRef: "claude-opus-4-8", pol: pol.pol}
						verdict := d.Finalize(context.Background(), sess, claudeapi.ProxyForwardResult{
							Response: claudeapi.MessageResponse{Content: blocks}, UpstreamStatus: 200,
						})
						if !verdict.Block {
							t.Fatal("legacy coverage was lost after added work was exhausted: response released")
						}
					} else {
						dec := d.Authorize(context.Background(), reqWithContent(blocks...), "bearer")
						if dec.Allow || dec.Status != http.StatusForbidden || bg.calls != 0 || !dec.Prepared.IsZero() {
							t.Fatal("legacy coverage was lost after added work was exhausted: request admitted")
						}
					}
					if fake != nil && fake.calls != 1 {
						t.Fatalf("inspector calls = %d, want 1", fake.calls)
					}
				})
			}
		}
	}
}

// JSON \u escapes in tool arguments: the inspector receives the unescaped value, and an
// admitted request is forwarded with the original escaped bytes.
func TestProxyInspectorDecodedEscapedArgumentsForwardUnchanged(t *testing.T) {
	const input = `{"fixture\u002dkey":"fixture\u0020value"}`
	b, _ := rawToolFixture(t, claudeapi.ChannelToolUse, input)
	for _, match := range []bool{true, false} {
		t.Run(map[bool]string{true: "refused", false: "benign"}[match], func(t *testing.T) {
			a, mg, bg, kg, pol := allowAll()
			d := newTestDecider(a, mg, bg, kg, pol)
			fake := &decodedFixtureInspector{text: "absent", kind: claudeapi.ChannelToolUse, role: "user"}
			if match {
				fake.text = "fixture value"
			}
			d.inspector = fake
			dec := d.Authorize(context.Background(), reqWithContent(b), "bearer")
			if fake.calls != 1 {
				t.Fatalf("inspector calls = %d, want 1", fake.calls)
			}
			if match {
				if dec.Allow || dec.Status != http.StatusForbidden || bg.calls != 0 {
					t.Fatal("the unescaped argument never reached the refusal decision")
				}
				return
			}
			if !dec.Allow || !bytes.Contains(dec.Prepared.Body(), []byte(`fixture\u002dkey`)) {
				t.Fatal("the admitted request lost its original escaped argument bytes")
			}
			var forwarded claudeapi.MessageRequest
			if err := json.Unmarshal(dec.Prepared.Body(), &forwarded); err != nil {
				t.Fatalf("missing prepared upstream request: %v", err)
			}
			original, err := json.Marshal(b)
			if err != nil || len(forwarded.Messages) != 1 || len(forwarded.Messages[0].Content) != 1 {
				t.Fatal("prepared request lost the original tool block")
			}
			got, err := json.Marshal(forwarded.Messages[0].Content[0])
			if err != nil || string(original) != string(got) {
				t.Fatal("inspection changed the prepared upstream tool block")
			}
		})
	}
}
