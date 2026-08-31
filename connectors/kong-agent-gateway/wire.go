// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package kongagw

import (
	"bytes"
	"encoding/json"
	"io"

	"gopkg.in/yaml.v3"
)

// The wire types are a TOLERANT, DOCUMENTED view of Kong's config (see doc.go for the
// verified field list). Every field is optional and one decode path serves both the
// decK declarative shape and the Admin API entity JSON.

type kongConfig struct {
	Services  []kongService  `json:"services"`
	Routes    []kongRoute    `json:"routes"`
	Plugins   []kongPlugin   `json:"plugins"`
	Consumers []kongConsumer `json:"consumers"`
}

type kongService struct {
	ID      string       `json:"id"`
	Name    string       `json:"name"`
	Routes  []kongRoute  `json:"routes"`  // decK: routes nested under a service
	Plugins []kongPlugin `json:"plugins"` // decK: plugins nested under a service
}

type kongRoute struct {
	ID      string       `json:"id"`
	Name    string       `json:"name"`
	Service kongRef      `json:"service"`
	Plugins []kongPlugin `json:"plugins"` // decK: plugins nested under a route
}

type kongConsumer struct {
	ID       string       `json:"id"`
	Username string       `json:"username"`
	Plugins  []kongPlugin `json:"plugins"`
}

type kongPlugin struct {
	Name     string           `json:"name"`
	Enabled  *bool            `json:"enabled"` // absent => enabled (Kong default true)
	Config   kongPluginConfig `json:"config"`
	Route    kongRef          `json:"route"`
	Service  kongRef          `json:"service"`
	Consumer kongRef          `json:"consumer"`
}

// enabledOrDefault reports the plugin's enabled state (Kong defaults to true when the
// field is absent).
func (p kongPlugin) enabledOrDefault() bool { return p.Enabled == nil || *p.Enabled }

type kongPluginConfig struct {
	Model   *kongModel   `json:"model"`   // ai-proxy
	Targets []kongTarget `json:"targets"` // ai-proxy-advanced
}

type kongModel struct {
	Name     string `json:"name"`
	Provider string `json:"provider"`
}

type kongTarget struct {
	Model *kongModel `json:"model"`
}

// kongRef is a reference to a route/service/consumer that Kong serializes three ways:
// an object ({"id":...,"name":...,"username":...}), a bare string (decK name/id), or
// null. It decodes all three; ref() returns a stable non-empty identifier or "".
type kongRef struct {
	ID       string
	Name     string
	Username string
}

func (r *kongRef) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) == 0 || string(b) == "null" {
		return nil
	}
	if b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		r.Name = s
		return nil
	}
	var obj struct {
		ID       string `json:"id"`
		Name     string `json:"name"`
		Username string `json:"username"`
	}
	if err := json.Unmarshal(b, &obj); err != nil {
		return err
	}
	r.ID, r.Name, r.Username = obj.ID, obj.Name, obj.Username
	return nil
}

// set reports whether the ref points at anything.
func (r kongRef) set() bool { return r.ID != "" || r.Name != "" || r.Username != "" }

// ref returns the most human-readable stable identifier.
func (r kongRef) ref() string {
	switch {
	case r.Name != "":
		return r.Name
	case r.Username != "":
		return r.Username
	default:
		return r.ID
	}
}

// decodeConfig parses a Kong config blob that may be YAML (decK, possibly a
// multi-document stream of concatenated per-workspace dumps) or JSON (Admin API).
// Each document is decoded via yaml.v3 into a generic map (JSON is valid YAML), then
// re-marshaled to JSON so the json-tagged config decodes it, and all documents are
// merged — one path for both formats. A malformed document stops the stream but keeps
// everything merged before it. Returns false only when nothing decoded.
func decodeConfig(data []byte) (kongConfig, bool) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	var merged kongConfig
	decoded := false
	for {
		var raw map[string]any
		err := dec.Decode(&raw)
		if err == io.EOF {
			break
		}
		if err != nil {
			break // tolerant: keep what merged, stop on the first malformed doc
		}
		if len(raw) == 0 {
			continue
		}
		j, err := json.Marshal(raw)
		if err != nil {
			continue
		}
		var cfg kongConfig
		if json.Unmarshal(j, &cfg) != nil {
			continue
		}
		merged.Services = append(merged.Services, cfg.Services...)
		merged.Routes = append(merged.Routes, cfg.Routes...)
		merged.Plugins = append(merged.Plugins, cfg.Plugins...)
		merged.Consumers = append(merged.Consumers, cfg.Consumers...)
		decoded = true
	}
	return merged, decoded
}
