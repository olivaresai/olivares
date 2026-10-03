// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package redact

import (
	"errors"
	"strings"

	internal "github.com/olivaresai/olivares/connectors/internal/redact"
)

// GeneratedMaskSpan identifies byte offsets of a marker emitted by a masker.
// End is exclusive. Callers must carry these offsets from the replacement;
// rediscovering marker-looking text cannot prove that the masker generated it.
type GeneratedMaskSpan = internal.GeneratedMaskSpan

// CleanMasked applies the existing pattern floor outside proven generated
// masks, preserving each mask byte-exact. Spans must be ordered, nonoverlapping
// and bound one complete well-formed marker. Invalid provenance returns no text.
// An empty span list is exactly Clean; original marker-looking text is ordinary
// input. The result is display-only and never replaces live policy or execution.
func CleanMasked(text string, generated []GeneratedMaskSpan) (string, error) {
	end := 0
	for _, span := range generated {
		if span.Start < end || span.End <= span.Start || span.End > len(text) || !generatedMaskMarker(text[span.Start:span.End]) {
			return "", errors.New("generated mask provenance is invalid")
		}
		end = span.End
	}
	return internal.CleanMasked(text, generated), nil
}

func generatedMaskMarker(marker string) bool {
	if marker == "[REDACTED]" {
		return true
	}
	if !strings.HasSuffix(marker, "]") {
		return false
	}
	for _, prefix := range []string{"[secret ", "[REDACTED:"} {
		if strings.HasPrefix(marker, prefix) {
			return shellReviewSecretMarkerName(marker[len(prefix) : len(marker)-1])
		}
	}
	return false
}
