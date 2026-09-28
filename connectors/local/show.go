// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package local

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"

	"github.com/olivaresai/olivares/connectors/modelprovider"
)

const (
	capabilitySourceLive    = "live"
	capabilitySourceUnknown = "unknown"

	// ollamaShowConcurrency is the maximum number of per-model show reads in
	// flight. The list still makes one request per model.
	ollamaShowConcurrency = 4

	// maxOllamaShowBody bounds a non-verbose show document. The request does
	// not set verbose, so a larger body is rejected and that model stays unknown.
	maxOllamaShowBody = 1 << 20
)

// modelEvidence is the capability claim one discovered model may carry.
// A nil caps slice with source unknown is absence of evidence, not a claim
// that the model has no capabilities.
type modelEvidence struct {
	caps    []modelprovider.Capability
	source  string
	context int64
	api     *modelprovider.ModelCapabilities
}

// ollamaThinkValue is one entry of the show thinking.values array. The
// documented values are booleans or strings; numbers are not accepted.
type ollamaThinkValue struct {
	Text string
}

func (v *ollamaThinkValue) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	switch string(b) {
	case "true":
		v.Text = "true"
		return nil
	case "false":
		v.Text = "false"
		return nil
	}
	if len(b) > 0 && b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		v.Text = s
		return nil
	}
	return fmt.Errorf("ollama thinking value is not a string or boolean")
}

// ollamaEvidence reads POST /api/show for one model. The transport is the same
// doer the list client uses, so the caller's context is the only deadline.
func (s *Source) ollamaEvidence(ctx context.Context, sem chan struct{}, name string) modelEvidence {
	unknown := modelEvidence{source: capabilitySourceUnknown}
	select {
	case sem <- struct{}{}:
		defer func() { <-sem }()
	case <-ctx.Done():
		return unknown
	}
	show, err := s.ollamaShow(ctx, name)
	if err != nil || show.Capabilities == nil {
		return unknown
	}
	caps, api := mapOllamaCapabilities(show)
	// Generate and chat default stream to true. Embed does not. Streaming is
	// claimed only when the returned list includes completion, which is the
	// feature those endpoints serve. A failed show stays unknown instead.
	if capabilityListed(*show.Capabilities, "completion") {
		caps = append([]modelprovider.Capability{modelprovider.CapStreaming}, caps...)
	}
	if api != nil {
		api.AsOf = s.clock().UTC().Format("2006-01-02")
	}
	return modelEvidence{
		caps:    caps,
		source:  capabilitySourceLive,
		context: ollamaContextWindow(show.ModelInfo),
		api:     api,
	}
}

// ollamaShow is the documented per-model read. It is POST because that is the
// show method; the connector does not pull, create, or generate.
func (s *Source) ollamaShow(ctx context.Context, modelName string) (ollamaShowResponse, error) {
	var zero ollamaShowResponse
	payload, err := json.Marshal(struct {
		Model string `json:"model"`
	}{Model: modelName})
	if err != nil {
		return zero, err
	}
	endpoint := strings.TrimRight(s.ollamaURL, "/") + "/api/show"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return zero, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	doer := s.doer
	if doer == nil {
		doer = http.DefaultClient
	}
	resp, err := doer.Do(req)
	if err != nil {
		return zero, err
	}
	if resp == nil || resp.Body == nil {
		return zero, fmt.Errorf("ollama show %s: empty response", modelName)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxOllamaShowBody+1))
	if err != nil {
		return zero, err
	}
	if len(body) > maxOllamaShowBody {
		return zero, fmt.Errorf("ollama show %s: body exceeds %d bytes", modelName, maxOllamaShowBody)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return zero, fmt.Errorf("ollama show %s: status %d", modelName, resp.StatusCode)
	}
	var out ollamaShowResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return zero, err
	}
	return out, nil
}

// mapOllamaCapabilities maps documented show capability names onto existing
// constants. completion, insert, embedding, image, and audio have no constant
// and are not reported. thinking.values of only false withdraws thinking.
func mapOllamaCapabilities(show ollamaShowResponse) ([]modelprovider.Capability, *modelprovider.ModelCapabilities) {
	off := thinkingIsOff(show.Thinking)
	var caps []modelprovider.Capability
	for _, name := range *show.Capabilities {
		switch name {
		case "tools":
			caps = addCapability(caps, modelprovider.CapToolUse)
		case "vision":
			caps = addCapability(caps, modelprovider.CapVision)
		case "thinking":
			if !off {
				caps = addCapability(caps, modelprovider.CapExtendedThinking)
			}
		}
	}
	modes := namedThinkingModes(show.Thinking)
	if !off && thinkingSupported(show.Thinking) {
		caps = addCapability(caps, modelprovider.CapExtendedThinking)
	}
	if off || len(modes) == 0 {
		return caps, nil
	}
	return caps, &modelprovider.ModelCapabilities{ThinkingModes: modes}
}

func capabilityListed(names []string, want string) bool {
	for _, name := range names {
		if name == want {
			return true
		}
	}
	return false
}

func addCapability(dst []modelprovider.Capability, c modelprovider.Capability) []modelprovider.Capability {
	if modelprovider.Has(dst, c) {
		return dst
	}
	return append(dst, c)
}

// thinkingIsOff reports the documented "values: [false]" case: the model does
// not support thinking. An omitted object is not that case.
func thinkingIsOff(th *ollamaThinking) bool {
	if th == nil || len(th.Values) == 0 {
		return false
	}
	for _, v := range th.Values {
		if v.Text != "false" {
			return false
		}
	}
	return true
}

// thinkingSupported is true when values contain true or a named level.
func thinkingSupported(th *ollamaThinking) bool {
	if th == nil {
		return false
	}
	for _, v := range th.Values {
		if v.Text != "" && v.Text != "false" {
			return true
		}
	}
	return false
}

// namedThinkingModes returns the string levels. Boolean on/off values are not
// mode names.
func namedThinkingModes(th *ollamaThinking) []string {
	if th == nil {
		return nil
	}
	var modes []string
	for _, v := range th.Values {
		if v.Text == "" || v.Text == "true" || v.Text == "false" {
			continue
		}
		modes = append(modes, v.Text)
	}
	return modes
}

// ollamaContextWindow reads the single "{architecture}.context_length" value
// from model_info, as in the documented show example. Zero means the value was
// absent, not a positive whole number, or ambiguous. The parameters num_ctx
// text is a runtime option and is not this window.
func ollamaContextWindow(info map[string]any) int64 {
	var got int64
	n := 0
	for key, value := range info {
		arch, rest, ok := strings.Cut(key, ".")
		if !ok || rest != "context_length" || arch == "" {
			continue
		}
		parsed, ok := positiveWhole(value)
		if !ok {
			continue
		}
		n++
		got = parsed
	}
	if n != 1 {
		return 0
	}
	return got
}

func positiveWhole(v any) (int64, bool) {
	f, ok := v.(float64)
	if !ok || math.IsNaN(f) || math.IsInf(f, 0) || f <= 0 || f != math.Trunc(f) || f > float64(math.MaxInt64) {
		return 0, false
	}
	return int64(f), true
}
