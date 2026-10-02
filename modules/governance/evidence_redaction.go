// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance

import (
	"bytes"
	"encoding/json"
	"errors"

	"github.com/olivaresai/olivares/connectors/redact"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

// redactPDPText is the persistence/logging seam, never an evaluator input.
// Run-specific values and the shared pattern floor cover different secrets.
func redactPDPText(runRedactor func(string) string, text string) string {
	if len(text) > model.MaxPolicyArtifactBytes {
		return "[REDACTED]"
	}
	if runRedactor != nil {
		text = runRedactor(text)
	}
	// Use the shared whole-value floor for strings too: a partial shell-token
	// scrub can leave the suffix of a quoted password behind in a log.
	raw, _ := json.Marshal(text) // A string always has a JSON representation.
	if len(raw) > model.MaxPolicyArtifactBytes {
		return "[REDACTED]"
	}
	clean, err := redact.CleanJSON(raw)
	if err != nil || json.Unmarshal(clean, &text) != nil {
		return "[REDACTED]"
	}
	return text
}

// redactRetainedAuthorization copies the captured inputs before persistence.
// Decode strings before the run scrub: a JSON escape must not hide a vault value.
// Keys can contain arbitrary resource IDs too. Drop a changed key rather than
// rename it and potentially merge two facts. Neither substitute is replayable.
func redactRetainedAuthorization(snapshot auth.RetainedAuthorization, runRedactor func(string) string) (auth.RetainedAuthorization, error) {
	raw, err := json.Marshal(snapshot)
	if err != nil {
		return auth.RetainedAuthorization{}, err
	}
	if len(raw) > model.MaxPolicyArtifactBytes {
		return auth.RetainedAuthorization{}, errors.New("governance: retained authorization inputs exceed artifact limit")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return auth.RetainedAuthorization{}, err
	}
	changed := false
	var scrub func(any) any
	scrub = func(value any) any {
		switch v := value.(type) {
		case map[string]any:
			for key, child := range v {
				if redactPDPText(runRedactor, key) != key {
					delete(v, key)
					changed = true
					continue
				}
				v[key] = scrub(child)
			}
		case []any:
			for i, child := range v {
				v[i] = scrub(child)
			}
		case string:
			clean := redactPDPText(runRedactor, v)
			changed = changed || clean != v
			return clean
		}
		return value
	}
	clean, err := json.Marshal(scrub(value))
	if err != nil {
		return auth.RetainedAuthorization{}, err
	}
	if len(clean) > model.MaxPolicyArtifactBytes {
		return auth.RetainedAuthorization{}, errors.New("governance: redacted authorization inputs exceed artifact limit")
	}
	// Structured sensitive values (e.g. Extra["password"]) are removed whole by
	// the existing shared scrubber, even if the value has no recognizable shape.
	floor, err := redact.CleanJSON(clean)
	if err != nil {
		return auth.RetainedAuthorization{}, err
	}
	if len(floor) > model.MaxPolicyArtifactBytes {
		return auth.RetainedAuthorization{}, errors.New("governance: redacted authorization inputs exceed artifact limit")
	}
	changed = changed || !bytes.Equal(clean, floor)
	var retained auth.RetainedAuthorization
	if err := json.Unmarshal(floor, &retained); err != nil {
		return auth.RetainedAuthorization{}, err
	}
	if changed {
		retained.InputRedacted = true
		retained.Complete = false
	}
	return retained, nil
}
