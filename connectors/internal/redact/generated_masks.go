// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package redact

import "strings"

// GeneratedMaskSpan identifies byte offsets emitted by a masker, not inferred
// from display text. The public connector adapter validates them before entry.
type GeneratedMaskSpan struct{ Start, End int }

type maskReplacement struct{ start, end int }

// CleanMasked uses the existing pattern floor while preserving generated spans.
// Match against an equal-length shadow so a marker's spaces cannot split a
// credential match. Only bytes outside the proven spans are ever replaced.
func CleanMasked(text string, generated []GeneratedMaskSpan) string {
	if len(generated) == 0 {
		return Clean(text)
	}
	spans := append([]GeneratedMaskSpan(nil), generated...)
	apply := func(matches []maskReplacement, marker string) {
		text, spans = replaceOutsideGenerated(text, spans, matches, marker)
	}
	var matches []maskReplacement
	for _, at := range urlUserInfoRe.FindAllStringSubmatchIndex(maskedPatternShadow(text, spans), -1) {
		matches = append(matches, maskReplacement{at[3], at[1] - 1})
	}
	apply(matches, Placeholder)
	matches = nil
	for _, at := range keyValueRe.FindAllStringSubmatchIndex(maskedPatternShadow(text, spans), -1) {
		matches = append(matches, maskReplacement{at[4], at[5]})
	}
	apply(matches, Placeholder)
	for _, pattern := range wholeMatchPatterns {
		matches = nil
		shadow := maskedPatternShadow(text, spans)
		for _, at := range pattern.re.FindAllStringIndex(shadow, -1) {
			end := at[1]
			for _, span := range spans {
				if span.Start < end && span.End > at[0] {
					// A fixed-length token pattern can terminate inside or just
					// after a longer generated marker. Its hidden original length
					// is unknown; scrub the remaining adjacent credential bytes.
					for end < len(shadow) && credentialTokenByte(shadow[end]) {
						end++
					}
					break
				}
			}
			if len(matches) > 0 && at[0] <= matches[len(matches)-1].end {
				matches[len(matches)-1].end = max(matches[len(matches)-1].end, end)
			} else {
				matches = append(matches, maskReplacement{at[0], end})
			}
		}
		apply(matches, "[REDACTED:"+pattern.label+"]")
	}
	return text
}

func maskedPatternShadow(text string, spans []GeneratedMaskSpan) string {
	shadow := []byte(text)
	for _, span := range spans {
		for i := span.Start; i < span.End; i++ {
			shadow[i] = 'X'
		}
	}
	return string(shadow)
}

// Subtract the generated spans from each ordered pattern match. A partial
// overlap never splits or replaces a marker. The remaining edits are ordered
// and disjoint, allowing offsets to follow each replacement without rescanning.
func replaceOutsideGenerated(text string, spans []GeneratedMaskSpan, matches []maskReplacement, marker string) (string, []GeneratedMaskSpan) {
	var edits []maskReplacement
	for _, match := range matches {
		cursor := match.start
		for _, span := range spans {
			if span.End <= cursor {
				continue
			}
			if span.Start >= match.end {
				break
			}
			if cursor < span.Start {
				edits = append(edits, maskReplacement{cursor, span.Start})
			}
			cursor = span.End
			if cursor >= match.end {
				break
			}
		}
		if cursor < match.end {
			edits = append(edits, maskReplacement{cursor, match.end})
		}
	}
	if len(edits) == 0 {
		return text, spans
	}
	var out strings.Builder
	out.Grow(len(text))
	cursor := 0
	for _, edit := range edits {
		out.WriteString(text[cursor:edit.start])
		out.WriteString(marker)
		cursor = edit.end
	}
	out.WriteString(text[cursor:])
	mapped := make([]GeneratedMaskSpan, len(spans))
	next, delta := 0, 0
	for i, span := range spans {
		for next < len(edits) && edits[next].end <= span.Start {
			edit := edits[next]
			delta += len(marker) - (edit.end - edit.start)
			next++
		}
		mapped[i] = GeneratedMaskSpan{span.Start + delta, span.End + delta}
	}
	return out.String(), mapped
}

func credentialTokenByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("._~+/=-", rune(c))
}
