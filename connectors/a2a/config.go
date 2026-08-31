// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package a2a

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/olivaresai/olivares/sdk"
)

// Name is the connector's globally unique dotted identifier.
const Name = "olivares.a2a"

// version is the connector's own semantic version.
const version = "0.1.0"

// Configuration keys (declared in the Descriptor, read in Open).
const (
	cfgAgents       = "agents"
	cfgInteractions = "interactions"
	cfgTimeout      = "timeout"
	cfgWellKnown    = "well_known_path"
	cfgAllowJKU     = "allow_jku_fetch"
)

// defaultTimeout bounds a single Agent Card fetch.
const defaultTimeout = 30 * time.Second

// defaultWellKnownPath is the A2A v1.0 recommended Agent Card discovery path
// (RFC 8615). It is RECOMMENDED, not mandatory; an operator can override it or
// point an agent's url directly at a card document.
const defaultWellKnownPath = "/.well-known/agent-card.json"

// agentSpec is one A2A agent to discover and trust-verify. URL is the agent's base
// URL (the well-known card path is appended) or a direct card URL. TrustJWKS is the
// operator's OUT-OF-BAND trust anchor (a JWK Set): a card signature that verifies
// against it is attributed; without it the card is at best self-asserted. Headers
// are optional request headers for fetching the card (read-only).
type agentSpec struct {
	Name      string            `json:"name"`
	URL       string            `json:"url"`
	TrustJWKS json.RawMessage   `json:"trust_jwks"`
	Headers   map[string]string `json:"headers"`
}

// interactionSpec is one OBSERVED A2A task/message between two agents, captured
// out-of-band (e.g. from a push-notification sink or gateway log — read-only, never
// in the data path). It becomes one agent↔agent edge into module IV. From/To are
// agent references; State is a TASK_STATE_* value; Skill optionally names the skill
// invoked.
type interactionSpec struct {
	From      string `json:"from"`
	To        string `json:"to"`
	TaskID    string `json:"task_id"`
	ContextID string `json:"context_id"`
	State     string `json:"state"`
	Skill     string `json:"skill"`
}

// config is the resolved connector configuration.
type config struct {
	agents        []agentSpec
	interactions  []interactionSpec
	timeout       time.Duration
	wellKnownPath string
	allowJKU      bool
}

// descriptor is the connector's stable self-description.
func descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:        Name,
		Version:     version,
		APIVersion:  sdk.APIVersion,
		Type:        sdk.TypeSource,
		Title:       "A2A observation",
		Description: "Observes Agent2Agent (A2A) v1.0 read-only: Agent Card discovery + JWS/JCS signature verification + Task-lifecycle edges into the orchestration graph.",
		ConfigFields: []sdk.ConfigField{
			{Key: cfgAgents, Type: sdk.FieldString, Description: "inline JSON array of A2A agent specs to discover and trust-verify"},
			{Key: cfgInteractions, Type: sdk.FieldString, Description: "inline JSON array of observed A2A task/message records (agent↔agent edges)"},
			{Key: cfgTimeout, Type: sdk.FieldDuration, Default: defaultTimeout.String(), Description: "per-card fetch timeout"},
			{Key: cfgWellKnown, Type: sdk.FieldString, Default: defaultWellKnownPath, Description: "Agent Card well-known path appended to an agent base URL"},
			{Key: cfgAllowJKU, Type: sdk.FieldBool, Default: "false", Description: "allow verifying a card against a key fetched from its own jku (self-asserted, lower trust; off by default)"},
		},
	}
}

// loadConfig resolves the connector configuration. At least one of agents or
// interactions must be present (otherwise there is nothing to observe).
func loadConfig(cfg sdk.Config) (config, error) {
	c := config{
		timeout:       cfg.GetDuration(cfgTimeout, defaultTimeout),
		wellKnownPath: cfg.Get(cfgWellKnown),
		allowJKU:      cfg.GetBool(cfgAllowJKU, false),
	}
	if c.timeout <= 0 {
		c.timeout = defaultTimeout
	}
	if c.wellKnownPath == "" {
		c.wellKnownPath = defaultWellKnownPath
	}

	if raw := cfg.Get(cfgAgents); raw != "" {
		if err := json.Unmarshal([]byte(raw), &c.agents); err != nil {
			return config{}, fmt.Errorf("a2a: parse %q: %w", cfgAgents, err)
		}
	}
	if raw := cfg.Get(cfgInteractions); raw != "" {
		if err := json.Unmarshal([]byte(raw), &c.interactions); err != nil {
			return config{}, fmt.Errorf("a2a: parse %q: %w", cfgInteractions, err)
		}
	}
	if len(c.agents) == 0 && len(c.interactions) == 0 {
		return config{}, fmt.Errorf("a2a: nothing to observe (set %q and/or %q)", cfgAgents, cfgInteractions)
	}
	for i := range c.agents {
		if c.agents[i].Name == "" {
			return config{}, fmt.Errorf("a2a: agent #%d has no name", i)
		}
	}
	return c, nil
}
