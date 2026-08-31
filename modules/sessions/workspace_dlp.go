// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"unicode/utf8"
)

// workspace_dlp.go declares the DLP CLASSIFIER seam for governed file reads (FASE V). The module declares the seam in its OWN terms and NEVER imports
// modules/knowledge or modules/security (no inter-module imports): cmd/olivares wires
// an adapter over security.ClassifySensitivity behind this interface (the proven
// composition-root pattern, like knowledgeclassifier.go). The default is nil — a read
// in `label` mode then returns no labels (degraded but functional); a read in `deny`
// mode fails closed (cannot prove the content safe).

// SensitivityHit is one DLP finding over a file's content. It carries the
// DLP-facing class and an explainability rule, the match count, and a severity —
// NEVER the raw matched value (minimal-data, docs/SECURITY-HARDENING.md).
type SensitivityHit struct {
	Class    string `json:"class"`
	Rule     string `json:"rule,omitempty"`
	Count    int    `json:"count,omitempty"`
	Severity string `json:"severity,omitempty"`
}

// Classifier discovers sensitive content (PII/secrets) in text. The production
// adapter wraps the deterministic catalog; a richer classifier can be swapped
// behind the same seam. It MUST be a pure, zero-egress classification (no network).
type Classifier interface {
	Classify(text string) ([]SensitivityHit, error)
}

// ClassifierFunc adapts a function to a Classifier (cmd/olivares wraps the catalog;
// tests inject a deterministic stub).
type ClassifierFunc func(text string) ([]SensitivityHit, error)

// Classify calls the wrapped function.
func (f ClassifierFunc) Classify(text string) ([]SensitivityHit, error) { return f(text) }

// classBinary marks content that could not be classified because it is not valid
// UTF-8 text (binary). In `deny` mode unscannable content is denied (fail-closed,
// matching "unscanned denies"); in `label` mode it is reported and allowed.
const classBinary = "binary"

// classifyContent applies the workspace's DLP posture to content read from a file. It
// returns the sensitivity hits to surface and whether the read must be DENIED. It
// never returns content (the caller decides what to send); it only classifies.
//
//   - off:   no classification, never denied.
//   - label: classify UTF-8 text (if a classifier is wired); binary is labeled; never denied.
//   - deny:  deny-closed. No classifier wired ⇒ deny. Binary ⇒ deny (unscannable).
//     Text with ≥1 hit ⇒ deny. Clean text ⇒ allow.
func (m *Module) classifyContent(_ context.Context, mode string, content []byte) (hits []SensitivityHit, deny bool) {
	switch mode {
	case dlpOff:
		return nil, false
	case dlpDeny:
		if m.rt.classifier == nil {
			return nil, true // cannot prove safe → deny-closed
		}
		if !utf8.Valid(content) {
			return []SensitivityHit{{Class: classBinary}}, true // unscannable → deny
		}
		hs, err := m.rt.classifier.Classify(string(content))
		if err != nil {
			return nil, true // classification failed → deny (never read-through on error)
		}
		return hs, len(hs) > 0
	default: // dlpLabel
		if !utf8.Valid(content) {
			return []SensitivityHit{{Class: classBinary}}, false
		}
		if m.rt.classifier == nil {
			return nil, false // no classifier wired: no labels, still returned
		}
		hs, err := m.rt.classifier.Classify(string(content))
		if err != nil {
			return nil, false // label mode never blocks; a classifier error just omits labels
		}
		return hs, false
	}
}

// dlpClasses extracts the distinct classes from hits (for the non-sensitive audit
// meta — the classes, never the content).
func dlpClasses(hits []SensitivityHit) []string {
	if len(hits) == 0 {
		return nil
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(hits))
	for _, h := range hits {
		if h.Class != "" && !seen[h.Class] {
			seen[h.Class] = true
			out = append(out, h.Class)
		}
	}
	return out
}
