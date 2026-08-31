// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sandbox

import "encoding/json"

// The store persists a KindJSON column as deterministic text (ARCHITECTURE.md, model
// KindJSON). These helpers (de)serialize a scenario's synthetic steps/mocks to and
// from that text. They never carry secrets — a scenario is an operator-authored
// fixture (docs/SECURITY-HARDENING.md) — and they fail soft (a malformed/empty column decodes to an
// empty slice) so a read never errors on stored data.

func encodeSteps(steps []stepDTO) string {
	if len(steps) == 0 {
		return "[]"
	}
	b, err := json.Marshal(steps)
	if err != nil {
		return "[]"
	}
	return string(b)
}

func decodeSteps(s string) []stepDTO {
	out := []stepDTO{}
	if s == "" {
		return out
	}
	_ = json.Unmarshal([]byte(s), &out)
	return out
}

func encodeMocks(mocks []mockDTO) string {
	if len(mocks) == 0 {
		return "[]"
	}
	b, err := json.Marshal(mocks)
	if err != nil {
		return "[]"
	}
	return string(b)
}

func decodeMocks(s string) []mockDTO {
	out := []mockDTO{}
	if s == "" {
		return out
	}
	_ = json.Unmarshal([]byte(s), &out)
	return out
}
