// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package hostops

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
)

// InputField is the bounded scalar vocabulary for a task's closed input object.
// Secret values must be store references; descriptors never hold credentials.
type InputField struct {
	Type string   `json:"type"`
	Enum []string `json:"enum,omitempty"`
}

// InputSchema defines the inputs all three renderers ask for.
type InputSchema struct {
	Type                 string                `json:"type"`
	Properties           map[string]InputField `json:"properties"`
	Required             []string              `json:"required"`
	AdditionalProperties bool                  `json:"additionalProperties"`
}

// Descriptor is the single task definition rendered by web, CLI and TUI.
type Descriptor struct {
	ID                string      `json:"id"`
	Module            string      `json:"module"`
	Verb              string      `json:"verb"`
	Title             string      `json:"title"`
	Category          string      `json:"category"`
	InputSchema       InputSchema `json:"input_schema"`
	Preconditions     []string    `json:"preconditions"`
	Consequence       string      `json:"consequence"`
	Confirmation      string      `json:"confirmation"`
	Modes             []string    `json:"modes"`
	ActVerb           string      `json:"act_verb"`
	Audience          string      `json:"audience"`
	EquivalentCommand string      `json:"equivalent_command"`
	OutputSchema      string      `json:"output_schema"`
	Surfaces          []string    `json:"surfaces"`
}

// Catalog owns a validated copy of every descriptor. Undeclared tasks and
// surfaces are refused, so adding a module requires its complete descriptor.
type Catalog struct{ entries map[string]Descriptor }

// NewCatalog refuses incomplete parity and open input schemas.
func NewCatalog(descriptors []Descriptor) (*Catalog, error) {
	c := &Catalog{entries: map[string]Descriptor{}}
	for _, d := range descriptors {
		key := d.Module + "." + d.Verb
		if _, exists := c.entries[key]; exists || !matchToken(d.ID, true) || !matchName(d.Module) || !matchName(d.Verb) || !plainText(d.Title) || !plainText(d.Consequence) || !plainText(d.EquivalentCommand) || !oneOf(d.Confirmation, "none", "confirm", "typed") || d.OutputSchema != "hostop-v1.schema.json" || !oneOf(d.Category, "Status", "Network", "Services", "Packages", "Updates", "Storage", "System", "License and modules", "Apps", "Tools and accounts", "Support") {
			return nil, errors.New("descriptor_refused")
		}
		if d.Preconditions == nil || d.InputSchema.Required == nil {
			return nil, errors.New("descriptor_refused")
		}
		for _, p := range d.Preconditions {
			if !plainText(p) {
				return nil, errors.New("descriptor_refused")
			}
		}
		if d.InputSchema.Type != "object" || d.InputSchema.AdditionalProperties || d.InputSchema.Properties == nil || len(d.Modes) == 0 || len(d.Surfaces) != 3 {
			return nil, errors.New("descriptor_refused")
		}
		for _, surface := range []string{"web", "cli", "tui"} {
			if !slices.Contains(d.Surfaces, surface) {
				return nil, errors.New("descriptor_refused")
			}
		}
		for _, mode := range d.Modes {
			if !oneOf(mode, "product-up", "product-down-repair", "unavailable", "tty1", "timer", "guard", "projection") {
				return nil, errors.New("descriptor_refused")
			}
		}
		for name, field := range d.InputSchema.Properties {
			if !matchToken(strings.ReplaceAll(name, "_", "-"), false) || !oneOf(field.Type, "string", "boolean", "integer") || (len(field.Enum) > 0 && field.Type != "string") {
				return nil, errors.New("descriptor_refused")
			}
		}
		for _, name := range d.InputSchema.Required {
			if _, ok := d.InputSchema.Properties[name]; !ok {
				return nil, errors.New("descriptor_refused")
			}
		}
		if !allowedAudience(d.ActVerb, d.Audience) || (d.Confirmation != "none" && d.ActVerb == "") {
			return nil, errors.New("descriptor_refused")
		}
		c.entries[key] = cloneDescriptor(d)
	}
	return c, nil
}

// Describe returns a copy, not a surface's private definition.
func (c *Catalog) Describe(module, verb, surface string) (Descriptor, error) {
	if c == nil || !oneOf(surface, "web", "cli", "tui") {
		return Descriptor{}, errors.New("input_refused")
	}
	d, ok := c.entries[module+"."+verb]
	if !ok {
		return Descriptor{}, errors.New("input_refused")
	}
	return cloneDescriptor(d), nil
}

// Descriptors lists tasks in a stable order for every renderer.
func (c *Catalog) Descriptors() []Descriptor {
	if c == nil {
		return nil
	}
	keys := make([]string, 0, len(c.entries))
	for k := range c.entries {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	out := make([]Descriptor, 0, len(keys))
	for _, k := range keys {
		out = append(out, cloneDescriptor(c.entries[k]))
	}
	return out
}

func cloneDescriptor(d Descriptor) Descriptor {
	b, _ := json.Marshal(d)
	var copy Descriptor
	_ = json.Unmarshal(b, &copy)
	return copy
}
func plainText(s string) bool {
	if s == "" || len(s) > 1024 {
		return false
	}
	for _, r := range s {
		if r < 32 || r == 127 {
			return false
		}
	}
	return true
}

// ValidateInput checks precisely the fields declared by this descriptor.
func (d Descriptor) ValidateInput(input json.RawMessage) error {
	var values map[string]json.RawMessage
	if err := DecodeClosed(input, &values); err != nil {
		return err
	}
	for _, name := range d.InputSchema.Required {
		if _, ok := values[name]; !ok {
			return refuse("inputs")
		}
	}
	for name, value := range values {
		field, ok := d.InputSchema.Properties[name]
		if !ok {
			return refuse("inputs")
		}
		switch field.Type {
		case "string":
			var text string
			if json.Unmarshal(value, &text) != nil || !plainText(text) || (len(field.Enum) > 0 && !slices.Contains(field.Enum, text)) {
				return refuse("inputs")
			}
		case "boolean":
			var b bool
			if json.Unmarshal(value, &b) != nil {
				return refuse("inputs")
			}
		case "integer":
			var n int64
			if json.Unmarshal(value, &n) != nil {
				return refuse("inputs")
			}
		default:
			return refuse("inputs")
		}
	}
	return nil
}

// StatusDescriptor is the initial read-only task. Module owners add their own
// complete definitions when their effects and authority paths are available.
func StatusDescriptor() Descriptor {
	return Descriptor{ID: "host.status", Module: "host", Verb: "status", Title: "Host operation status", Category: "Status", InputSchema: InputSchema{Type: "object", Properties: map[string]InputField{}, Required: []string{}}, Preconditions: []string{"Local admission or an authorized web session"}, Consequence: "Read operation status; no host setting changes.", Confirmation: "none", Modes: []string{"product-up", "product-down-repair", "unavailable"}, EquivalentCommand: "olivares-appliance host status --plan", OutputSchema: "hostop-v1.schema.json", Surfaces: []string{"web", "cli", "tui"}}
}

// allowedAudience checks descriptor consistency only. A listed consumer still
// needs product-side authorization and explicit adoption before any effect.
func allowedAudience(verb, audience string) bool {
	switch verb {
	case "":
		return audience == ""
	case "apply-update", "package":
		return audience == "appliance-helper:olivares-portal-apt"
	case "roll-back":
		return audience == "appliance-helper:olivares-portal-snapshot"
	case "network":
		return audience == "appliance-helper:olivares-portal-firewall" || audience == "appliance-netclient"
	case "vpn":
		return audience == "appliance-netclient"
	case "certificate":
		return audience == "appliance-helper:olivares-portal-cert"
	case "support-bundle":
		return audience == "appliance-helper:olivares-portal-support-bundle"
	case "power":
		return audience == "appliance-helper:olivares-portal-power"
	case "service":
		return audience == "appliance-helper:olivares-portal-units"
	case "storage":
		return audience == "appliance-helper:olivares-portal-storage"
	case "app":
		return audience == "appliance-helper:olivares-portal-apps"
	case "system":
		return audience == "appliance-helper:olivares-portal-system"
	default:
		return false
	}
}
