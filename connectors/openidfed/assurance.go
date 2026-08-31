// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package openidfed

import (
	"regexp"
	"strings"
)

// Assurance is the declared NIST SP 800-63-4 assurance of a federated identity,
// as a GOVERNANCE TAG (not enforcement). SP 800-63-4 (Final, July 2025) defines
// three families, each with levels 1–3:
//
//   - IAL (Identity Assurance Level): confidence the claimed identity is the
//     person's real-world identity (proofing). IAL1 < IAL2 < IAL3.
//   - AAL (Authentication Assurance Level): confidence the claimant controls the
//     bound authenticator(s). AAL1 < AAL2 < AAL3.
//   - FAL (Federation Assurance Level): strength of the assertion protocol binding
//     in a federation transaction. FAL1 < FAL2 < FAL3.
//
// Honesty: SP 800-63-4 does NOT define how IAL/AAL/FAL are CONVEYED
// in an assertion — acr/amr is OpenID Connect Core, eduPersonAssurance/the REFEDS
// Assurance Framework (RAF) is REFEDS, and a NIST↔RAF mapping is stated in neither.
// So this mapper extracts only EXPLICIT level declarations (a value naming IALn /
// AALn / FALn) and passes RAF/eduPersonAssurance values through UNMAPPED — it never
// fabricates a NIST level from a RAF profile. The RAF→NIST mapping is post-v1.
type Assurance struct {
	// IAL/AAL/FAL are the highest explicitly-declared level in each family ("" when
	// none was declared), normalized to "IAL2"/"AAL2"/"FAL2" form.
	IAL string
	AAL string
	FAL string
	// RawAssurance holds the declared assurance values that name no explicit NIST
	// level (e.g. REFEDS RAF profile URIs); they are surfaced unmapped, never
	// silently coerced to a NIST level.
	RawAssurance []string
}

// levelRe matches an explicit assurance-level token: ial2, AAL3, fal-1, etc.
var levelRe = regexp.MustCompile(`(?i)\b(ial|aal|fal)[ _-]?([123])\b`)

// MapAssurance derives the assurance tag from a flat set of declared assurance
// values (collected from a login assertion's acr/amr/assurance claims and/or
// eduPersonAssurance). It is a pure function. It keeps the HIGHEST explicit level
// declared per family and surfaces every other declared value as RawAssurance.
func MapAssurance(declared []string) Assurance {
	var a Assurance
	highest := map[string]int{}
	matchedExplicit := map[string]bool{}
	for _, raw := range declared {
		v := strings.TrimSpace(raw)
		if v == "" {
			continue
		}
		found := false
		for _, m := range levelRe.FindAllStringSubmatch(v, -1) {
			family := strings.ToUpper(m[1])
			level := int(m[2][0] - '0')
			if level > highest[family] {
				highest[family] = level
			}
			found = true
		}
		if found {
			matchedExplicit[v] = true
		} else {
			a.RawAssurance = append(a.RawAssurance, v)
		}
	}
	if n := highest["IAL"]; n > 0 {
		a.IAL = "IAL" + string(rune('0'+n))
	}
	if n := highest["AAL"]; n > 0 {
		a.AAL = "AAL" + string(rune('0'+n))
	}
	if n := highest["FAL"]; n > 0 {
		a.FAL = "FAL" + string(rune('0'+n))
	}
	if len(a.RawAssurance) == 0 {
		a.RawAssurance = nil
	}
	return a
}

// HasAny reports whether any explicit NIST level was declared.
func (a Assurance) HasAny() bool { return a.IAL != "" || a.AAL != "" || a.FAL != "" }
