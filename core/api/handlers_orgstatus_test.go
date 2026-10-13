// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// TestSetOrgStatusContractDoesNotPromiseAAL3 is the copy-versus-code check the
// external contrast asked for. The AAL3 gate on this route was removed on purpose
// (see handleSetOrgStatus and the assertion above) — but the PUBLISHED OpenAPI
// description still announced "AAL3 step-up", so the contract every generated
// client and every integrator reads promised a control the server does not apply.
//
// Published copy that promises a control the code does not enforce is the forbidden
// family, and the direction matters: it is not a doc nicety, it tells an operator a
// step-up protects this route when nothing does.
//
// The check is anchored on a KNOWN POSITIVE in the same document — setOrgRegion,
// whose handler really does call requireStepUp — so it cannot pass by simply failing
// to find the string anywhere.
func TestSetOrgStatusContractDoesNotPromiseAAL3(t *testing.T) {
	h := newHarness(t)
	res := h.do(http.MethodGet, "/openapi.json", "", nil, nil)
	if res.code != http.StatusOK {
		t.Fatalf("GET /openapi.json = %d", res.code)
	}
	var doc struct {
		Paths map[string]map[string]json.RawMessage `json:"paths"`
	}
	if err := json.Unmarshal([]byte(res.raw), &doc); err != nil {
		t.Fatalf("decode spec: %v", err)
	}
	text := func(opID string) string {
		for _, ops := range doc.Paths {
			for method, raw := range ops {
				switch method {
				case "get", "put", "post", "delete", "patch", "options", "head", "trace":
				default:
					continue
				}
				var op struct {
					OperationID string `json:"operationId"`
					Summary     string `json:"summary"`
					Description string `json:"description"`
				}
				if err := json.Unmarshal(raw, &op); err != nil {
					t.Fatalf("decode %s operation: %v", method, err)
				}
				if op.OperationID == opID {
					return op.Summary + " " + op.Description
				}
			}
		}
		t.Fatalf("operation %q not found in the served spec", opID)
		return ""
	}
	// Calibration: a route that DOES enforce AAL3 still says so. Without this the
	// assertion below would also pass on a spec that had lost the phrase entirely,
	// or on a probe that never matched anything.
	if region := text("setOrgRegion"); !strings.Contains(region, "AAL3") {
		t.Fatalf("calibration failed: setOrgRegion enforces AAL3 but its contract text does not say so (%q) — "+
			"this probe cannot distinguish a fixed setOrgStatus from a spec that lost the phrase", region)
	}
	if status := text("setOrgStatus"); strings.Contains(status, "AAL3") {
		t.Fatalf("the published contract promises a control the handler does not enforce: setOrgStatus says %q, "+
			"but handleSetOrgStatus deliberately does not call requireStepUp", status)
	}
}
