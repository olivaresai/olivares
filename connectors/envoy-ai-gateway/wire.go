// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package envoyaigw

import (
	"bytes"
	"encoding/json"
	"io"

	"gopkg.in/yaml.v3"
)

// The wire types are a TOLERANT, DOCUMENTED view of the Envoy AI Gateway v1alpha1
// CRDs (see doc.go for the verified field list). Every field is optional; a spec is
// a union of the fields across the kinds this connector reads, so one decode path
// serves every object and an unknown/renamed field degrades to fewer findings.

// k8sObject is the generic manifest envelope: a single object, or a List whose
// Items carry the real objects.
type k8sObject struct {
	APIVersion string      `json:"apiVersion"`
	Kind       string      `json:"kind"`
	Metadata   objMeta     `json:"metadata"`
	Spec       objSpec     `json:"spec"`
	Items      []k8sObject `json:"items"`
}

type objMeta struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
}

// objSpec unions the spec fields across AIServiceBackend, BackendSecurityPolicy,
// AIGatewayRoute, MCPRoute and QuotaPolicy — all optional.
type objSpec struct {
	// AIServiceBackend
	Schema *apiSchema `json:"schema"`
	// BackendSecurityPolicy / QuotaPolicy
	TargetRefs []nameRef `json:"targetRefs"`
	Type       string    `json:"type"`
	// AIGatewayRoute
	Rules []routeRule `json:"rules"`
	// MCPRoute
	Path           string          `json:"path"`
	BackendRefs    []mcpBackendRef `json:"backendRefs"`
	SecurityPolicy json.RawMessage `json:"securityPolicy"`
	// QuotaPolicy
	DefaultBucket *quotaBucket `json:"defaultBucket"`
}

type apiSchema struct {
	Name string `json:"name"`
}

type nameRef struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
}

type routeRule struct {
	BackendRefs     []aiRouteBackendRef `json:"backendRefs"`
	LLMRequestCosts []json.RawMessage   `json:"llmRequestCosts"`
}

type aiRouteBackendRef struct {
	Name              string `json:"name"`
	Namespace         string `json:"namespace"`
	ModelNameOverride string `json:"modelNameOverride"`
}

type mcpBackendRef struct {
	Name           string          `json:"name"`
	ToolSelector   json.RawMessage `json:"toolSelector"`
	SecurityPolicy json.RawMessage `json:"securityPolicy"`
}

type quotaBucket struct {
	Limit    int64  `json:"limit"`
	Duration string `json:"duration"`
}

// present reports whether an optional raw field carries a real value (not absent,
// not explicit null).
func present(r json.RawMessage) bool {
	return len(r) > 0 && string(bytes.TrimSpace(r)) != "null"
}

// decodeObjects parses a manifest blob that may be YAML (possibly multi-document)
// or JSON (a single object, a List, or a JSON stream). Each document is decoded via
// yaml.v3 into a generic map (JSON is valid YAML), then re-marshaled to JSON so the
// json-tagged envelope decodes it — one path for both formats. A List is flattened;
// a malformed document stops the stream but keeps everything parsed before it
// (discovery never aborts wholesale on one bad export).
func decodeObjects(data []byte) []k8sObject {
	var out []k8sObject
	dec := yaml.NewDecoder(bytes.NewReader(data))
	for {
		var raw map[string]any
		err := dec.Decode(&raw)
		if err == io.EOF {
			break
		}
		if err != nil {
			break // tolerant: keep what parsed, stop on the first malformed doc
		}
		if len(raw) == 0 {
			continue
		}
		j, err := json.Marshal(raw)
		if err != nil {
			continue
		}
		var obj k8sObject
		if json.Unmarshal(j, &obj) != nil {
			continue
		}
		if len(obj.Items) > 0 {
			out = append(out, obj.Items...)
			continue
		}
		if obj.Kind != "" || obj.Metadata.Name != "" {
			out = append(out, obj)
		}
	}
	return out
}
