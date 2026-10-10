// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sandbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// templateSyntheticData expands only two literal placeholders; it has no store,
// network, secret, template evaluation or executable-code capability.
type templateSyntheticData struct{}

func (templateSyntheticData) Generate(ctx context.Context, _ model.TenantID, spec GenSpec) ([]GenSample, error) {
	if spec.Count == 0 {
		spec.Count = 10
	}
	if spec.SubjectKind == "" {
		spec.SubjectKind = "agent"
	}
	if spec.Seed == "" {
		spec.Seed = "{{subject_kind}}-sample-{{index}}"
	}
	if spec.Count < 1 || spec.Count > 100 {
		return nil, errors.New("count must be between 1 and 100")
	}
	if len(spec.SubjectKind) > maxNameLen || len(spec.Seed) > maxStepLen {
		return nil, errors.New("subject_kind must fit in 200 bytes and seed in 8192 bytes")
	}
	samples := make([]GenSample, 0, spec.Count)
	for i := 1; i <= spec.Count; i++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		input := strings.NewReplacer("{{index}}", strconv.Itoa(i), "{{subject_kind}}", spec.SubjectKind).Replace(spec.Seed)
		if len(input) > maxStepLen {
			return nil, errors.New("each generated input must fit in 8192 bytes")
		}
		samples = append(samples, GenSample{Key: fmt.Sprintf("sample-%04d", i), Input: input})
	}
	return samples, nil
}

// handleGenerateSynthetic generates bounded local template samples for scenario
// steps and audits only their count, without storing inputs or making network requests.
func (m *Module) handleGenerateSynthetic(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	var req struct {
		SubjectKind string `json:"subject_kind"`
		Count       int    `json:"count"`
		Seed        string `json:"seed"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	var generator SyntheticDataGenerator = templateSyntheticData{}
	samples, err := generator.Generate(r.Context(), mc.Tenant, GenSpec{SubjectKind: req.SubjectKind, Count: req.Count, Seed: req.Seed})
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody(err.Error()))
		return
	}
	steps := make([]stepDTO, len(samples))
	for i, sample := range samples {
		steps[i] = stepDTO{Key: sample.Key, Input: sample.Input}
	}
	// Reserve the scenario's bounded metadata at its worst-case JSON escaping
	// (six bytes per control character), using the consumer's actual DTO and cap.
	scenario, err := json.Marshal(createScenarioRequest{
		Name: strings.Repeat("\x00", maxNameLen), Description: strings.Repeat("\x00", maxRefLen),
		SubjectKind: strings.Repeat("\x00", maxNameLen), Steps: steps,
	})
	if err != nil || len(scenario) > maxReqBytes {
		writeJSON(w, http.StatusBadRequest, errorBody("generated samples exceed the scenario request limit; reduce count or seed length"))
		return
	}
	// Only the count enters the audit ledger. Inputs are returned to the caller,
	// who can save them as scenario steps through the existing scenario API.
	if err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		return auditEvent(r.Context(), sc, mc, "sandbox.synthetic.generate", "sandbox.synthetic_data", "", map[string]any{"samples": len(samples)})
	}); err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Samples []stepDTO `json:"samples"`
	}{Samples: steps})
}
