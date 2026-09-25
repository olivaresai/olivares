// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claudeapi

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func requireCollectedChannel(t *testing.T, c CollectedContent, kind, role, text string) {
	t.Helper()
	for _, ch := range c.Channels {
		if ch.Scannable && ch.Kind == kind && ch.Role == role && ch.Text == text {
			return
		}
	}
	t.Fatalf("missing expected decoded channel: kind=%s role=%s", kind, role)
}

func decodedFixtureBlock(t *testing.T, kind string, input any) ContentBlock {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"type": kind, "name": "fixture-tool", "input": input})
	if err != nil {
		t.Fatal(err)
	}
	return blockFromJSON(t, string(raw))
}

func TestCollectDecodedToolArgumentsBothDirections(t *testing.T) {
	const command = "fixture-line-1\nfixture-line-2"
	for _, kind := range []string{ChannelToolUse, ChannelServerToolUse} {
		for _, request := range []bool{true, false} {
			name := kind + "/response"
			if request {
				name = kind + "/request"
			}
			t.Run(name, func(t *testing.T) {
				b := decodedFixtureBlock(t, kind, map[string]any{
					"z": "tail", "a": []any{"first", map[string]any{"command": command}},
				})
				before, err := json.Marshal(b)
				if err != nil {
					t.Fatal(err)
				}
				var c CollectedContent
				role := roleAssistant
				if request {
					c = CollectRequestContent(reqWithUserBlocks(b))
					role = roleUser
				} else {
					c = CollectResponseContent(MessageResponse{Content: []ContentBlock{b}})
				}
				if c.Unscanned {
					t.Fatal("bounded valid arguments became unscanned")
				}
				requireCollectedChannel(t, c, kind, role, command)
				var ordered []string
				var rawFound bool
				for _, ch := range c.Channels {
					if strings.HasPrefix(ch.Text, "fixture-tool {") {
						rawFound = ch.Kind == kind && ch.Role == role && strings.Contains(ch.Text, `\n`)
					}
					if ch.Text == "first" || ch.Text == command || ch.Text == "tail" {
						ordered = append(ordered, ch.Text)
					}
					if strings.Contains(ch.Ref, "fixture-line") || strings.Contains(ch.Ref, "command") {
						t.Fatal("decoded content or an input key leaked into Ref")
					}
				}
				if !rawFound || !reflect.DeepEqual(ordered, []string{"first", command, "tail"}) {
					t.Fatal("raw compatibility or deterministic nested value order was lost")
				}
				after, err := json.Marshal(b)
				if err != nil || string(before) != string(after) {
					t.Fatal("collection changed the original wire block")
				}
			})
		}
	}
}

func TestCollectDecodedVariantsReachChannels(t *testing.T) {
	const plain = "???~~~\nfixture-deny!"
	variants := map[string]string{
		"url":            url.QueryEscape(plain),
		"standard":       base64.StdEncoding.EncodeToString([]byte(plain)),
		"raw_standard":   base64.RawStdEncoding.EncodeToString([]byte(plain)),
		"url_base64":     base64.URLEncoding.EncodeToString([]byte(plain)),
		"raw_url_base64": base64.RawURLEncoding.EncodeToString([]byte(plain)),
		"embedded":       "before " + base64.StdEncoding.EncodeToString([]byte(plain)) + " after",
		"nested":         url.QueryEscape(base64.StdEncoding.EncodeToString([]byte(plain))),
	}
	for name, encoded := range variants {
		t.Run(name, func(t *testing.T) {
			c := CollectResponseContent(MessageResponse{Content: []ContentBlock{TextBlock(encoded)}})
			if c.Unscanned || !hasText(c, plain) {
				t.Fatal("bounded encoding lost its existing DLP result")
			}
			requireCollectedChannel(t, c, ChannelMessageText, roleAssistant, plain)
			for _, text := range c.Texts {
				requireCollectedChannel(t, c, ChannelMessageText, roleAssistant, text)
			}
		})
	}
}

func TestCollectDecodedVariantsRetainProvenanceInEitherOrder(t *testing.T) {
	const plain = "fixture-line-one\nfixture-line-two!"
	encoded := base64.StdEncoding.EncodeToString([]byte(url.QueryEscape(plain)))
	tr, err := json.Marshal(map[string]any{"type": "tool_result", "content": encoded})
	if err != nil {
		t.Fatal(err)
	}
	for _, systemFirst := range []bool{true, false} {
		t.Run(map[bool]string{true: "system_first", false: "tool_first"}[systemFirst], func(t *testing.T) {
			tool := Message{Role: roleUser, Content: []ContentBlock{blockFromJSON(t, string(tr))}}
			req := MessageRequest{System: []ContentBlock{TextBlock(encoded)}, Messages: []Message{tool}}
			if !systemFirst {
				// The connector also supports mid-conversation system messages.
				req.System = nil
				req.Messages = []Message{tool, {Role: roleSystem, Content: []ContentBlock{TextBlock(encoded)}}}
			}
			c := CollectRequestContent(req)
			for _, text := range []string{encoded, url.QueryEscape(plain), plain} {
				requireCollectedChannel(t, c, ChannelSystemText, roleSystem, text)
				requireCollectedChannel(t, c, ChannelToolResult, roleUser, text)
			}
			count := 0
			for _, text := range c.Texts {
				if text == plain {
					count++
				}
			}
			if c.Unscanned || count != 1 {
				t.Fatal("channel provenance must not change DLP text deduplication")
			}
		})
	}
}

func TestCollectDecodedSourceVariantsReachChannels(t *testing.T) {
	const plain = "fixture-line-one\nfixture-line-two!"
	encoded := base64.StdEncoding.EncodeToString([]byte(plain))
	for _, source := range []struct{ kind, media string }{
		{ChannelDocument, "text/plain"}, {ChannelDocument, "application/pdf"}, {ChannelImage, "image/png"},
	} {
		t.Run(source.kind+"/"+source.media, func(t *testing.T) {
			b := blockFromJSON(t, `{"type":"`+source.kind+`","source":{"type":"base64","media_type":"`+source.media+`","data":"`+encoded+`"}}`)
			c := CollectRequestContent(reqWithUserBlocks(b))
			requireCollectedChannel(t, c, source.kind, roleUser, plain)
			if c.Unscanned != (source.media != "text/plain") {
				t.Fatal("decoded bytes changed the binary-source disposition")
			}
		})
	}
	for _, kind := range []string{ChannelDocument, ChannelImage} {
		b := blockFromJSON(t, `{"type":"`+kind+`","source":{"type":"url","url":"https://fixture.invalid/api%5Fkey%3Dfixture"}}`)
		c := CollectRequestContent(reqWithUserBlocks(b))
		requireCollectedChannel(t, c, kind, roleUser, "https://fixture.invalid/api_key=fixture")
		if !c.Unscanned {
			t.Fatal("remote content must remain unscanned")
		}
	}
}

func TestCollectDecodedArgumentBoundsAndMalformedInput(t *testing.T) {
	for name, input := range map[string]string{
		"malformed": `{"command":`,
		"trailing":  `{} {}`,
		"deep":      strings.Repeat("[", 2000) + `"end"` + strings.Repeat("]", 2000),
		// A percent-encoded text above the 1 MiB per-item guard is never decoded.
		"oversized": `{"command":"%41` + strings.Repeat("x", maxDecodedContentSize) + `"}`,
	} {
		t.Run(name, func(t *testing.T) {
			b := ContentBlock{Type: ChannelToolUse, raw: json.RawMessage(`{"type":"tool_use","name":"fixture","input":` + input + `}`)}
			c := CollectResponseContent(MessageResponse{Content: []ContentBlock{b}})
			if !c.Unscanned {
				t.Fatal("invalid or excessive argument work was silently accepted")
			}
		})
	}
}

func TestCollectDecodedArgumentDepthBoundaryAndQuotedBrackets(t *testing.T) {
	for _, depth := range []int{maxContentDepth, maxContentDepth + 1} {
		input := strings.Repeat("[", depth) + `"safe!"` + strings.Repeat("]", depth)
		b := decodedFixtureBlock(t, ChannelToolUse, json.RawMessage(input))
		c := CollectResponseContent(MessageResponse{Content: []ContentBlock{b}})
		if c.Unscanned != (depth > maxContentDepth) {
			t.Fatalf("argument depth %d: unexpected unscanned disposition", depth)
		}
		if depth == maxContentDepth {
			requireCollectedChannel(t, c, ChannelToolUse, roleAssistant, "safe!")
		}
	}
	value := strings.Repeat("[", 64) + `\"quoted\"` + strings.Repeat("]", 64)
	b := decodedFixtureBlock(t, ChannelToolUse, map[string]any{"value": value})
	c := CollectResponseContent(MessageResponse{Content: []ContentBlock{b}})
	if c.Unscanned {
		t.Fatal("brackets inside an escaped string were treated as JSON nesting")
	}
	requireCollectedChannel(t, c, ChannelToolUse, roleAssistant, value)
}

func TestCollectDecodedArgumentVariantsAndMalformedEncoding(t *testing.T) {
	const plain = "fixture-first\nfixture-second!"
	encoded := base64.StdEncoding.EncodeToString([]byte(url.QueryEscape(plain)))
	b := decodedFixtureBlock(t, ChannelServerToolUse, map[string]any{"nested": []any{encoded}})
	c := CollectResponseContent(MessageResponse{Content: []ContentBlock{b}})
	for _, text := range []string{encoded, url.QueryEscape(plain), plain} {
		requireCollectedChannel(t, c, ChannelServerToolUse, roleAssistant, text)
	}
	for _, value := range []string{"%41%GG", "////////////"} {
		c := CollectRequestContent(reqWithUserBlocks(TextBlock(value)))
		if !c.Unscanned {
			t.Fatal("explicit malformed or binary encoding lost its opaque marker")
		}
	}
}

func TestCollectDecodedAggregateChargesRepeatedWork(t *testing.T) {
	for _, encoded := range []bool{false, true} {
		t.Run(map[bool]string{false: "arguments", true: "variants"}[encoded], func(t *testing.T) {
			// The classifier sees one distinct text, but every occurrence is an expansion
			// channel: 4,200 blocks of 16 argument strings, or 66,000 decodings of one
			// encoded text, exceed the 65,536 expansion channels.
			arguments := make([]string, 16)
			for i := range arguments {
				arguments[i] = "x x"
			}
			b, repeats := decodedFixtureBlock(t, ChannelToolUse, arguments), 4200
			if encoded {
				b, repeats = TextBlock(base64.StdEncoding.EncodeToString([]byte("x x x x x x x x"))), 66000
			}
			blocks := make([]ContentBlock, repeats)
			for i := range blocks {
				blocks[i] = b
			}
			c := CollectRequestContent(reqWithUserBlocks(blocks...))
			if !c.Unscanned || !anyChannelRef(c, "decoded/channel-limit") {
				t.Fatal("global text deduplication made repeated decode work free")
			}
		})
	}
	// Empty containers still consume token work even though they yield no strings.
	b := decodedFixtureBlock(t, ChannelToolUse, json.RawMessage("["+strings.Repeat("[],", 200000)+"[]]"))
	c := CollectResponseContent(MessageResponse{Content: []ContentBlock{b}})
	if !c.Unscanned || !anyChannelRef(c, "arguments/token-limit") {
		t.Fatal("wide empty structures bypassed the argument token bound")
	}
}

// The fixed dispositions of the four independent derived-work limits.
var decodedLimitRefs = []string{
	"decoded/input-limit", "decoded/output-limit", "arguments/token-limit", "decoded/channel-limit",
}

func anyChannelRef(c CollectedContent, ref string) bool {
	for _, ch := range c.Channels {
		if ch.Ref == ref {
			return true
		}
	}
	return false
}

func requireOpaqueRef(t *testing.T, c CollectedContent, kind, role, ref string) {
	t.Helper()
	for _, ch := range c.Channels {
		if !ch.Scannable && ch.Kind == kind && ch.Role == role && ch.Ref == ref {
			return
		}
	}
	t.Fatalf("missing opaque disposition %s: kind=%s role=%s", ref, kind, role)
}

// rawToolBlock builds a wire tool block with exact input bytes. It also returns the
// pre-existing name+input channel text, which no argument limit may remove.
func rawToolBlock(t *testing.T, kind, input string) (ContentBlock, string) {
	t.Helper()
	b := blockFromJSON(t, `{"type":"`+kind+`","id":"fixture-id","name":"fixture-tool","input":`+input+`}`)
	return b, "fixture-tool " + input
}

func TestCollectDecodedRawToolChannelSurvivesArgumentLimits(t *testing.T) {
	const secret = "AKIAIOSFODNN7EXAMPLE"
	// The secret sits below the argument depth bound, so only the raw channel carries it.
	overDepth := `{"pad":` + strings.Repeat("[", 8) + `"` + secret + `"` + strings.Repeat("]", 8) + `}`
	tokens := "[" + strings.Repeat("0,", 270000) + "0]"
	for _, kind := range []string{ChannelToolUse, ChannelServerToolUse} {
		for _, request := range []bool{true, false} {
			for _, limit := range []string{"over_depth", "exhausted"} {
				name := kind + "/" + map[bool]string{true: "request", false: "response"}[request] + "/" + limit
				t.Run(name, func(t *testing.T) {
					var blocks []ContentBlock
					input, wantRef := `{"command":"`+secret+`"}`, "arguments/token-limit"
					if limit == "over_depth" {
						input, wantRef = overDepth, "arguments/max-depth"
					} else {
						// 270,000 number tokens of the other tool kind spend the 262,144-token
						// bound first, so the disposition asserted below is the target's own.
						other := ChannelToolUse
						if kind == ChannelToolUse {
							other = ChannelServerToolUse
						}
						exhausting, _ := rawToolBlock(t, other, tokens)
						blocks = append(blocks, exhausting)
					}
					target, raw := rawToolBlock(t, kind, input)
					blocks = append(blocks, target)
					c, role := CollectRequestContent(reqWithUserBlocks(blocks...)), roleUser
					if !request {
						c, role = CollectResponseContent(MessageResponse{Content: blocks}), roleAssistant
					}
					requireCollectedChannel(t, c, kind, role, raw)
					if !hasText(c, secret) {
						t.Fatal("an argument limit removed the raw tool text from the DLP input")
					}
					if !c.Unscanned {
						t.Fatal("incomplete argument collection was not marked unscanned")
					}
					requireOpaqueRef(t, c, kind, role, wantRef)
				})
			}
		}
	}
}

func TestCollectDecodedBenignCapacityStaysScannable(t *testing.T) {
	document := strings.Repeat("benign fixture line\n", 45000) // 900,000 decoded bytes
	data := base64.StdEncoding.EncodeToString([]byte(document))
	doc := blockFromJSON(t, `{"type":"document","source":{"type":"base64","media_type":"text/plain","data":"`+data+`"}}`)
	c := CollectRequestContent(reqWithUserBlocks(doc))
	if c.Unscanned {
		t.Fatal("a 900,000-byte text/plain base64 document became unscanned")
	}
	requireCollectedChannel(t, c, ChannelDocument, roleUser, document)

	argument := strings.Repeat("benign fixture line\n", 30000) // 600,000 bytes
	b := decodedFixtureBlock(t, ChannelToolUse, map[string]any{"content": argument})
	for _, request := range []bool{true, false} {
		c, role := CollectRequestContent(reqWithUserBlocks(b)), roleUser
		if !request {
			c, role = CollectResponseContent(MessageResponse{Content: []ContentBlock{b}}), roleAssistant
		}
		if c.Unscanned {
			t.Fatal("a 600,000-byte ordinary tool argument became unscanned")
		}
		requireCollectedChannel(t, c, ChannelToolUse, role, argument)
	}
}

// Ordinary 600,000-byte arguments carrying a valid percent escape, or printable base64,
// stay scannable as they were before decoded channels: the legacy decoding of the raw tool
// text and the added decoding of the argument string are bounded separately.
func TestCollectDecodedEncodedArgumentsKeepLegacyCapacity(t *testing.T) {
	const line = "benign fixture line\n"
	plain := strings.Repeat(line, 22500) // 450,000 bytes, 600,000 once encoded
	for name, tc := range map[string]struct{ argument, decoded string }{
		"percent": {"benign%20fixture line\n" + strings.Repeat(line, 29999), strings.Repeat(line, 30000)},
		"base64":  {base64.StdEncoding.EncodeToString([]byte(plain)), plain},
	} {
		b := decodedFixtureBlock(t, ChannelToolUse, map[string]any{"content": tc.argument})
		for _, request := range []bool{true, false} {
			t.Run(name+map[bool]string{true: "/request", false: "/response"}[request], func(t *testing.T) {
				c, role := CollectRequestContent(reqWithUserBlocks(b)), roleUser
				if !request {
					c, role = CollectResponseContent(MessageResponse{Content: []ContentBlock{b}}), roleAssistant
				}
				if c.Unscanned {
					t.Fatal("an ordinary encoded 600,000-byte argument became unscanned")
				}
				requireCollectedChannel(t, c, ChannelToolUse, role, tc.argument)
				requireCollectedChannel(t, c, ChannelToolUse, role, tc.decoded)
				if !hasText(c, tc.decoded) {
					t.Fatal("the decoded argument did not reach the DLP input")
				}
			})
		}
	}
}

// agentHistory resends n earlier tool calls, each an assistant tool_use block answered by
// a user tool_result, the way a long agent session sends its whole conversation.
func agentHistory(t *testing.T, n int, name string, input func(i int) map[string]any) MessageRequest {
	t.Helper()
	req := MessageRequest{Model: "claude-opus-4-8"}
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
			Message{Role: roleAssistant, Content: []ContentBlock{blockFromJSON(t, string(call))}},
			Message{Role: roleUser, Content: []ContentBlock{blockFromJSON(t, string(result))}})
	}
	return req
}

// Every request resends the whole conversation, so ordinary long agent sessions must
// stay scannable while their encoded request fits the transport limit.
func TestCollectDecodedLongHistoryStaysScannable(t *testing.T) {
	const line = "benign fixture line\n"
	large := strings.Repeat(line, 30000)
	for name, tc := range map[string]struct {
		req            MessageRequest
		oldest, newest string
	}{
		"edit_1200": {agentHistory(t, 1200, "Edit", func(i int) map[string]any {
			return map[string]any{"file_path": "fixture." + strconv.Itoa(i) + ".txt",
				"old_string": strings.Repeat(line, 20), "new_string": strings.Repeat(line, 25)}
		}), "fixture.0.txt", "fixture.1199.txt"},
		"bash_2100": {agentHistory(t, 2100, "Bash", func(i int) map[string]any {
			return map[string]any{"command": "echo benign fixture " + strconv.Itoa(i),
				"description": "Print a benign fixture"}
		}), "echo benign fixture 0", "echo benign fixture 2099"},
		"two_600000_byte_arguments": {agentHistory(t, 2, "Write", func(int) map[string]any {
			return map[string]any{"content": large}
		}), large, large},
	} {
		t.Run(name, func(t *testing.T) {
			wire, err := json.Marshal(tc.req)
			if err != nil || len(wire) >= maxProxyRequestBody {
				t.Fatalf("the history fixture must fit the transport limit: %d bytes", len(wire))
			}
			c := CollectRequestContent(tc.req)
			for _, ref := range decodedLimitRefs {
				if anyChannelRef(c, ref) {
					t.Fatalf("resent ordinary tool history reached %s", ref)
				}
			}
			if c.Unscanned {
				t.Fatal("resent ordinary tool history became unscanned")
			}
			requireCollectedChannel(t, c, ChannelToolUse, roleAssistant, tc.oldest)
			requireCollectedChannel(t, c, ChannelToolUse, roleAssistant, tc.newest)
		})
	}
}

// Exhausting only added work must leave the pre-existing decoding intact: a later
// base64 text is still decoded for DLP and a later text/plain base64 document still
// yields its decoded channel. Each later source that lost expansion is marked once.
func TestCollectDecodedLegacyCoverageSurvivesAddedExhaustion(t *testing.T) {
	const textSecret, docSecret = "AKIAIOSFODNN7EXAMPLE", "AKIAI44QH8DHBEXAMPLE"
	// 270,000 number tokens exhaust the token bound; nothing in them is legacy content.
	exhausting, _ := rawToolBlock(t, ChannelToolUse, "["+strings.Repeat("0,", 270000)+"0]")
	encodedText := func(s string) ContentBlock {
		return TextBlock(base64.StdEncoding.EncodeToString([]byte(s)))
	}
	docText := "legacy document key " + docSecret
	doc := blockFromJSON(t, `{"type":"document","source":{"type":"base64","media_type":"text/plain","data":"`+
		base64.StdEncoding.EncodeToString([]byte(docText))+`"}}`)
	blocks := []ContentBlock{exhausting, encodedText("deploy key " + textSecret), encodedText("later benign text"), doc}
	for _, request := range []bool{true, false} {
		t.Run(map[bool]string{true: "request", false: "response"}[request], func(t *testing.T) {
			c, role := CollectRequestContent(reqWithUserBlocks(blocks...)), roleUser
			if !request {
				c, role = CollectResponseContent(MessageResponse{Content: blocks}), roleAssistant
			}
			if !c.Unscanned {
				t.Fatal("incomplete added work was not marked unscanned")
			}
			if !hasText(c, textSecret) || !hasText(c, "later benign text") {
				t.Fatal("added-work exhaustion stopped the legacy decoding of a later text")
			}
			requireCollectedChannel(t, c, ChannelDocument, role, docText)
			if !hasText(c, docSecret) {
				t.Fatal("added-work exhaustion stopped the legacy text/plain document decoding")
			}
			overflow := 0
			for _, ch := range c.Channels {
				if !ch.Scannable && ch.Kind == ChannelMessageText && ch.Role == role && ch.Ref == "arguments/token-limit" {
					overflow++
				}
			}
			if overflow != 2 {
				t.Fatalf("later text sources carry %d overflow dispositions, want one each (2)", overflow)
			}
		})
	}
}

// The pre-existing decoding of ordinary text is not added work: 40 MiB of text that the
// legacy decoder attempts (a URL-alphabet run that decodes to invalid UTF-8) stays
// scannable, as it did before decoded channels existed.
func TestCollectDecodedLegacyDecodingIsNotChargedAsAddedWork(t *testing.T) {
	opaqueRun := "-" + strings.Repeat("A", 1<<20-1)
	c := CollectRequestContent(reqWithUserBlocks(repeatedBlocks(TextBlock(opaqueRun), 40)...))
	for _, ref := range decodedLimitRefs {
		if anyChannelRef(c, ref) {
			t.Fatalf("legacy text decoding was charged to added work: %s", ref)
		}
	}
	if c.Unscanned {
		t.Fatal("legacy text decoding became unscanned")
	}
	requireCollectedChannel(t, c, ChannelMessageText, roleUser, opaqueRun)
}

// jsonEscaped writes every rune of s as a JSON \u escape, so the wire bytes of an
// argument hold no base64 run for the pre-existing decoder to find.
func jsonEscaped(s string) string {
	var b strings.Builder
	for _, r := range s {
		fmt.Fprintf(&b, `\u%04x`, r)
	}
	return b.String()
}

// Added work that classifies a decoding first must not stop the pre-existing decoding of
// the same text later. An argument string decodes to a first layer and the expansion
// bound stops before its second layer; a later text with the same encoding still reaches
// the second layer through the legacy decoder.
func TestCollectDecodedAddedTextsDoNotSuppressLegacyDecoding(t *testing.T) {
	const secret = "AKIAIOSFODNN7EXAMPLE"
	layer2 := "legacy layer key " + secret
	layer1 := base64.StdEncoding.EncodeToString([]byte(layer2))
	encoded := base64.StdEncoding.EncodeToString([]byte(layer1))
	// 65,534 strings leave room for exactly two expansion channels: the argument string
	// and its first decoding.
	filler, _ := rawToolBlock(t, ChannelServerToolUse, "["+strings.Repeat(`"a",`, 65533)+`"a"]`)
	argument, _ := rawToolBlock(t, ChannelToolUse, `"`+jsonEscaped(encoded)+`"`)
	c := CollectRequestContent(reqWithUserBlocks(filler, argument, TextBlock(encoded)))
	if !hasText(c, secret) {
		t.Fatal("added work that classified a decoding first stopped its legacy decoding")
	}
	// The scenario itself: added work recorded the first layer and stopped before the second.
	requireCollectedChannel(t, c, ChannelToolUse, roleUser, layer1)
	for _, ch := range c.Channels {
		if ch.Kind == ChannelToolUse && ch.Text == layer2 {
			t.Fatal("added work reached the second layer; the fixture no longer isolates the legacy decoder")
		}
	}
	requireOpaqueRef(t, c, ChannelToolUse, roleUser, "decoded/channel-limit")
}

func repeatedBlocks(b ContentBlock, n int) []ContentBlock {
	blocks := make([]ContentBlock, n)
	for i := range blocks {
		blocks[i] = b
	}
	return blocks
}

// Each added-work bound is reached alone just above it and not at all just below it:
// 32 MiB of expansion output, 64 MiB of added parser and decoder input, 262,144 JSON
// tokens and 65,536 expansion channels. The output and input shapes are direct collector
// fixtures larger than the 16 MiB HTTP request body, a separately bounded surface.
func TestCollectDecodedWorkLimitsFireIndependently(t *testing.T) {
	// 512,000 decoded bytes per occurrence, which the legacy 1 MiB bound still admits when
	// repeated: 64 occurrences fit, and a 2 MiB argument string on top of them exceeds the
	// output bound that decodings and arguments share.
	decoded := strings.Repeat("benign fixture line\n", 25600)
	encoded := TextBlock(base64.StdEncoding.EncodeToString([]byte(decoded)))
	outputBlock, outputRaw := rawToolBlock(t, ChannelToolUse, `{"a":"`+strings.Repeat("x ", 1<<20)+`"}`)
	// A 1 MiB member outside input is parsed and never exposed: it spends only input.
	inputBlock := blockFromJSON(t, `{"type":"tool_use","id":"`+strings.Repeat("x", 1<<20)+
		`","name":"fixture-tool","input":{}}`)
	tokenBlock := func(n int) (ContentBlock, string) {
		return rawToolBlock(t, ChannelToolUse, "["+strings.Repeat("0,", n-1)+"0]")
	}
	channelBlock := func(n int) (ContentBlock, string) {
		return rawToolBlock(t, ChannelServerToolUse, "["+strings.Repeat(`"a",`, n-1)+`"a"]`)
	}
	tokensBelow, tokensBelowRaw := tokenBlock(250000)
	tokensAbove, tokensAboveRaw := tokenBlock(270000)
	channelsBelow, channelsBelowRaw := channelBlock(65000)
	channelsAbove, channelsAboveRaw := channelBlock(66000)
	for _, tc := range []struct {
		name, ref, kind, raw string
		blocks               []ContentBlock
	}{
		{"output/below", "", ChannelMessageText, encoded.Text, repeatedBlocks(encoded, 64)},
		{"output/above", "decoded/output-limit", ChannelToolUse, outputRaw,
			append(repeatedBlocks(encoded, 64), outputBlock)},
		{"input/below", "", ChannelToolUse, "fixture-tool {}", repeatedBlocks(inputBlock, 62)},
		{"input/above", "decoded/input-limit", ChannelToolUse, "fixture-tool {}", repeatedBlocks(inputBlock, 66)},
		{"tokens/below", "", ChannelToolUse, tokensBelowRaw, []ContentBlock{tokensBelow}},
		{"tokens/above", "arguments/token-limit", ChannelToolUse, tokensAboveRaw, []ContentBlock{tokensAbove}},
		{"channels/below", "", ChannelServerToolUse, channelsBelowRaw, []ContentBlock{channelsBelow}},
		{"channels/above", "decoded/channel-limit", ChannelServerToolUse, channelsAboveRaw,
			[]ContentBlock{channelsAbove}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := CollectRequestContent(reqWithUserBlocks(tc.blocks...))
			requireCollectedChannel(t, c, tc.kind, roleUser, tc.raw)
			for _, other := range decodedLimitRefs {
				if other != tc.ref && anyChannelRef(c, other) {
					t.Fatalf("%s fired for the %s fixture", other, tc.name)
				}
			}
			if tc.ref == "" {
				if c.Unscanned {
					t.Fatal("work below every added-work bound became unscanned")
				}
				return
			}
			if !c.Unscanned {
				t.Fatal("exhausted added work was not marked unscanned")
			}
			requireOpaqueRef(t, c, tc.kind, roleUser, tc.ref)
		})
	}
}

func countOpaqueRef(c CollectedContent, kind, role, ref string) int {
	n := 0
	for _, ch := range c.Channels {
		if !ch.Scannable && ch.Kind == kind && ch.Role == role && ch.Ref == ref {
			n++
		}
	}
	return n
}

// An opaque reason is recorded once per original source, however many of its strings or
// candidates produce it; distinct sources keep their own, even with identical text.
func TestCollectDecodedOpaqueMarkersArePerSource(t *testing.T) {
	const reason = "encoded/undecodable-or-oversized"
	// 10,000 explicit-alphabet candidates that decode to no printable text.
	invalid := strings.Repeat("//////////// ", 10000)
	c := CollectRequestContent(reqWithUserBlocks(TextBlock(invalid)))
	if n := countOpaqueRef(c, ChannelMessageText, roleUser, reason); !c.Unscanned || n != 1 {
		t.Fatalf("one text source carries %d opaque markers, want 1", n)
	}
	c = CollectRequestContent(MessageRequest{Messages: []Message{
		{Role: roleUser, Content: []ContentBlock{TextBlock(invalid)}},
		{Role: roleUser, Content: []ContentBlock{TextBlock(invalid)}},
	}})
	if n := countOpaqueRef(c, ChannelMessageText, roleUser, reason); n != 2 {
		t.Fatalf("two messages with the same text carry %d opaque markers, want 2", n)
	}
	// The raw tool text and its decoded argument string belong to one source.
	b, _ := rawToolBlock(t, ChannelToolUse, `{"a":"`+invalid+`"}`)
	c = CollectResponseContent(MessageResponse{Content: []ContentBlock{b}})
	if n := countOpaqueRef(c, ChannelToolUse, roleAssistant, reason); n != 1 {
		t.Fatalf("one tool call carries %d opaque markers, want 1", n)
	}
}

func TestCollectDecodedArgumentKeysAndRepeatedValues(t *testing.T) {
	const input = `{"outer":{"dupkey":"first-dup-value","dupkey":"second-dup-value"},"escaped\nkey!":"v"}`
	want := []string{"first-dup-value", "second-dup-value", "escaped\nkey!"}
	for _, kind := range []string{ChannelToolUse, ChannelServerToolUse} {
		t.Run(kind, func(t *testing.T) {
			b, raw := rawToolBlock(t, kind, input)
			before, err := json.Marshal(b)
			if err != nil {
				t.Fatal(err)
			}
			c := CollectResponseContent(MessageResponse{Content: []ContentBlock{b}})
			if c.Unscanned {
				t.Fatal("fully enumerated nested duplicates and escaped keys became unscanned")
			}
			requireCollectedChannel(t, c, kind, roleAssistant, raw)
			var ordered []string
			for _, ch := range c.Channels {
				if ch.Kind == kind && ch.Role == roleAssistant && ch.Scannable &&
					(ch.Text == want[0] || ch.Text == want[1] || ch.Text == want[2]) {
					ordered = append(ordered, ch.Text)
				}
				if strings.Contains(ch.Ref, "dup") || strings.Contains(ch.Ref, "escaped") || strings.Contains(ch.Ref, "outer") {
					t.Fatal("a decoded key or value leaked into Ref")
				}
			}
			if !reflect.DeepEqual(ordered, want) {
				t.Fatal("repeated values or escaped keys were not exposed in document order")
			}
			if !hasText(c, "first-dup-value") || !hasText(c, want[2]) {
				t.Fatal("decoded keys and repeated values did not reach the DLP input")
			}
			after, err := json.Marshal(b)
			if err != nil || string(before) != string(after) {
				t.Fatal("collection changed the original wire block")
			}
		})
	}
}

// JSON \u escapes in the wire bytes: the raw channel keeps them, and the decoded key and
// value, which only exist after unescaping, reach Channels and the DLP input.
func TestCollectDecodedUnicodeEscapedKeyAndValue(t *testing.T) {
	const secret = "AKIAIOSFODNN7EXAMPLE"
	const input = `{"fixture\u002dkey":"AKIA\u0049OSFODNN7EXAMPLE"}`
	for _, kind := range []string{ChannelToolUse, ChannelServerToolUse} {
		t.Run(kind, func(t *testing.T) {
			b, raw := rawToolBlock(t, kind, input)
			if strings.Contains(raw, secret) || !strings.Contains(raw, `\u0049`) {
				t.Fatal("the fixture must carry the secret only in JSON-escaped form")
			}
			before, err := json.Marshal(b)
			if err != nil {
				t.Fatal(err)
			}
			c := CollectRequestContent(reqWithUserBlocks(b))
			requireCollectedChannel(t, c, kind, roleUser, raw)
			requireCollectedChannel(t, c, kind, roleUser, "fixture-key")
			requireCollectedChannel(t, c, kind, roleUser, secret)
			if !hasText(c, secret) {
				t.Fatal("the unescaped value did not reach the DLP input")
			}
			for _, ch := range c.Channels {
				if strings.Contains(ch.Ref, "fixture-key") || strings.Contains(ch.Ref, "AKIA") {
					t.Fatal("a decoded key or value leaked into Ref")
				}
			}
			after, err := json.Marshal(b)
			if err != nil || string(before) != string(after) {
				t.Fatal("collection changed the original wire block")
			}
		})
	}
}

func TestCollectDecodedRepeatedToolMembersAreAmbiguous(t *testing.T) {
	for name, tc := range map[string]struct{ block, raw string }{
		"input": {
			`{"type":"tool_use","name":"fixture-tool","input":{"command":"first-input-value"},"input":{"command":"last-input-value"}}`,
			`fixture-tool {"command":"last-input-value"}`,
		},
		// encoding/json matches field names case-insensitively, so this is a repeat too.
		"folded_input": {
			`{"type":"tool_use","name":"fixture-tool","input":{"command":"first-input-value"},"Input":{"command":"last-input-value"}}`,
			`fixture-tool {"command":"last-input-value"}`,
		},
		"name": {
			`{"type":"tool_use","name":"fixture-tool","NAME":"fixture-other","input":{"command":"first-input-value"}}`,
			`fixture-other {"command":"first-input-value"}`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			c := CollectRequestContent(reqWithUserBlocks(blockFromJSON(t, tc.block)))
			requireCollectedChannel(t, c, ChannelToolUse, roleUser, tc.raw)
			requireCollectedChannel(t, c, ChannelToolUse, roleUser, "first-input-value")
			if name != "name" {
				requireCollectedChannel(t, c, ChannelToolUse, roleUser, "last-input-value")
			}
			if !c.Unscanned {
				t.Fatal("an ambiguous tool member was treated as fully inspected")
			}
			requireOpaqueRef(t, c, ChannelToolUse, roleUser, "arguments/ambiguous-member")
		})
	}
}

func TestCollectDecodedArgumentNumbersKeepSiblings(t *testing.T) {
	for _, number := range []string{"1e400", "-1e400", "123456789012345678901234567890e999"} {
		b, _ := rawToolBlock(t, ChannelToolUse, `{"before":"sibling-before","n":`+number+`,"after":"sibling-after"}`)
		c := CollectResponseContent(MessageResponse{Content: []ContentBlock{b}})
		if c.Unscanned {
			t.Fatalf("number %s made valid arguments unscanned", number)
		}
		requireCollectedChannel(t, c, ChannelToolUse, roleAssistant, "sibling-before")
		requireCollectedChannel(t, c, ChannelToolUse, roleAssistant, "sibling-after")
	}
}

func TestCollectDecodedArgumentAndEncodingDepthsAreIndependent(t *testing.T) {
	const plain = "fixture layered value\nsecond line"
	layered := func(layers int) string {
		s := plain
		for range layers {
			s = base64.StdEncoding.EncodeToString([]byte(s))
		}
		return s
	}
	nested := func(depth int, value string) string {
		return strings.Repeat("[", depth) + `"` + value + `"` + strings.Repeat("]", depth)
	}
	// Argument depth 6 and six encoding layers: both counters sit exactly at their bound.
	b, _ := rawToolBlock(t, ChannelToolUse, nested(6, layered(6)))
	c := CollectResponseContent(MessageResponse{Content: []ContentBlock{b}})
	if c.Unscanned {
		t.Fatal("argument depth consumed the independent encoding depth")
	}
	requireCollectedChannel(t, c, ChannelToolUse, roleAssistant, plain)

	b, _ = rawToolBlock(t, ChannelToolUse, nested(7, "fixture-plain"))
	c = CollectResponseContent(MessageResponse{Content: []ContentBlock{b}})
	requireOpaqueRef(t, c, ChannelToolUse, roleAssistant, "arguments/max-depth")
	if anyChannelRef(c, "encoded/max-depth") {
		t.Fatal("argument depth seven also tripped the encoding bound")
	}

	b, _ = rawToolBlock(t, ChannelToolUse, nested(1, layered(7)))
	c = CollectResponseContent(MessageResponse{Content: []ContentBlock{b}})
	requireOpaqueRef(t, c, ChannelToolUse, roleAssistant, "encoded/max-depth")
	if anyChannelRef(c, "arguments/max-depth") {
		t.Fatal("seven encoding layers also tripped the argument bound")
	}
}

func TestCollectDecodedURLSourcesKeepOriginalChannel(t *testing.T) {
	const rawURL = "https://fixture.invalid/fixture-path?api%5Fkey%3Dfixture&q=a+b"
	for _, kind := range []string{ChannelDocument, ChannelImage} {
		t.Run(kind, func(t *testing.T) {
			b := blockFromJSON(t, `{"type":"`+kind+`","source":{"type":"url","url":"`+rawURL+`"}}`)
			before, err := json.Marshal(b)
			if err != nil {
				t.Fatal(err)
			}
			c := CollectRequestContent(reqWithUserBlocks(b))
			ref := kind + "/url:fixture.invalid"
			original := false
			for _, ch := range c.Channels {
				if ch.Scannable && ch.Kind == kind && ch.Role == roleUser && ch.Text == rawURL && ch.Ref == ref {
					original = true
				}
				if strings.Contains(ch.Ref, "fixture-path") || strings.Contains(ch.Ref, "api") {
					t.Fatal("a URL path or query leaked into Ref")
				}
			}
			if !original {
				t.Fatal("the original URL string has no scannable channel")
			}
			requireCollectedChannel(t, c, kind, roleUser, "https://fixture.invalid/fixture-path?api_key=fixture&q=a b")
			requireOpaqueRef(t, c, kind, roleUser, ref)
			if !c.Unscanned {
				t.Fatal("remote content must remain unscanned")
			}
			after, err := json.Marshal(b)
			if err != nil || string(before) != string(after) {
				t.Fatal("collection changed the original wire block")
			}
		})
	}
}
