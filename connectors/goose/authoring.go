// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// authoring.go is the POLICY-WRITE surface for Goose: it generates a profiles.yaml
// fragment from governance rules that the operator distributes to the Goose install.
// The generated profile pins provider, model, tool approval, allowed tools, and
// extension allowlist — the governance-relevant subset of profiles.yaml.
//
// This is ADVISORY governance: Goose has no admin/system settings tier. The generated
// profile is a recommendation file that the operator deploys to ~/.config/goose/;
// the user can override it by editing the file directly. The connector documents this
// limitation honestly.
package goose

import "gopkg.in/yaml.v3"

// Policy is the governance-authored intent for a Goose profile: the desired
// configuration posture expressed in clean, typed form. It is the input to Render.
type Policy struct {
	// ProfileName is the target profile name. Empty defaults to "default".
	ProfileName string
	// Provider pins the LLM provider. Empty = not governed.
	Provider string
	// Model pins the LLM model. Empty = not governed.
	Model string
	// RequireApproval enforces tool execution confirmation. nil = not governed.
	RequireApproval *bool
	// AllowedTools is the tool allowlist. Empty = not governed.
	AllowedTools []string
	// Extensions is the MCP server/extension allowlist. Empty = not governed.
	Extensions map[string]PolicyExtension
}

// PolicyExtension is one governed extension (MCP server) entry.
type PolicyExtension struct {
	Type    string `yaml:"type,omitempty"`
	Command string `yaml:"command,omitempty"`
	URL     string `yaml:"url,omitempty"`
}

type renderedProfile struct {
	Provider   string                     `yaml:"provider,omitempty"`
	Model      string                     `yaml:"model,omitempty"`
	Extensions map[string]PolicyExtension `yaml:"extensions,omitempty"`
	Toolshim   *renderedToolshim          `yaml:"toolshim,omitempty"`
}

type renderedToolshim struct {
	RequireApproval *bool    `yaml:"require_approval,omitempty"`
	AllowedTools    []string `yaml:"allowed_tools,omitempty"`
}

// Render produces a profiles.yaml document from the governance-authored Policy.
// The output is valid YAML containing only the governed profile. Empty policy = minimal.
func Render(p Policy) ([]byte, error) {
	profileName := p.ProfileName
	if profileName == "" {
		profileName = "default"
	}

	profile := renderedProfile{
		Provider:   p.Provider,
		Model:      p.Model,
		Extensions: p.Extensions,
	}

	hasToolshim := p.RequireApproval != nil || len(p.AllowedTools) > 0
	if hasToolshim {
		profile.Toolshim = &renderedToolshim{
			RequireApproval: p.RequireApproval,
			AllowedTools:    p.AllowedTools,
		}
	}

	doc := map[string]renderedProfile{profileName: profile}
	return yaml.Marshal(doc)
}
