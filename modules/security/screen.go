// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package security

import (
	"context"

	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// The optional classifier interface remains supported. Stock binaries currently
// configure no model-backed screen: provider/account selection is not connected
// to this port. A nil screen makes no model call; deterministic inspection remains active.
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

// ScreenFunc accepts a surface and text and returns a model-screen verdict.
// Stock binaries do not configure one. Rationale must not expose raw content.
type ScreenFunc func(ctx context.Context, surface string, text string) (ScreenVerdict, error)

// anthropicScreenClassifier adapts an injected ScreenFunc to Classifier.
// A nil screen makes Classify a no-op.
type anthropicScreenClassifier struct{ screen ScreenFunc }

// NewAnthropicScreenClassifier adapts an injected screen for WithClassifier.
// Injected screens add detections without suppressing deterministic findings.
// A nil screen remains a no-op.
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
