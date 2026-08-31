// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package crossplane

import (
	"sort"
	"strings"
)

// apiGroupPrefix is the Crossplane apiextensions API group. A manifest whose
// apiVersion does not begin with this (a stray ConfigMap, a Provider, a Composition
// sharing the same directory) is ignored. Crossplane ships the XRD API as
// apiextensions.crossplane.io/v1 (current and stable). Verified against
// docs.crossplane.io (the CompositeResourceDefinition reference). The version is NOT
// pinned beyond the group prefix on purpose: the structural fields this
// connector reads (group / names / versions / served / referenceable) are identical
// across recent Crossplane releases, including the v2 line.
const apiGroupPrefix = "apiextensions.crossplane.io/"

// kindXRD is the only kind this connector handles. Any other kind in a mixed
// manifest is skipped.
const kindXRD = "CompositeResourceDefinition"

// document is the minimal envelope of a Crossplane XRD manifest the connector reads.
// apiVersion+kind dispatch the decode; metadata.name is the XRD name (the stable
// subject ref, e.g. "xdatabases.custom-api.example.org"); spec carries ONLY the
// structural composite API surface — the group, the composite kind/plural names, and
// the declared versions. The connector reads NOTHING else: no status, no Composition
// reference, and no schema property values (only, optionally, the top-level required
// field NAMES of a version — see openAPISchema).
type document struct {
	APIVersion string     `yaml:"apiVersion"`
	Kind       string     `yaml:"kind"`
	Metadata   objectMeta `yaml:"metadata"`
	Spec       xrdSpec    `yaml:"spec"`
}

type objectMeta struct {
	Name string `yaml:"name"`
}

// xrdSpec is the subset of an XRD spec the connector reads. group is the API group
// the composite type lives in; names declares the composite Kind and its plural;
// versions[] declares each API version of the composite type. claimNames (the v1.x
// claim API) is deliberately NOT decoded: this connector inventories the composite
// type surface, not the claim model, and Crossplane v2 reworked claims — encoding a
// claim posture here would assert a behavior the connector does not verify.
type xrdSpec struct {
	Group    string         `yaml:"group"`
	Names    compositeNames `yaml:"names"`
	Versions []xrdVersion   `yaml:"versions"`
}

// compositeNames is spec.names: the composite resource's Kind and its plural form,
// which together with the group form the composite API surface this connector
// catalogs. Only the structural type names are read — never a label or annotation.
type compositeNames struct {
	Kind   string `yaml:"kind"`
	Plural string `yaml:"plural"`
}

// xrdVersion is one spec.versions[] entry. name is the version string (e.g.
// "v1alpha1"); served reports whether the API server serves this version;
// referenceable reports whether a Composition may select this version. schema holds
// the version's OpenAPI v3 schema — of which the connector reads ONLY the top-level
// required field NAMES (see openAPISchema), never a property value.
type xrdVersion struct {
	Name          string    `yaml:"name"`
	Served        bool      `yaml:"served"`
	Referenceable bool      `yaml:"referenceable"`
	Schema        xrdSchema `yaml:"schema"`
}

// xrdSchema is the version's schema wrapper. openAPIV3Schema is the embedded OpenAPI
// schema; the connector descends ONLY to its top-level "required" list (a list of
// field NAMES, which are part of the public API contract) and reads nothing else —
// no property type, default, description, or nested schema (all payload-adjacent,
// docs/SECURITY-HARDENING.md).
type xrdSchema struct {
	OpenAPIV3Schema openAPISchema `yaml:"openAPIV3Schema"`
}

// openAPISchema captures only the top-level "required" field-name list of an XRD
// version's OpenAPI v3 schema. Properties, defaults and descriptions are NOT decoded
// (the struct simply has no fields for them), so a property value can never reach an
// emitted finding.
type openAPISchema struct {
	Required []string `yaml:"required"`
}

// isXRDDoc reports whether a decoded document is a Crossplane XRD this connector
// handles.
func isXRDDoc(d document) bool {
	if !strings.HasPrefix(d.APIVersion, apiGroupPrefix) {
		return false
	}
	return d.Kind == kindXRD
}

// subjectRef is the stable reference a finding's SubjectRef and DetailHash converge
// on: the XRD's metadata.name (e.g. "xdatabases.custom-api.example.org"). An XRD is
// a cluster-scoped object, so there is no namespace to qualify it.
func subjectRef(d document) string {
	return strings.TrimSpace(d.Metadata.Name)
}

// versionLabel renders one declared version for a finding Title, annotating its
// served/referenceable posture so the inventory shows WHICH versions are live. The
// annotations are taken verbatim from the version's own served/referenceable flags,
// never inferred: a non-served version is marked "[not served]" so a reader sees a
// version that exists in the type but is no longer exposed.
func versionLabel(v xrdVersion) string {
	name := strings.TrimSpace(v.Name)
	switch {
	case v.Served && v.Referenceable:
		return name + "[served]"
	case v.Served && !v.Referenceable:
		return name + "[served,not-referenceable]"
	case !v.Served && v.Referenceable:
		return name + "[not served,referenceable]"
	default:
		return name + "[not served]"
	}
}

// versionLabels renders every declared version in spec order for the Title. The
// order is preserved from the manifest (Crossplane lists versions newest-first by
// convention); an empty version name is skipped so a stray entry cannot produce a
// dangling "[served]" token.
func versionLabels(d document) []string {
	out := make([]string, 0, len(d.Spec.Versions))
	for _, v := range d.Spec.Versions {
		if strings.TrimSpace(v.Name) == "" {
			continue
		}
		out = append(out, versionLabel(v))
	}
	return out
}

// versionNames is the bare, sorted list of declared version name strings, used in the
// stable DetailHash key (sorted so the same XRD re-hashes identically regardless of
// the order the versions appear in the manifest).
func versionNames(d document) []string {
	out := make([]string, 0, len(d.Spec.Versions))
	for _, v := range d.Spec.Versions {
		if n := strings.TrimSpace(v.Name); n != "" {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}

// compositeAPI is the "<plural>.<group>" identity of the composite resource type the
// XRD introduces (the form an operator types into `kubectl get`). It is built from
// the structural names only; when plural or group is absent it degrades gracefully
// rather than fabricating a value.
func compositeAPI(d document) string {
	plural := strings.TrimSpace(d.Spec.Names.Plural)
	group := strings.TrimSpace(d.Spec.Group)
	switch {
	case plural != "" && group != "":
		return plural + "." + group
	case plural != "":
		return plural
	case group != "":
		return group
	default:
		return subjectRef(d)
	}
}
