// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package externalsecrets

import (
	"sort"
	"strings"
)

// apiGroupPrefix is the External Secrets Operator API group. A manifest whose
// apiVersion does not begin with this (e.g. a stray ConfigMap or a cert-manager
// object in the same directory) is ignored. ESO ships both external-secrets.io/v1
// (current) and external-secrets.io/v1beta1 (legacy); this connector accepts both
// because the spec paths it reads are identical across them.
const apiGroupPrefix = "external-secrets.io/"

// The ESO kinds this connector recognizes. PushSecret and ClusterExternalSecret
// exist in the same group but are deliberately ignored (out of scope).
const (
	kindExternalSecret     = "ExternalSecret"
	kindSecretStore        = "SecretStore"
	kindClusterSecretStore = "ClusterSecretStore"
)

// The resource kind emitted for a materialized Kubernetes Secret.
const resourceK8sSecret = "k8s.secret"

// defaultNamespace is the namespace assumed when a namespaced object omits
// metadata.namespace, matching the Kubernetes default. A ClusterSecretStore is
// cluster-scoped and uses the clusterScope sentinel in its ref instead.
const (
	defaultNamespace = "default"
	clusterScope     = "cluster"
)

// document is the minimal envelope every ESO manifest shares: its apiVersion and
// kind dispatch the decode, its metadata carries the object's name/namespace, and
// its spec is decoded lazily into the kind-specific shape. The connector reads ONLY
// these fields — it never decodes a status, a secret value, or any other payload.
type document struct {
	APIVersion string         `yaml:"apiVersion"`
	Kind       string         `yaml:"kind"`
	Metadata   objectMeta     `yaml:"metadata"`
	Spec       externalSecret `yaml:"spec"` // union: ExternalSecret OR Store spec
}

type objectMeta struct {
	Name      string `yaml:"name"`
	Namespace string `yaml:"namespace"`
}

// externalSecret is the subset of an ExternalSecret/Store spec the connector reads.
// It is a UNION shape: secretStoreRef/target/data/dataFrom belong to an
// ExternalSecret; provider belongs to a SecretStore/ClusterSecretStore. Decoding
// both into one struct is safe because the field sets do not overlap, and the
// connector only consults the fields valid for the document's kind.
type externalSecret struct {
	SecretStoreRef secretStoreRef    `yaml:"secretStoreRef"`
	Target         target            `yaml:"target"`
	Data           []dataEntry       `yaml:"data"`
	DataFrom       []dataFromEntry   `yaml:"dataFrom"`
	Provider       map[string]rawAny `yaml:"provider"` // SecretStore/ClusterSecretStore
}

// secretStoreRef points an ExternalSecret at its store. Kind is exactly
// "SecretStore" or "ClusterSecretStore"; an empty Kind defaults to "SecretStore"
// (the ESO default, verified against the CRD).
type secretStoreRef struct {
	Name string `yaml:"name"`
	Kind string `yaml:"kind"`
}

// target is spec.target. Name defaults to metadata.name when empty (the ESO
// default). CreationPolicy gates edge derivation: Owner/Merge/Orphan hydrate the
// Secret (a write edge); None means ESO does not provision it, so no edge is
// emitted (see deriveEdges).
type target struct {
	Name           string `yaml:"name"`
	CreationPolicy string `yaml:"creationPolicy"`
}

// dataEntry is one spec.data[] item: a single backend key mapped to one key inside
// the materialized Secret. remoteRef.key is the backend key NAME (never a value).
type dataEntry struct {
	SecretKey string    `yaml:"secretKey"`
	RemoteRef remoteRef `yaml:"remoteRef"`
}

type remoteRef struct {
	Key      string `yaml:"key"`
	Property string `yaml:"property"`
	Version  string `yaml:"version"`
}

// dataFromEntry is one spec.dataFrom[] item: a bulk extract of a backend key, or a
// find by path/name. extract.key names a single backend key; find.path/find.name
// name a discovery scope. Either way it names backend references, never values.
type dataFromEntry struct {
	Extract *extractRef `yaml:"extract"`
	Find    *findRef    `yaml:"find"`
}

type extractRef struct {
	Key string `yaml:"key"`
}

type findRef struct {
	Path string `yaml:"path"`
	Name string `yaml:"name"`
}

// rawAny is a permissive YAML value used only to detect the presence of a provider
// backend key; its contents are never inspected (the connector records WHICH
// backend, not its config, which can hold endpoints but no secret value).
type rawAny struct{}

// UnmarshalYAML accepts any node without decoding its children, so a provider
// backend's nested config (which may legitimately reference auth secret names) is
// never pulled into memory.
func (rawAny) UnmarshalYAML(func(interface{}) error) error { return nil }

// provider is the resolved single backend of a SecretStore. ESO requires exactly
// one backend key under spec.provider; backend is that key ("vault", "aws",
// "gcpsm", "azurekv", "kubernetes", or any other ESO provider). ok is false when no
// backend key is present (a malformed/empty provider) — the store is still
// inventoried, with backend left empty, never guessed.
type provider struct {
	backend string
}

// resolveProvider returns the single backend key declared under spec.provider. ESO
// validates that exactly one is set; if a manifest somehow lists several, the
// lexicographically first is chosen for determinism and the rest are a documented
// seam (the connector does not invent a composite). An empty map yields ok=false.
func resolveProvider(m map[string]rawAny) (provider, bool) {
	if len(m) == 0 {
		return provider{}, false
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return provider{backend: keys[0]}, true
}

// storeRef is the resolved identity of a SecretStore/ClusterSecretStore: its kind,
// scope (namespace, or the clusterScope sentinel) and name. It is the convergence
// key shared by the inventory NHI Ref and the edge OriginRef.
type storeRef struct {
	kind  string // "SecretStore" | "ClusterSecretStore"
	scope string // namespace, or "cluster" for a ClusterSecretStore
	name  string
}

// ref is the stable external id both halves converge on:
// eso.store:<StoreKind>:<ns|cluster>:<name>.
func (r storeRef) ref() string {
	return "eso.store:" + r.kind + ":" + r.scope + ":" + r.name
}

// displayName is the human label stamped on the inventory NHI, e.g.
// "ESO SecretStore default/vault-backend (vault)".
func (r storeRef) displayName(backend string) string {
	label := "ESO " + r.kind + " " + r.scope + "/" + r.name
	if backend != "" {
		label += " (" + backend + ")"
	}
	return label
}

// storeRefForES resolves the store an ExternalSecret references. The store kind is
// secretStoreRef.kind, defaulting to "SecretStore" when empty (the ESO default). A
// namespaced SecretStore is resolved in the ExternalSecret's own namespace; a
// ClusterSecretStore is cluster-scoped (clusterScope sentinel). ok is false when no
// store name is given — the ExternalSecret is then skipped, never guessed.
func storeRefForES(es document) (storeRef, bool) {
	name := strings.TrimSpace(es.Spec.SecretStoreRef.Name)
	if name == "" {
		return storeRef{}, false
	}
	kind := strings.TrimSpace(es.Spec.SecretStoreRef.Kind)
	if kind == "" {
		kind = kindSecretStore // ESO default
	}
	scope := namespaceOf(es)
	if kind == kindClusterSecretStore {
		scope = clusterScope
	}
	return storeRef{kind: kind, scope: scope, name: name}, true
}

// storeRefForStore resolves a SecretStore/ClusterSecretStore object to its own ref,
// so its inventory NHI shares the exact key an ExternalSecret's edge converges on. A
// ClusterSecretStore is cluster-scoped regardless of any metadata.namespace.
func storeRefForStore(d document) storeRef {
	if d.Kind == kindClusterSecretStore {
		return storeRef{kind: kindClusterSecretStore, scope: clusterScope, name: d.Metadata.Name}
	}
	return storeRef{kind: kindSecretStore, scope: namespaceOf(d), name: d.Metadata.Name}
}

// namespaceOf returns an object's namespace, defaulting to "default" when omitted.
func namespaceOf(d document) string {
	if ns := strings.TrimSpace(d.Metadata.Namespace); ns != "" {
		return ns
	}
	return defaultNamespace
}

// targetSecretName returns the name of the Kubernetes Secret an ExternalSecret
// materializes: spec.target.name, defaulting to metadata.name (the ESO default).
func targetSecretName(es document) string {
	if n := strings.TrimSpace(es.Spec.Target.Name); n != "" {
		return n
	}
	return strings.TrimSpace(es.Metadata.Name)
}

// remoteKey is one backend key reference resolved from a data/dataFrom entry,
// carried into an edge's ToolRef. It is a NAME the store reads from, never a value.
func remoteKeysOf(es document) []string {
	var keys []string
	for _, d := range es.Spec.Data {
		if k := strings.TrimSpace(d.RemoteRef.Key); k != "" {
			keys = append(keys, k)
		}
	}
	for _, df := range es.Spec.DataFrom {
		switch {
		case df.Extract != nil && strings.TrimSpace(df.Extract.Key) != "":
			keys = append(keys, strings.TrimSpace(df.Extract.Key))
		case df.Find != nil && strings.TrimSpace(df.Find.Path) != "":
			keys = append(keys, strings.TrimSpace(df.Find.Path))
		case df.Find != nil && strings.TrimSpace(df.Find.Name) != "":
			keys = append(keys, strings.TrimSpace(df.Find.Name))
		}
	}
	return keys
}

// isESODoc reports whether a decoded document is an ESO CRD this connector handles.
func isESODoc(d document) bool {
	if !strings.HasPrefix(d.APIVersion, apiGroupPrefix) {
		return false
	}
	switch d.Kind {
	case kindExternalSecret, kindSecretStore, kindClusterSecretStore:
		return true
	default:
		return false
	}
}
