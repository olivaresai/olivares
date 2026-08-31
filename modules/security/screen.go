// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package security

import (
	"context"

	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// This file ships the Classifier seam Anthropic prescribes ON TOP of deterministic
// rules: a lightweight model screen (a Haiku-class call) that gives a fuzzy second
// opinion on prompt-injection / jailbreak attempts the regex floor cannot catch.
// Anthropic is explicit that keyword screening alone is insufficient, so the
// product must be able to wire a model screen — but it must do so HONESTLY: the
// Claude Messages API client that would back the screen does not exist in the repo
// yet (CLA-17 /). Until a screen function is injected, NO model call is made
// and the deterministic detectors stand alone (read-first: a guardrail dependency
// must never fail the inspection). This file is the wired-and-tested seam, not a
// fabricated model call.
// Source: https://platform.claude.com/docs/en/test-and-evaluate/strengthen-guardrails/mitigate-jailbreaks

// ScreenVerdict is the result of one lightweight model screen.
type ScreenVerdict struct {
	// Injection is whether the screen judged the text a prompt-injection or
	// jailbreak attempt.
	Injection bool
	// Severity optionally overrides the default (High) when the screen grades it.
	Severity string
	// Rationale is a short, NON-SENSITIVE reason (it must not echo the payload).
	Rationale string
}

// ScreenFunc is the injection seam to a Claude (Haiku-class) screening call. The
// composition root provides it once a Messages API client exists; it takes
// the surface and the text to screen and returns a verdict. It must not surface
// raw content in its rationale (minimal-data, docs/SECURITY-HARDENING.md).
type ScreenFunc func(ctx context.Context, surface string, text string) (ScreenVerdict, error)

// anthropicScreenClassifier adapts a ScreenFunc to the Classifier seam, applying
// Anthropic's recommended lightweight output/content screening. A nil screen makes
// Classify a no-op (the safe default until the model client is wired), so the
// adapter can be constructed unconditionally and only does work once backed.
type anthropicScreenClassifier struct{ screen ScreenFunc }

// NewAnthropicScreenClassifier wires a model-screen function as the optional
// guardrail Classifier (security.WithClassifier). Pass the Haiku-backed ScreenFunc
// when the Messages client lands; pass nil for the honest no-op seam today.
func NewAnthropicScreenClassifier(screen ScreenFunc) Classifier {
	return anthropicScreenClassifier{screen: screen}
}

// Classify runs the model screen and turns an injection verdict into a Detection.
// A nil screen, or a screen that finds nothing, returns no detections. An error is
// propagated to the caller, which logs and ignores it (the deterministic
// detections still stand — read-first).
func (c anthropicScreenClassifier) Classify(ctx context.Context, in GuardrailInput) ([]Detection, error) {
	if c.screen == nil {
		return nil, nil
	}
	v, err := c.screen(ctx, string(in.Surface), in.Text)
	if err != nil {
		return nil, err
	}
	if !v.Injection {
		return nil, nil
	}
	return []Detection{
		Detection{
			Class:    classInjection,
			Rule:     "model-screen",
			Severity: sevOrDefault(v.Severity),
			Title:    "model screen flagged a prompt-injection / jailbreak attempt",
		}.tagged("LLM01:2025", "AML.T0051"),
	}, nil
}

// sevOrDefault parses an optional severity from the screen verdict, defaulting to
// High (a model-flagged injection is serious) when unset or unrecognized.
func sevOrDefault(s string) sdkmodel.Severity {
	switch sdkmodel.Severity(s) {
	case sdkmodel.SeverityLow, sdkmodel.SeverityMedium, sdkmodel.SeverityHigh, sdkmodel.SeverityCritical:
		return sdkmodel.Severity(s)
	default:
		return sdkmodel.SeverityHigh
	}
}
