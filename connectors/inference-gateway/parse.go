// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package inferencegateway

import (
	"strings"
)

// The Gateway API Inference Extension API groups this connector recognizes. A
// manifest whose apiVersion does not begin with one of these (a stray Deployment, a
// Service in the same directory) is ignored. Verified against the upstream reference
// (https://gateway-api-inference-extension.sigs.k8s.io/): the GA group is
// "inference.networking.k8s.io" and the pre-GA group is
// "inference.networking.x-k8s.io" (the GA-migration guide states the v1 API moves
// from x-k8s.io to k8s.io and replaces InferenceModel with InferenceObjective).
const (
	apiGroupGA    = "inference.networking.k8s.io/"
	apiGroupAlpha = "inference.networking.x-k8s.io/"
)

// The Inference Extension kinds this connector reads. InferenceModel (v1alpha2) and
// its v1 successor InferenceObjective are BOTH handled — a cluster mid-migration may
// carry either. InferencePoolImport / InferenceModelRewrite exist in the same group
// but are deliberately out of scope (they do not declare a pool or a model->pool
// binding this connector maps).
const (
	kindInferencePool      = "InferencePool"
	kindInferenceModel     = "InferenceModel"     // v1alpha2
	kindInferenceObjective = "InferenceObjective" // v1 GA (replaces InferenceModel)
)

// The resource kinds emitted for the two declared-topology edge classes.
const (
	resourcePool  = "inference.pool"
	resourceModel = "inference.model"
)

// toolRef is the constant tool label stamped on every edge: the surface that
// declared the topology. It is the human-facing "which tool" for the permitted edge,
// distinct from the edge Source (model.SignalPolicy) and from the connector's own
// provenance value (SignalInferenceGateway).
const toolRef = "k8s.inference_gateway"

// defaultNamespace is the namespace assumed when a namespaced object omits
// metadata.namespace, matching the Kubernetes default.
const defaultNamespace = "default"

// document is the minimal, TOLERANT envelope every Inference Extension CRD shares.
// apiVersion + kind dispatch the decode; metadata carries name/namespace; spec is a
// UNION of the InferencePool and the InferenceModel/InferenceObjective shapes. The
// union is safe because the field sets do not overlap and the connector only consults
// the fields valid for the document's kind. The connector reads ONLY these fields —
// never a status, a selector value beyond a scrubbed label, or any payload.
//
// The spec carries BOTH the old and the new field names for the two fields that were
// renamed at GA (the EPP reference and the port), so a manifest from either group
// version parses. This tolerance is a documented superset of the verified upstream
// fields, not an invented schema (see doc.go schema authority).
type document struct {
	APIVersion string     `yaml:"apiVersion"`
	Kind       string     `yaml:"kind"`
	Metadata   objectMeta `yaml:"metadata"`
	Spec       spec       `yaml:"spec"`
}

type objectMeta struct {
	Name      string `yaml:"name"`
	Namespace string `yaml:"namespace"`
}

// spec is the union of the two CRD spec shapes (see document).
type spec struct {
	// --- InferencePool ---
	// Selector is spec.selector: the label set selecting the model-serving pods. Its
	// shape DIFFERS by API version (v1alpha2 is a flat map of label key->value; v1 GA
	// wraps it in a LabelSelector with a nested matchLabels), and the connector NEVER
	// emits a selector value — so it is decoded into a discarding rawAny rather than a
	// typed map. This both tolerates either shape and guarantees no selector value
	// (which could carry an accidentally-pasted secret) is ever pulled into memory.
	Selector rawAny `yaml:"selector"`
	// TargetPortNumber is the v1alpha2 single serving port; TargetPorts is the v1 GA
	// list. Either may be set; the connector reads whichever is present.
	TargetPortNumber int          `yaml:"targetPortNumber"`
	TargetPorts      []targetPort `yaml:"targetPorts"`
	// ExtensionRef (v1alpha2) / EndpointPickerRef (v1 GA) is the Endpoint Picker (EPP)
	// the pool delegates per-request endpoint selection to. Either may be set.
	ExtensionRef      *objectRef `yaml:"extensionRef"`
	EndpointPickerRef *objectRef `yaml:"endpointPickerRef"`

	// --- InferenceModel (v1alpha2) / InferenceObjective (v1 GA) ---
	// ModelName is the InferenceModel served-model name; InferenceObjective has none
	// (its identity is metadata.name), so it is empty for an Objective.
	ModelName string `yaml:"modelName"`
	// PoolRef is the InferencePool this model/objective is served on.
	PoolRef *poolRef `yaml:"poolRef"`
	// Criticality (InferenceModel: Critical|Standard|Sheddable) / Priority
	// (InferenceObjective: integer) is the serving-priority band. It is carried for
	// display only; it never affects R/RW. Priority is a *int so "0" (a real priority)
	// is distinguished from "unset".
	Criticality string `yaml:"criticality"`
	Priority    *int   `yaml:"priority"`
}

type targetPort struct {
	Number int `yaml:"number"`
}

// objectRef is the EPP reference (extensionRef / endpointPickerRef). Only Name is
// load-bearing for the edge origin; Group/Kind are read to keep the parse faithful
// but are not emitted.
type objectRef struct {
	Name  string `yaml:"name"`
	Group string `yaml:"group"`
	Kind  string `yaml:"kind"`
}

// poolRef is spec.poolRef on a model/objective: the InferencePool it binds to. Name
// is the pool name; Group/Kind default to the InferencePool of the same API group.
type poolRef struct {
	Name  string `yaml:"name"`
	Group string `yaml:"group"`
	Kind  string `yaml:"kind"`
}

// rawAny is a permissive YAML value used for fields the connector parses for
// completeness but never inspects or emits (spec.selector). Its UnmarshalYAML accepts
// any node — a flat map (v1alpha2) or a nested LabelSelector (v1 GA) — without
// decoding its children, so a selector value (which could carry an accidentally
// pasted secret) is never pulled into memory. Mirrors connectors/externalsecrets.
type rawAny struct{}

// UnmarshalYAML accepts any node without decoding its children.
func (rawAny) UnmarshalYAML(func(interface{}) error) error { return nil }

// isInferenceDoc reports whether a decoded document is an Inference Extension CRD this
// connector handles (either API group, one of the three kinds).
func isInferenceDoc(d document) bool {
	if !strings.HasPrefix(d.APIVersion, apiGroupGA) && !strings.HasPrefix(d.APIVersion, apiGroupAlpha) {
		return false
	}
	switch d.Kind {
	case kindInferencePool, kindInferenceModel, kindInferenceObjective:
		return true
	default:
		return false
	}
}

// namespaceOf returns an object's namespace, defaulting to "default" when omitted.
func namespaceOf(d document) string {
	if ns := strings.TrimSpace(d.Metadata.Namespace); ns != "" {
		return ns
	}
	return defaultNamespace
}

// poolOrigin resolves the ORIGIN identity for an InferencePool edge: the EPP
// extension/endpoint-picker name when one is declared (it is the routing actor —
// the component that picks the endpoint per request), else the pool's own name.
// The EPP reference may be carried under either the v1alpha2 (extensionRef) or the
// v1 GA (endpointPickerRef) field; whichever names something wins. ok is false when
// neither the EPP nor the pool name resolves (a malformed object), so the edge is
// skipped rather than emitted with an empty origin.
func poolOrigin(d document) (string, bool) {
	if ref := d.Spec.EndpointPickerRef; ref != nil {
		if n := strings.TrimSpace(ref.Name); n != "" {
			return n, true
		}
	}
	if ref := d.Spec.ExtensionRef; ref != nil {
		if n := strings.TrimSpace(ref.Name); n != "" {
			return n, true
		}
	}
	if n := strings.TrimSpace(d.Metadata.Name); n != "" {
		return n, true
	}
	return "", false
}

// modelIdentity resolves the served-model identity for an InferenceModel /
// InferenceObjective edge. An InferenceModel carries spec.modelName; an
// InferenceObjective (which has no modelName) is identified by metadata.name. ok is
// false when neither resolves.
func modelIdentity(d document) (string, bool) {
	if n := strings.TrimSpace(d.Spec.ModelName); n != "" {
		return n, true
	}
	if n := strings.TrimSpace(d.Metadata.Name); n != "" {
		return n, true
	}
	return "", false
}
