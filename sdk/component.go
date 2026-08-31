// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package sdk

import (
	"strconv"
	"time"
)

// APIVersion is the SDK contract version this package describes. It is advisory
// metadata surfaced in a Descriptor; the hard compatibility gate between a host
// and an out-of-process plugin is the go-plugin protocol version (see the
// sdk/plugin module), which equals the protobuf major version. Bump APIVersion
// when the Go interface surface changes in a way authors should notice.
const APIVersion = "v1"

// ComponentType is what a component is: a source connector, an output connector
// or a module. The engine uses it to wire the component correctly.
type ComponentType string

// The component types.
const (
	// TypeSource is a SourceConnector: it gathers facts from a system and emits
	// observations.
	TypeSource ComponentType = "source"
	// TypeOutput is an OutputConnector: it delivers notifications to a system.
	TypeOutput ComponentType = "output"
	// TypeContentSource is a ContentSource: it serves documents and permission
	// references to the governed knowledge module.
	TypeContentSource ComponentType = "content-source"
	// TypeModule is a Module: it consumes events and implements product logic.
	TypeModule ComponentType = "module"
)

// Valid reports whether t is a known component type.
func (t ComponentType) Valid() bool {
	switch t {
	case TypeSource, TypeOutput, TypeContentSource, TypeModule:
		return true
	default:
		return false
	}
}

// ConfigFieldType is the declared type of a configuration field, so the engine
// can validate and the UI can render the right control.
type ConfigFieldType string

// The configuration field types.
const (
	// FieldString is free text.
	FieldString ConfigFieldType = "string"
	// FieldInt is an integer.
	FieldInt ConfigFieldType = "int"
	// FieldBool is a boolean.
	FieldBool ConfigFieldType = "bool"
	// FieldDuration is a Go duration string (e.g. "30s").
	FieldDuration ConfigFieldType = "duration"
)

// ConfigField declares one configuration setting a component accepts. It lets
// the engine validate configuration and a UI render a form, without the
// component running. Secrets are declared (Secret=true); the operator supplies a
// secret REFERENCE ("<scheme>:<locator>"), never the literal, and the engine's
// resolver opens it to the live value at Open — an inline secret is refused
// (docs/SECURITY-HARDENING.md, core/secret).
type ConfigField struct {
	// Key is the setting key (looked up in Config.Settings).
	Key string
	// Type is the declared value type.
	Type ConfigFieldType
	// Required fails validation if the setting is absent.
	Required bool
	// Default is the value used when the setting is absent and not required.
	Default string
	// Secret marks a sensitive field: its operator-supplied value is a secret
	// REFERENCE ("<scheme>:<locator>"), never the secret itself — the engine
	// resolves it to the live value at Open and refuses an inline literal —
	// so the UI masks it and logs never print it.
	Secret bool
	// Description is a short human explanation.
	Description string
}

// Descriptor is the stable self-description every component returns. It is what
// the engine registers, displays and version-checks. Name must be globally
// unique and dotted ("<vendor>.<component>", e.g. "olivares.pg-audit").
type Descriptor struct {
	// Name is the globally unique dotted identifier.
	Name string
	// Version is the component's own semantic version.
	Version string
	// APIVersion is the SDK contract version it was built against (see APIVersion).
	APIVersion string
	// Type is what kind of component this is.
	Type ComponentType
	// Title is a short human label.
	Title string
	// Description is a one-line human summary.
	Description string
	// ConfigFields declares the configuration the component accepts (may be nil).
	ConfigFields []ConfigField
	// Surfaces declares the governance surfaces this component materializes
	// (open vocabulary, e.g. "knowledge.document", "observation.edge",
	// "observation.cost", "observation.finding", "notify.sink"). It is advisory
	// metadata for humans, catalogs and admission UIs only; it is never an
	// enforcement input. Enforcement binds to the configured source identity.
	Surfaces []string
}

// Config is the RESOLVED configuration handed to a component at Open/Init: a flat
// string map (so it crosses the gRPC wire unchanged), typed access via the
// getters. A secret value reaches the component as a live value, but the operator
// never wrote it inline — they wrote a reference ("<scheme>:<locator>", e.g.
// store:gdrive-token or vault:secret/data/gdrive#token) that the engine's secret
// resolver (core/secret) opened to the live value just before this Config
// was built. An inline literal in a field declared Secret is refused (docs/SECURITY-HARDENING.md).
type Config struct {
	// Settings is the resolved key/value configuration.
	Settings map[string]string
}

// Get returns the setting for key, or "" if absent.
func (c Config) Get(key string) string { return c.Settings[key] }

// Lookup returns the setting for key and whether it was present.
func (c Config) Lookup(key string) (string, bool) {
	v, ok := c.Settings[key]
	return v, ok
}

// GetInt returns the setting parsed as an int, or def if absent or unparseable.
func (c Config) GetInt(key string, def int) int {
	v, ok := c.Settings[key]
	if !ok {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

// GetBool returns the setting parsed as a bool, or def if absent or unparseable.
func (c Config) GetBool(key string, def bool) bool {
	v, ok := c.Settings[key]
	if !ok {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return def
	}
	return b
}

// GetDuration returns the setting parsed as a time.Duration, or def if absent or
// unparseable.
func (c Config) GetDuration(key string, def time.Duration) time.Duration {
	v, ok := c.Settings[key]
	if !ok {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return def
	}
	return d
}
