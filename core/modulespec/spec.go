// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package modulespec holds the built-in module table without importing modules.
// The constructor binds each row to its existing routes, permissions and schema
// (including migrations); those declarations stay in the module that owns them.
package modulespec

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/olivaresai/olivares/sdk"
)

// Spec describes one API namespace. Order in the table is registration order.
// Selectable preserves the profile catalog separately from the kernel: edition
// modules have always run outside the administrator's selectable catalog.
// ConsoleEntries names the console views (registry ids) whose page follows this
// module's state; one row at most names a view.
type Spec struct {
	Namespace         string                      `json:"namespace"`
	Name              string                      `json:"name"`
	Package           string                      `json:"package"`
	Constructor       string                      `json:"constructor"`
	ConstructorInputs map[string]ConstructorInput `json:"constructor_inputs,omitempty"`
	Edition           string                      `json:"edition"`
	EditionSlot       bool                        `json:"edition_slot,omitempty"`
	Kind              string                      `json:"kind"`
	Requires          []string                    `json:"requires"`
	Standard          bool                        `json:"standard"`
	Selectable        bool                        `json:"selectable"`
	Published26100    bool                        `json:"published_26100"`
	ConfigDefaults    sdk.Config                  `json:"config_defaults"`
	ConsoleEntries    []string                    `json:"console_entries"`
	RetirementOrder   int                         `json:"retirement_order,omitempty"`
	DeliveryName      string                      `json:"delivery_name,omitempty"`
	DeliveryClass     string                      `json:"delivery_class,omitempty"`
}

// ConstructorInput binds a required constructor argument to an earlier row.
// Type is its Go type, checked by the generated binding before construction.
type ConstructorInput struct {
	Namespace string `json:"namespace"`
	Type      string `json:"type"`
}

//go:embed modules.json
var document []byte

// All returns an owned copy, so callers cannot change another reader's policy.
// An invalid built-in table is a build defect; never continue with an empty list.
func All() []Spec {
	var specs []Spec
	if err := json.Unmarshal(document, &specs); err != nil {
		panic(fmt.Sprintf("invalid built-in module spec: %v", err))
	}
	return specs
}

// DefaultConfig is the configuration passed to the existing module lifecycle.
// Edition modules absent from this table retain the empty configuration.
func DefaultConfig(namespace string) sdk.Config {
	for _, spec := range All() {
		if spec.Namespace == namespace {
			return spec.ConfigDefaults
		}
	}
	return sdk.Config{}
}

// RetirementModules preserves the declared order, including an unavailable
// implementation so the account-retirement census fails closed.
func RetirementModules() []string {
	var specs []Spec
	for _, spec := range All() {
		if spec.RetirementOrder > 0 {
			specs = append(specs, spec)
		}
	}
	slices.SortFunc(specs, func(a, b Spec) int { return a.RetirementOrder - b.RetirementOrder })
	names := make([]string, len(specs))
	for i, spec := range specs {
		names[i] = spec.Namespace
	}
	return names
}
