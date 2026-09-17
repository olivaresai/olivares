// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package modelprovider

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"
)

func TestPrepareChatTextRequestExactBytesAndImmutableDigest(t *testing.T) {
	in := ChatTextRequest{Model: "route/model-1", Input: "  Hola 🌍\n\"quoted\" \\ <>&\t  ", MaxCompletionTokens: 37}
	p, err := PrepareChatTextRequest(in)
	if err != nil {
		t.Fatal(err)
	}
	want := []byte(`{"model":"route/model-1","messages":[{"role":"user","content":"  Hola 🌍\n\"quoted\" \\ \u003c\u003e\u0026\t  "}],"max_completion_tokens":37,"stream":false,"n":1,"store":false}`)
	if p.IsZero() || !bytes.Equal(p.Bytes(), want) {
		t.Fatalf("prepared bytes differ: %q", p.Bytes())
	}
	if p.Digest() != sha256.Sum256(want) {
		t.Fatal("digest is not bound to the exact wire bytes")
	}
	copyOfPrepared := p
	callerBytes := p.Bytes()
	callerBytes[0] = '!'
	callerDigest := p.Digest()
	callerDigest[0] ^= 0xff
	in.Input = "changed after preparation"
	if !bytes.Equal(p.Bytes(), want) || p.Digest() != sha256.Sum256(want) || copyOfPrepared.Digest() != p.Digest() {
		t.Fatal("caller mutation changed a prepared value")
	}
	var zero PreparedChatTextRequest
	if !zero.IsZero() || zero.Bytes() != nil || zero.Digest() != [sha256.Size]byte{} {
		t.Fatal("zero value must be recognizably unprepared")
	}
}

func TestPrepareChatTextRequestUnicodeAndTokenRange(t *testing.T) {
	in := ChatTextRequest{Model: "私有/模型", Input: "e\u0301\u0000\u2028\u2029\ufffd", MaxCompletionTokens: math.MaxInt64}
	p, err := PrepareChatTextRequest(in)
	if err != nil {
		t.Fatal(err)
	}
	var wire struct {
		Model    string `json:"model"`
		Messages []struct {
			Content string `json:"content"`
		} `json:"messages"`
		MaxCompletionTokens int64 `json:"max_completion_tokens"`
	}
	if err := json.Unmarshal(p.Bytes(), &wire); err != nil {
		t.Fatal(err)
	}
	if wire.Model != in.Model || len(wire.Messages) != 1 || wire.Messages[0].Content != in.Input || wire.MaxCompletionTokens != in.MaxCompletionTokens {
		t.Fatal("valid Unicode or token bound changed during preparation")
	}
}

func TestPrepareChatTextRequestRejectsInvalidInput(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*ChatTextRequest)
		field  string
	}{
		{"missing_model", func(r *ChatTextRequest) { r.Model = "" }, "model"},
		{"model_space", func(r *ChatTextRequest) { r.Model = "model x" }, "model"},
		{"model_unicode_space", func(r *ChatTextRequest) { r.Model = "model\u2003" }, "model"},
		{"model_control", func(r *ChatTextRequest) { r.Model = "model\x00" }, "model"},
		{"model_invalid_utf8", func(r *ChatTextRequest) { r.Model = "model\xff" }, "model"},
		{"missing_input", func(r *ChatTextRequest) { r.Input = "" }, "input"},
		{"blank_input", func(r *ChatTextRequest) { r.Input = " \n\t\u2003" }, "input"},
		{"input_invalid_utf8", func(r *ChatTextRequest) { r.Input = "input\xff" }, "input"},
		{"zero_tokens", func(r *ChatTextRequest) { r.MaxCompletionTokens = 0 }, "max_completion_tokens"},
		{"negative_tokens", func(r *ChatTextRequest) { r.MaxCompletionTokens = -1 }, "max_completion_tokens"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := ChatTextRequest{Model: "model", Input: "hello", MaxCompletionTokens: 1}
			tc.change(&r)
			got, err := PrepareChatTextRequest(r)
			assertChatTextError(t, err, ChatTextInvalidRequest, tc.field)
			if !got.IsZero() || got.Bytes() != nil || got.Digest() != [sha256.Size]byte{} {
				t.Fatal("invalid request produced a prepared body")
			}
		})
	}
}

func chatTextWire(message, finish, usage string) []byte {
	return []byte(`{"object":"chat.completion","model":"observed-model","choices":[{"index":0,"message":` + message + `,"finish_reason":"` + finish + `"}]` + usage + `}`)
}

func TestDecodeChatTextResponseStatesAndText(t *testing.T) {
	for _, tc := range []struct {
		name, message, finish string
		state                 ChatTextState
		text, refusal         *string
	}{
		{"stop", `{"role":"assistant","content":"Hola 🌍\n\"quoted\""}`, "stop", ChatTextStopped, chatTextPtr("Hola 🌍\n\"quoted\""), nil},
		{"empty_text", `{"role":"assistant","content":"","refusal":null}`, "stop", ChatTextStopped, chatTextPtr(""), nil},
		{"length_partial", `{"role":"assistant","content":"partial"}`, "length", ChatTextLengthLimited, chatTextPtr("partial"), nil},
		{"length_null", `{"role":"assistant","content":null}`, "length", ChatTextLengthLimited, nil, nil},
		{"filtered_null", `{"role":"assistant","content":null}`, "content_filter", ChatTextContentFiltered, nil, nil},
		{"filtered_partial", `{"role":"assistant","content":"partial"}`, "content_filter", ChatTextContentFiltered, chatTextPtr("partial"), nil},
		{"refusal", `{"role":"assistant","content":null,"refusal":"declined"}`, "stop", ChatTextRefused, nil, chatTextPtr("declined")},
		{"refusal_with_text", `{"role":"assistant","content":"context","refusal":"declined"}`, "stop", ChatTextRefused, chatTextPtr("context"), chatTextPtr("declined")},
		{"refusal_preserves_length", `{"role":"assistant","refusal":"declined"}`, "length", ChatTextRefused, nil, chatTextPtr("declined")},
		{"empty_refusal", `{"role":"assistant","refusal":""}`, "stop", ChatTextRefused, nil, chatTextPtr("")},
		{"escaped_surrogate_pair", `{"role":"assistant","content":"\ud83c\udf0d"}`, "stop", ChatTextStopped, chatTextPtr("🌍"), nil},
		{"literal_escape_and_replacement", `{"role":"assistant","content":"\\ud800 \ufffd"}`, "stop", ChatTextStopped, chatTextPtr("\\ud800 �"), nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := chatTextWire(tc.message, tc.finish, "")
			got, err := DecodeChatTextResponse(body, len(body))
			if err != nil {
				t.Fatal(err)
			}
			want := ChatTextResponse{Model: "observed-model", Text: tc.text, Refusal: tc.refusal, FinishReason: tc.finish, State: tc.state}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("response mismatch: got %#v want %#v", got, want)
			}
			for i := range body {
				body[i] = 'x'
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatal("input byte mutation changed decoded response")
			}
		})
	}
}

func TestDecodeChatTextResponseUsageUnknownVersusZero(t *testing.T) {
	for _, tc := range []struct {
		name, usage string
		want        *ChatTextUsage
	}{
		{"absent", "", nil},
		{"null", `,"usage":null`, nil},
		{"empty_object", `,"usage":{}`, &ChatTextUsage{}},
		{"null_counters", `,"usage":{"prompt_tokens":null,"completion_tokens":null,"total_tokens":null,"prompt_tokens_details":null,"completion_tokens_details":null}`, &ChatTextUsage{}},
		{"zero", `,"usage":{"prompt_tokens":0,"completion_tokens":0,"total_tokens":0}`, &ChatTextUsage{PromptTokens: chatTextPtr(int64(0)), CompletionTokens: chatTextPtr(int64(0)), TotalTokens: chatTextPtr(int64(0))}},
		{"partial_and_details", `,"usage":{"completion_tokens":7,"prompt_tokens_details":{"cached_tokens":0,"audio_tokens":null},"completion_tokens_details":{"reasoning_tokens":2,"audio_tokens":0,"accepted_prediction_tokens":1,"rejected_prediction_tokens":3}}`, &ChatTextUsage{CompletionTokens: chatTextPtr(int64(7)), PromptTokensDetails: &ChatTextPromptTokensDetails{CachedTokens: chatTextPtr(int64(0))}, CompletionTokensDetails: &ChatTextCompletionTokensDetails{ReasoningTokens: chatTextPtr(int64(2)), AudioTokens: chatTextPtr(int64(0)), AcceptedPredictionTokens: chatTextPtr(int64(1)), RejectedPredictionTokens: chatTextPtr(int64(3))}}},
		{"maximum_without_inferred_total", `,"usage":{"prompt_tokens":9223372036854775807}`, &ChatTextUsage{PromptTokens: chatTextPtr(int64(math.MaxInt64))}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := chatTextWire(`{"role":"assistant","content":"ok"}`, "stop", tc.usage)
			got, err := DecodeChatTextResponse(body, len(body))
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got.Usage, tc.want) {
				t.Fatalf("usage mismatch: %#v", got.Usage)
			}
		})
	}
}

func TestDecodeChatTextResponseAcceptsExtensionsWithoutGrantingAuthority(t *testing.T) {
	body := []byte(`{
		"model":"different-upstream-model", "vendor":{"nested":[1,2]}, "error":null,
		"choices":[{"index":0,"vendor_flag":true,"finish_reason":"stop","message":{
			"role":"assistant","content":"ok","annotations":[],"vendor":"extension",
			"function_call":null,"audio":null,"tool_calls":[ ]}}],
		"usage":{"vendor":1,"prompt_tokens_details":{"future_counter":2}}
	}`)
	got, err := DecodeChatTextResponse(body, len(body))
	if err != nil {
		t.Fatal(err)
	}
	if got.Model != "different-upstream-model" || got.State != ChatTextStopped || got.Usage == nil || got.Usage.PromptTokensDetails == nil || got.Usage.PromptTokensDetails.CachedTokens != nil {
		t.Fatal("extension handling changed observations or fabricated counters")
	}
}

func TestDecodeChatTextResponseRejectsInvalidShapesAndUnsupportedPayloads(t *testing.T) {
	valid := string(chatTextWire(`{"role":"assistant","content":"ok"}`, "stop", ""))
	for _, tc := range []struct {
		name, body string
		kind       ChatTextErrorKind
	}{
		{"empty", "", ChatTextInvalidJSON},
		{"malformed", `{"secret":"PAYLOAD_SENTINEL",`, ChatTextInvalidJSON},
		{"truncated", valid[:len(valid)-1], ChatTextInvalidJSON},
		{"trailing_json", valid + `{}`, ChatTextInvalidJSON},
		{"invalid_utf8", strings.Replace(valid, "ok", "\xff", 1), ChatTextInvalidJSON},
		{"high_surrogate", strings.Replace(valid, "ok", `\ud800`, 1), ChatTextInvalidJSON},
		{"low_surrogate", strings.Replace(valid, "ok", `\udfff`, 1), ChatTextInvalidJSON},
		{"wrong_surrogate_pair", strings.Replace(valid, "ok", `\ud800\u0041`, 1), ChatTextInvalidJSON},
		{"root_null", `null`, ChatTextInvalidResponse},
		{"root_array", `[]`, ChatTextInvalidResponse},
		{"no_model", strings.Replace(valid, `"model":"observed-model",`, "", 1), ChatTextInvalidResponse},
		{"null_model", strings.Replace(valid, `"observed-model"`, `null`, 1), ChatTextInvalidResponse},
		{"invalid_model", strings.Replace(valid, `observed-model`, `PAYLOAD_SENTINEL model`, 1), ChatTextInvalidResponse},
		{"no_choices", `{"model":"m"}`, ChatTextInvalidResponse},
		{"null_choices", `{"model":"m","choices":null}`, ChatTextInvalidResponse},
		{"empty_choices", `{"model":"m","choices":[]}`, ChatTextInvalidResponse},
		{"multiple_choices", `{"model":"m","choices":[{},{}]}`, ChatTextInvalidResponse},
		{"choices_object", `{"model":"m","choices":{}}`, ChatTextInvalidResponse},
		{"choice_null", `{"model":"m","choices":[null]}`, ChatTextInvalidResponse},
		{"index_missing", strings.Replace(valid, `"index":0,`, "", 1), ChatTextInvalidResponse},
		{"index_null", strings.Replace(valid, `"index":0`, `"index":null`, 1), ChatTextInvalidResponse},
		{"index_one", strings.Replace(valid, `"index":0`, `"index":1`, 1), ChatTextInvalidResponse},
		{"index_fraction", strings.Replace(valid, `"index":0`, `"index":0.0`, 1), ChatTextInvalidResponse},
		{"wrong_role", strings.Replace(valid, `assistant`, `user`, 1), ChatTextInvalidResponse},
		{"message_null", string(chatTextWire(`null`, "stop", "")), ChatTextInvalidResponse},
		{"null_text_stop", string(chatTextWire(`{"role":"assistant","content":null}`, "stop", "")), ChatTextInvalidResponse},
		{"missing_text_stop", string(chatTextWire(`{"role":"assistant"}`, "stop", "")), ChatTextInvalidResponse},
		{"unknown_finish", strings.Replace(valid, `"stop"`, `"PAYLOAD_SENTINEL"`, 1), ChatTextInvalidResponse},
		{"null_finish", strings.Replace(valid, `"stop"`, `null`, 1), ChatTextInvalidResponse},
		{"stream_chunk", strings.Replace(valid, `chat.completion`, `chat.completion.chunk`, 1), ChatTextInvalidResponse},
		{"error_envelope", strings.Replace(valid, `"choices":`, `"error":{"message":"PAYLOAD_SENTINEL"},"choices":`, 1), ChatTextInvalidResponse},
		{"tool_finish", strings.Replace(valid, `"stop"`, `"tool_calls"`, 1), ChatTextUnsupportedResponse},
		{"function_finish", strings.Replace(valid, `"stop"`, `"function_call"`, 1), ChatTextUnsupportedResponse},
		{"multimodal_content", string(chatTextWire(`{"role":"assistant","content":[{"type":"text","text":"PAYLOAD_SENTINEL"}]}`, "stop", "")), ChatTextUnsupportedResponse},
		{"numeric_content", string(chatTextWire(`{"role":"assistant","content":12}`, "stop", "")), ChatTextUnsupportedResponse},
		{"object_refusal", string(chatTextWire(`{"role":"assistant","content":"ok","refusal":{}}`, "stop", "")), ChatTextUnsupportedResponse},
		{"duplicate_model", strings.Replace(valid, `"model":`, `"model":"first","model":`, 1), ChatTextInvalidResponse},
		{"duplicate_index", strings.Replace(valid, `"index":`, `"index":1,"index":`, 1), ChatTextInvalidResponse},
		{"duplicate_content", strings.Replace(valid, `"content":`, `"content":"first","content":`, 1), ChatTextInvalidResponse},
		{"duplicate_escaped_tools", string(chatTextWire(`{"role":"assistant","content":"ok","tool_calls":[{}],"tool\u005fcalls":null}`, "stop", "")), ChatTextInvalidResponse},
		{"duplicate_unknown", strings.Replace(valid, `"model":`, `"vendor":1,"vendor":2,"model":`, 1), ChatTextInvalidResponse},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := DecodeChatTextResponse([]byte(tc.body), len(tc.body)+1)
			assertChatTextError(t, err, tc.kind, "")
			if !reflect.DeepEqual(got, ChatTextResponse{}) {
				t.Fatal("error returned a partially decoded response")
			}
		})
	}
	for _, field := range []string{"tools", "tool_calls", "function_call", "audio"} {
		for _, position := range []string{"root", "choice", "message"} {
			t.Run(position+"_"+field, func(t *testing.T) {
				needle := `"model":`
				if position == "choice" {
					needle = `"index":`
				}
				if position == "message" {
					needle = `"role":`
				}
				body := strings.Replace(valid, needle, `"`+field+`":{"secret":"PAYLOAD_SENTINEL"},`+needle, 1)
				got, err := DecodeChatTextResponse([]byte(body), len(body))
				assertChatTextError(t, err, ChatTextUnsupportedResponse, field)
				if !reflect.DeepEqual(got, ChatTextResponse{}) {
					t.Fatal("unsupported payload returned a partial response")
				}
			})
		}
	}
}

func TestDecodeChatTextResponseRejectsInvalidUsage(t *testing.T) {
	for _, usage := range []string{
		`[]`, `false`, `{"prompt_tokens":-1}`, `{"completion_tokens":1.5}`, `{"total_tokens":"0"}`,
		`{"total_tokens":1e0}`, `{"total_tokens":9223372036854775808}`, `{"total_tokens":{}}`,
		`{"prompt_tokens":9,"prompt_tokens":0}`, `{"prompt_tokens_details":[]}`,
		`{"prompt_tokens_details":{"cached_tokens":-1}}`, `{"prompt_tokens_details":{"audio_tokens":"0"}}`,
		`{"completion_tokens_details":false}`, `{"completion_tokens_details":{"reasoning_tokens":0.5}}`,
		`{"completion_tokens_details":{"audio_tokens":-1}}`, `{"completion_tokens_details":{"accepted_prediction_tokens":true}}`,
		`{"completion_tokens_details":{"rejected_prediction_tokens":-1}}`,
	} {
		t.Run(usage, func(t *testing.T) {
			body := chatTextWire(`{"role":"assistant","content":"PAYLOAD_SENTINEL"}`, "stop", `,"usage":`+usage)
			got, err := DecodeChatTextResponse(body, len(body))
			assertChatTextError(t, err, ChatTextInvalidResponse, "")
			if !reflect.DeepEqual(got, ChatTextResponse{}) {
				t.Fatal("invalid usage leaked an earlier decoded result")
			}
		})
	}
}

func TestDecodeChatTextResponseCallerByteLimit(t *testing.T) {
	body := append(chatTextWire(`{"role":"assistant","content":"🌍"}`, "stop", ""), '\n', ' ')
	if _, err := DecodeChatTextResponse(body, len(body)); err != nil {
		t.Fatalf("exact limit: %v", err)
	}
	if _, err := DecodeChatTextResponse(body, len(body)+1); err != nil {
		t.Fatalf("larger caller limit: %v", err)
	}
	for _, limit := range []int{-1, 0, len(body) - 1} {
		got, err := DecodeChatTextResponse(body, limit)
		kind := ChatTextLimitExceeded
		if limit <= 0 {
			kind = ChatTextInvalidLimit
		}
		assertChatTextError(t, err, kind, "response")
		if !reflect.DeepEqual(got, ChatTextResponse{}) {
			t.Fatal("limit failure returned a response")
		}
	}
	_, err := DecodeChatTextResponse([]byte(`not JSON PAYLOAD_SENTINEL`), 1)
	assertChatTextError(t, err, ChatTextLimitExceeded, "response")
}

func chatTextPtr[T any](v T) *T { return &v }

func assertChatTextError(t *testing.T, err error, kind ChatTextErrorKind, field string) {
	t.Helper()
	var got *ChatTextCodecError
	if !errors.As(err, &got) {
		t.Fatalf("expected typed codec error, got %v", err)
	}
	if got.Kind != kind || (field != "" && got.Field != field) {
		t.Fatalf("wrong error: %v", err)
	}
	if strings.Contains(err.Error(), "PAYLOAD_SENTINEL") || errors.Unwrap(err) != nil {
		t.Fatal("error exposed provider content or wrapped parser detail")
	}
}
