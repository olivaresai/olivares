// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package local

import (
	"strconv"
	"strings"
)

// vLLM exposes per-model token counters on its Prometheus /metrics endpoint:
//
//	vllm:prompt_tokens_total{model_name="meta-llama/Llama-3.1-8B"} 12345
//	vllm:generation_tokens_total{model_name="meta-llama/Llama-3.1-8B"} 6789
//
// This file is a deliberately tiny, dependency-free parser for exactly those two
// counters — not a general Prometheus client. It reads only token COUNTS and the
// model_name label; it carries no prompt/output content (minimal-data, docs/SECURITY-HARDENING.md).

const (
	promptMetric     = "vllm:prompt_tokens_total"
	generationMetric = "vllm:generation_tokens_total"
)

// modelTokens accumulates the prompt/generation counters for one model.
type modelTokens struct {
	prompt     int64
	generation int64
}

// parseVLLMTokens extracts per-model prompt/generation token totals from a
// Prometheus exposition body. Lines that are comments, blank, or other metrics are
// ignored; a malformed sample is skipped, never fatal. Counter values may be float
// (e.g. "1.2345e+04"); they are rounded to int64.
func parseVLLMTokens(body string) map[string]*modelTokens {
	out := map[string]*modelTokens{}
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		var isPrompt bool
		switch {
		case strings.HasPrefix(line, promptMetric):
			isPrompt = true
		case strings.HasPrefix(line, generationMetric):
			isPrompt = false
		default:
			continue
		}
		model, value, ok := parseSample(line)
		if !ok || model == "" {
			continue
		}
		mt := out[model]
		if mt == nil {
			mt = &modelTokens{}
			out[model] = mt
		}
		if isPrompt {
			mt.prompt += value
		} else {
			mt.generation += value
		}
	}
	return out
}

// parseSample extracts the model_name label and the numeric value from one metric
// line. It returns ok=false for a line it cannot parse.
func parseSample(line string) (model string, value int64, ok bool) {
	// Labels (if any) sit between the first '{' and the matching '}'.
	openIdx := strings.IndexByte(line, '{')
	closeIdx := strings.LastIndexByte(line, '}')
	var rest string
	if openIdx >= 0 && closeIdx > openIdx {
		model = labelValue(line[openIdx+1:closeIdx], "model_name")
		rest = strings.TrimSpace(line[closeIdx+1:])
	} else {
		// No labels: "metric value" — no model_name, so nothing to attribute.
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return "", 0, false
		}
		rest = fields[len(fields)-1]
	}
	// The value is the first whitespace-separated token of the remainder (a
	// trailing timestamp, if present, is ignored).
	valStr := rest
	if i := strings.IndexAny(rest, " \t"); i >= 0 {
		valStr = rest[:i]
	}
	f, err := strconv.ParseFloat(valStr, 64)
	if err != nil {
		return "", 0, false
	}
	return model, int64(f + 0.5), true
}

// labelValue returns the value of label key in a Prometheus label set
// (`a="x",model_name="y"`), or "" if absent. It tolerates the simple, unescaped
// values vLLM emits for model names.
func labelValue(labels, key string) string {
	for _, part := range strings.Split(labels, ",") {
		part = strings.TrimSpace(part)
		eq := strings.IndexByte(part, '=')
		if eq < 0 {
			continue
		}
		if strings.TrimSpace(part[:eq]) != key {
			continue
		}
		v := strings.TrimSpace(part[eq+1:])
		return strings.Trim(v, `"`)
	}
	return ""
}
