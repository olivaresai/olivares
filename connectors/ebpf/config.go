// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package ebpf

import (
	"strings"
	"time"

	"github.com/olivaresai/olivares/sdk"
)

// Name is the connector's globally unique dotted identifier.
const Name = "olivares.ebpf"

// version is the connector's own semantic version.
const version = "0.1.0"

// stdinPath is the events_path value that selects standard input as the event
// source, so the connector can be fed by a pipe (e.g. `tetra getevents -o json |
// ebpf-source`).
const stdinPath = "-"

// Configuration keys (declared in the Descriptor, read in Open).
const (
	cfgEventsPath    = "events_path"
	cfgFollow        = "follow"
	cfgDetectEvasion = "detect_evasion"
	cfgEvasionWindow = "evasion_window"
	cfgAgentSigs     = "agent_signatures"
	cfgOTLPEndpoints = "otlp_endpoints"
)

// Configuration defaults.
const (
	defaultEventsPath    = stdinPath
	defaultFollow        = true
	defaultDetectEvasion = false
	defaultEvasionWindow = 5 * time.Minute
	// defaultAgentSignatures classifies the cooperative agents this product
	// targets. Matching is by executable base name or argv token (see
	// classifier in evasion.go); it is deliberately small and only affects the
	// off-by-default anti-evasion detector, never edge emission.
	defaultAgentSignatures = "claude,claude-code"
	// defaultOTLPEndpoints mirrors the OpenTelemetry conventions (4317 gRPC,
	// 4318 HTTP) and the addresses the cooperative connector binds, so a
	// locally-collected agent is recognized as cooperative out of the box.
	defaultOTLPEndpoints = "127.0.0.1:4317,127.0.0.1:4318"
)

// config is the resolved, validated connector configuration.
type config struct {
	eventsPath    string
	follow        bool
	detectEvasion bool
	evasionWindow time.Duration
	agentSigs     []string
	otlpEndpoints []string
}

// descriptor is the connector's stable self-description.
func descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:        Name,
		Version:     version,
		APIVersion:  sdk.APIVersion,
		Type:        sdk.TypeSource,
		Title:       "eBPF backstop (Tetragon)",
		Description: "Consumes the Tetragon kernel event stream; emits ground-truth R/RW file and network access edges and an optional anti-evasion finding.",
		ConfigFields: []sdk.ConfigField{
			{Key: cfgEventsPath, Type: sdk.FieldString, Default: defaultEventsPath, Description: "path to the Tetragon JSON event stream (a file or FIFO Tetragon writes), or \"-\" for standard input (the default)"},
			{Key: cfgFollow, Type: sdk.FieldBool, Default: "true", Description: "keep reading as the stream grows (tail a file / block on a FIFO); false reads to EOF once"},
			{Key: cfgDetectEvasion, Type: sdk.FieldBool, Default: "false", Description: "emit anti-evasion findings when an agent acts at the kernel without cooperative telemetry (heuristic, off by default; see docs/08 §6)"},
			{Key: cfgEvasionWindow, Type: sdk.FieldDuration, Default: defaultEvasionWindow.String(), Description: "grace period from an agent's first observed activity before a missing cooperative connection is flagged"},
			{Key: cfgAgentSigs, Type: sdk.FieldString, Default: defaultAgentSignatures, Description: "comma-separated executable base names / argv tokens that classify a process as a cooperative agent (anti-evasion only)"},
			{Key: cfgOTLPEndpoints, Type: sdk.FieldString, Default: defaultOTLPEndpoints, Description: "comma-separated host:port cooperative-telemetry endpoints; a connection to one marks an agent cooperative (anti-evasion only)"},
		},
	}
}

// loadConfig reads the resolved settings, applying defaults. An out-of-range
// duration falls back to its default rather than failing: a misconfiguration
// should degrade, not crash a read-only collector. Validation that must hard-fail
// (a missing event source) happens in Open, not here.
func loadConfig(cfg sdk.Config) config {
	c := config{
		eventsPath:    firstNonEmpty(cfg.Get(cfgEventsPath), defaultEventsPath),
		follow:        cfg.GetBool(cfgFollow, defaultFollow),
		detectEvasion: cfg.GetBool(cfgDetectEvasion, defaultDetectEvasion),
		evasionWindow: cfg.GetDuration(cfgEvasionWindow, defaultEvasionWindow),
		agentSigs:     splitList(firstNonEmpty(cfg.Get(cfgAgentSigs), defaultAgentSignatures)),
		otlpEndpoints: splitList(firstNonEmpty(cfg.Get(cfgOTLPEndpoints), defaultOTLPEndpoints)),
	}
	if c.evasionWindow <= 0 {
		c.evasionWindow = defaultEvasionWindow
	}
	return c
}

// firstNonEmpty returns a if non-empty, else b.
func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// splitList parses a comma-separated setting into a trimmed, non-empty slice.
func splitList(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}
