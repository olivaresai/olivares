// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package istiotelemetry

import (
	"strings"
)

// apiGroupPrefix is the Istio Telemetry API group. A manifest whose apiVersion
// does not begin with this (a stray ConfigMap, a VirtualService, a cert-manager
// object sharing the same directory) is ignored. Istio ships the Telemetry API as
// telemetry.istio.io/v1 (current, since Istio 1.22) and telemetry.istio.io/v1alpha1
// (the original GA-as-alpha name); this connector accepts both because the spec
// paths it reads (accessLogging[].disabled, tracing[].disableSpanReporting,
// metrics[].overrides[].disabled, selector.matchLabels) are byte-identical across
// them. Verified against istio.io/latest/docs/reference/config/telemetry/.
const apiGroupPrefix = "telemetry.istio.io/"

// kindTelemetry is the only kind this connector handles. Any other kind in a mixed
// manifest is skipped.
const kindTelemetry = "Telemetry"

// defaultNamespace is the namespace assumed when a Telemetry object omits
// metadata.namespace, matching the Kubernetes default. A Telemetry resource is
// namespaced; one in the istio root namespace (istio-system) applies mesh-wide, one
// in an app namespace applies to that namespace, and a selector narrows it further —
// but that scoping is recorded as the subject ref, never inferred into a guess.
const defaultNamespace = "default"

// The three observability signals a Telemetry resource configures. They are the
// vocabulary the finding Title/Kind name so a "blind spot" finding says exactly
// WHICH signal a scope turned off.
const (
	signalAccessLogging = "access_logging"
	signalTracing       = "tracing"
	signalMetrics       = "metrics"
)

// document is the minimal envelope of an Istio Telemetry manifest the connector
// reads. apiVersion+kind dispatch the decode; metadata carries name/namespace; spec
// carries only the three observability stanzas and the workload selector. The
// connector reads NOTHING else — no status, no provider config (which can carry
// endpoints), no custom-tag literals (which can carry request header names) — only
// the structural posture: which signal is configured and whether it is disabled.
type document struct {
	APIVersion string        `yaml:"apiVersion"`
	Kind       string        `yaml:"kind"`
	Metadata   objectMeta    `yaml:"metadata"`
	Spec       telemetrySpec `yaml:"spec"`
}

type objectMeta struct {
	Name      string `yaml:"name"`
	Namespace string `yaml:"namespace"`
}

// telemetrySpec is the subset of a Telemetry spec the connector reads. selector
// scopes the policy to a set of workload labels; the three slices each configure one
// observability signal. targetRefs (the Gateway-API attachment alternative) is
// deliberately not decoded for matchLabels — it names a referenced resource, not a
// label set, and the posture (configured / disabled) does not depend on it.
type telemetrySpec struct {
	Selector      selector        `yaml:"selector"`
	AccessLogging []accessLogging `yaml:"accessLogging"`
	Tracing       []tracing       `yaml:"tracing"`
	Metrics       []metrics       `yaml:"metrics"`
}

// selector is spec.selector. matchLabels narrows the policy to the workloads whose
// pod labels match; an empty selector means the whole scope (namespace, or mesh-wide
// in the root namespace). The labels are recorded only to describe the scope.
type selector struct {
	MatchLabels map[string]string `yaml:"matchLabels"`
}

// accessLogging is one spec.accessLogging[] entry. providers names the access-log
// providers it applies to (empty = the default provider). disabled, when true,
// SUPPRESSES access logs for the impacted workloads — verbatim from the source:
// "Controls logging. If set to true, no access logs will be generated for impacted
// workloads (for the specified providers)." That is the observability blind spot
// this connector flags. filter/match are intentionally not read (a CEL filter
// expression can embed header/attribute names — payload-adjacent, docs/SECURITY-HARDENING.md).
type accessLogging struct {
	Providers []providerRef `yaml:"providers"`
	Disabled  boolValue     `yaml:"disabled"`
}

// tracing is one spec.tracing[] entry. disableSpanReporting (NOT "disabled" — the
// Telemetry API uses a distinct field name for tracing) is the suppress switch:
// "Controls span reporting. If set to true, no spans will be reported for impacted
// workloads." randomSamplingPercentage and customTags are intentionally NOT read:
// the sampling number is posture noise and a custom tag literal can carry a request
// header value (payload-adjacent). Only the configured/disabled posture is taken.
type tracing struct {
	Providers            []providerRef `yaml:"providers"`
	DisableSpanReporting boolValue     `yaml:"disableSpanReporting"`
}

// metrics is one spec.metrics[] entry. providers names the metrics providers;
// overrides[] can disable individual metrics. A metrics entry is treated as a
// blind-spot signal only when an override disables a metric — otherwise it is
// coverage. (Plain metrics coverage is reported as enabled, like logging/tracing.)
type metrics struct {
	Providers []providerRef    `yaml:"providers"`
	Overrides []metricOverride `yaml:"overrides"`
}

type metricOverride struct {
	Disabled boolValue `yaml:"disabled"`
}

// providerRef is a spec.*.providers[] entry. Only the provider NAME is read (a
// reference to a named, mesh-config-declared provider), never its config.
type providerRef struct {
	Name string `yaml:"name"`
}

// boolValue tolerates the two YAML shapes the Istio "disabled" / "disableSpanReporting"
// fields legitimately take. The Telemetry API types them as google.protobuf.BoolValue,
// whose canonical YAML is the bare scalar an operator writes ("disabled: true"), but
// the wrapper object form ("disabled: {value: true}") is also valid protobuf-JSON/YAML
// for a BoolValue. Decoding BOTH means the connector never UNDER-detects a disabled
// signal (a missed disable would silently hide a blind spot — the exact anti-evasion
// failure this connector exists to catch). A field that is absent or null stays
// {set:false}, which classifyDoc reads as "not disabled" — never guessed.
type boolValue struct {
	value bool
	set   bool
}

// True reports whether the field is present and true.
func (b boolValue) True() bool { return b.set && b.value }

// UnmarshalYAML accepts a bare bool scalar or a {value: bool} wrapper object. A
// scalar that is not a bool, or a wrapper without a value, leaves the field unset
// (treated as not-disabled) rather than erroring — a tolerant parser must not reject
// a whole manifest over one off-shape field.
func (b *boolValue) UnmarshalYAML(unmarshal func(interface{}) error) error {
	var scalar bool
	if err := unmarshal(&scalar); err == nil {
		b.value, b.set = scalar, true
		return nil
	}
	var wrap struct {
		Value bool `yaml:"value"`
	}
	if err := unmarshal(&wrap); err == nil {
		b.value, b.set = wrap.Value, true
		return nil
	}
	// Unrecognized shape: leave unset (not-disabled). Tolerant, never a guess.
	return nil
}

// isTelemetryDoc reports whether a decoded document is an Istio Telemetry CRD this
// connector handles.
func isTelemetryDoc(d document) bool {
	if !strings.HasPrefix(d.APIVersion, apiGroupPrefix) {
		return false
	}
	return d.Kind == kindTelemetry
}

// namespaceOf returns the object's namespace, defaulting to "default" when omitted.
func namespaceOf(d document) string {
	if ns := strings.TrimSpace(d.Metadata.Namespace); ns != "" {
		return ns
	}
	return defaultNamespace
}

// subjectRef is the stable "<namespace>/<name>" reference a finding's SubjectRef and
// DetailHash converge on.
func subjectRef(d document) string {
	return namespaceOf(d) + "/" + strings.TrimSpace(d.Metadata.Name)
}

// posture is one classified observability signal of a Telemetry resource: which
// signal, and whether the resource ENABLES (configures) it or DISABLES it (a
// deliberate blind spot). It is derived verbatim from the spec, never inferred.
type posture struct {
	signal   string // signalAccessLogging | signalTracing | signalMetrics
	disabled bool   // true => the resource turns this signal OFF for its scope
}

// classifyDoc reduces a Telemetry document to its observability postures — at most
// one enabled + one disabled per signal, de-duplicated, in a stable order
// (accessLogging, tracing, metrics). The classification is taken VERBATIM from the
// source fields:
//
//   - accessLogging[]: disabled=true => a logging blind spot; otherwise a configured
//     access-logging entry => logging coverage.
//   - tracing[]: disableSpanReporting=true => a tracing blind spot; otherwise a
//     configured tracing entry => tracing coverage.
//   - metrics[]: any overrides[].disabled=true => a metrics blind spot; otherwise a
//     configured metrics entry => metrics coverage.
//
// A signal the resource does not mention at all yields NO posture (absence is not a
// finding — this connector reports what the manifest declares, it does not infer a
// missing-coverage gap from silence, ARCHITECTURE.md /).
func classifyDoc(d document) []posture {
	var out []posture
	addedEnabled := map[string]bool{}
	addedDisabled := map[string]bool{}

	add := func(signal string, disabled bool) {
		if disabled {
			if !addedDisabled[signal] {
				addedDisabled[signal] = true
				out = append(out, posture{signal: signal, disabled: true})
			}
			return
		}
		if !addedEnabled[signal] {
			addedEnabled[signal] = true
			out = append(out, posture{signal: signal, disabled: false})
		}
	}

	for _, al := range d.Spec.AccessLogging {
		add(signalAccessLogging, al.Disabled.True())
	}
	for _, tr := range d.Spec.Tracing {
		add(signalTracing, tr.DisableSpanReporting.True())
	}
	for _, mt := range d.Spec.Metrics {
		disabled := false
		for _, ov := range mt.Overrides {
			if ov.Disabled.True() {
				disabled = true
				break
			}
		}
		add(signalMetrics, disabled)
	}

	return reorder(out)
}

// reorder sorts postures into a canonical, deterministic sequence — disabled signals
// (the blind-spot findings) first, then enabled (coverage), each in the fixed signal
// order (accessLogging, tracing, metrics) — so emission order does not depend on the
// order the stanzas appeared in the manifest. Disabled-first puts the higher-severity
// findings ahead of the informational ones.
func reorder(in []posture) []posture {
	out := make([]posture, 0, len(in))
	for _, disabled := range []bool{true, false} {
		for _, sig := range []string{signalAccessLogging, signalTracing, signalMetrics} {
			for _, p := range in {
				if p.signal == sig && p.disabled == disabled {
					out = append(out, p)
				}
			}
		}
	}
	return out
}
