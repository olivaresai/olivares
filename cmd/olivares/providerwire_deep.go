// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/olivaresai/olivares/modules/sessions"
)

// deepProviderProbe qualifies the selected model with at most 192 output tokens.
// Auto choice exercises the parser used by coding agents; named/required choice
// can succeed via constrained decoding even when vLLM's auto parser is disabled.
func (p providerProbe) deepProviderProbe(ctx context.Context, req sessions.ProviderProbeRequest, models []providerProbeModel) (*sessions.ProviderCapabilities, error) {
	selected := req.Model
	if selected == "" && len(models) == 1 {
		selected = models[0].ID
	}
	caps := &sessions.ProviderCapabilities{Model: selected}
	found := false
	for _, md := range models {
		if md.ID == selected {
			found = true
			// Optional metadata must not make an ordinary model-list probe fail.
			for _, raw := range []json.RawMessage{md.MaxModelLen, md.ContextLength} {
				var n int64
				if json.Unmarshal(raw, &n) == nil && n > 0 {
					caps.MaxContext = n
					break
				}
			}
			break
		}
	}
	if !found {
		caps.Detail = "Set a default model the server lists before running a deep test."
		return caps, nil
	}
	endpoint, err := modelsURL(req.Kind, req.BaseURL)
	if err != nil {
		return nil, err
	}
	endpoint = strings.TrimSuffix(endpoint, "/models") + "/chat/completions"
	// No redirects or retries for requests that can generate tokens.
	client := *p.client
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	for _, check := range []string{"tools", "streaming", "usage"} {
		request := map[string]any{
			"model": selected, "max_tokens": 64,
			"messages": []map[string]string{{"role": "user", "content": "Reply OK."}},
		}
		if check == "tools" {
			request["messages"] = []map[string]string{{"role": "user", "content": "Call olivares_probe with no arguments. Do not answer with text."}}
			request["tools"] = []any{map[string]any{"type": "function", "function": map[string]any{
				"name": "olivares_probe", "description": "Confirm tool calling without side effects.",
				"parameters": map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false},
			}}}
			request["tool_choice"] = "auto"
		}
		if check == "streaming" {
			request["stream"] = true
		}
		body, err := json.Marshal(request)
		if err != nil {
			return nil, fmt.Errorf("encode deep probe: %w", err)
		}
		hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
		if err != nil {
			return nil, fmt.Errorf("create deep probe request: %w", err)
		}
		authHeaders(hreq, req.Kind, "", req.APIKey)
		hreq.Header.Set("Content-Type", "application/json")
		hreq.Header.Set("User-Agent", providerProbeUserData+"/"+version)
		hreq.Header.Set("Accept", "application/json")
		if check == "streaming" {
			hreq.Header.Set("Accept", "text/event-stream")
		}
		resp, err := client.Do(hreq)
		if err != nil {
			caps.Detail += check + ": endpoint did not answer. "
			continue
		}
		data, readErr := io.ReadAll(io.LimitReader(resp.Body, providerProbeBodyCap+1))
		_ = resp.Body.Close()
		if readErr != nil || len(data) > providerProbeBodyCap || resp.StatusCode != http.StatusOK {
			caps.Detail += check + ": request failed or response exceeded its bound. "
			continue
		}
		if check == "streaming" {
			if strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
				seen, done, valid := false, false, true
				err := scanSSE(bytes.NewReader(data), func(_ string, event string) bool {
					if event == "[DONE]" {
						done = true
						return false
					}
					var chunk struct {
						Choices []struct{ Delta struct{ Content string } }
					}
					if json.Unmarshal([]byte(event), &chunk) != nil {
						valid = false
						return false
					}
					for _, c := range chunk.Choices {
						seen = seen || c.Delta.Content != ""
					}
					return true
				})

				caps.Streaming = err == nil && valid && seen && done
			}
		} else {
			var answer struct {
				Choices []struct {
					Message struct {
						Content   string
						ToolCalls []struct {
							Type     string
							Function struct {
								Name      string
								Arguments string
							}
						} `json:"tool_calls"`
					}
				}
				Usage *struct {
					Prompt     int64 `json:"prompt_tokens"`
					Completion int64 `json:"completion_tokens"`
					Total      int64 `json:"total_tokens"`
				}
			}
			if json.Unmarshal(data, &answer) == nil {
				if check == "tools" {
					for _, choice := range answer.Choices {
						for _, call := range choice.Message.ToolCalls {
							var args map[string]any
							caps.Tools = caps.Tools || call.Type == "function" && call.Function.Name == "olivares_probe" &&
								json.Unmarshal([]byte(call.Function.Arguments), &args) == nil && args != nil && len(args) == 0
						}
					}
				} else {
					u := answer.Usage
					caps.Usage = len(answer.Choices) > 0 && u != nil && u.Prompt > 0 && u.Completion > 0 && u.Total == u.Prompt+u.Completion
				}
			}
		}
	}
	if !caps.Tools {
		caps.Detail += "No usable tool call observed; for vLLM check --enable-auto-tool-choice and --tool-call-parser for this model. "
	}
	if !caps.Streaming {
		caps.Detail += "No complete streamed answer observed. "
	}
	if !caps.Usage {
		caps.Detail += "No valid usage report observed. "
	}
	if caps.MaxContext == 0 {
		caps.Detail += "Context length was not advertised by the server."
	}
	caps.Detail = strings.TrimSpace(caps.Detail)
	return caps, nil
}
