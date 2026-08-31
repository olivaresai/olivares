// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package externalsecrets

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/olivaresai/olivares/connectors/identitysource"
	"github.com/olivaresai/olivares/connectors/internal/identity"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// Name is the connector's globally unique identifier.
const Name = "olivares.external-secrets"

// signalESO is the SignalSource stamped on every edge: the External Secrets
// Operator's declared provisioning topology (a configuration fact, like a Vault
// policy grant — not a runtime observation).
const signalESO model.SignalSource = "eso"

// Source is the External Secrets Operator (ESO) connector. It
// satisfies sdk.SourceConnector (the store→k8s-secret provisioning edges) and
// identitysource.GraphProvider (the secret_store inventory). The zero
// value is not usable; call New.
type Source struct {
	path string
	now  func() time.Time // injectable clock (tests); nil => time.Now
}

// Compile-time proof that Source satisfies both contracts.
var (
	_ sdk.SourceConnector          = (*Source)(nil)
	_ identitysource.GraphProvider = (*Source)(nil)
)

// New returns an external-secrets source.
func New() *Source { return &Source{} }

// Descriptor returns the connector's self-description and configuration schema.
func (s *Source) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:        Name,
		Version:     "0.1.0",
		APIVersion:  sdk.APIVersion,
		Type:        sdk.TypeSource,
		Title:       "External Secrets Operator (ESO)",
		Description: "Reads ESO CRDs (ExternalSecret/SecretStore/ClusterSecretStore) from exported Kubernetes manifests and maps which K8s secret is provisioned from which backend store. Read-only; never reads a secret value.",
		ConfigFields: []sdk.ConfigField{
			{Key: "path", Type: sdk.FieldString, Required: true, Description: "ESO manifest file, or a directory of *.yaml / *.yml / *.json Kubernetes manifests (multi-document YAML supported)."},
		},
	}
}

// Open reads and validates configuration.
func (s *Source) Open(_ context.Context, cfg sdk.Config) error {
	s.path = strings.TrimSpace(cfg.Get("path"))
	if s.path == "" {
		return errors.New("external-secrets: path is required")
	}
	return nil
}

// Close releases resources; this connector holds none between Gather runs.
func (s *Source) Close(context.Context) error { return nil }

// Gather reads the configured manifests and emits one edge per ExternalSecret
// data/dataFrom entry: "the referenced store provisions (writes) this Kubernetes
// Secret, hydrated from that backend key". It is a batch source: it returns nil
// when the manifests are exhausted. It never emits a secret value (a CRD has none).
func (s *Source) Gather(ctx context.Context, sink sdk.Sink) error {
	docs, err := s.readDocs()
	if err != nil {
		return err
	}
	now := s.clock().UTC()
	for _, d := range docs {
		if err := ctx.Err(); err != nil {
			return err
		}
		if d.Kind != kindExternalSecret {
			continue
		}
		for _, edge := range deriveEdges(d, now) {
			if err := sink.Emit(ctx, edge); err != nil {
				return err
			}
		}
	}
	return nil
}

// Snapshot exposes every SecretStore/ClusterSecretStore the manifests declare as a
// secret_store NHI, each carrying the single backend it fronts. The
// store Ref matches the edge OriginRef exactly, so inventory and use converge. With
// no configured path it returns an empty graph (offline). It never returns a secret
// value (a CRD has none).
func (s *Source) Snapshot(_ context.Context) (identitysource.Graph, error) {
	g := identitysource.Graph{Source: identitysource.SourceExternalSecrets, CapturedAt: s.clock().UTC()}
	if s.path == "" {
		return g, nil
	}
	docs, err := s.readDocs()
	if err != nil {
		return identitysource.Graph{}, err
	}
	type storeInfo struct {
		ref     storeRef
		backend string
	}
	stores := map[string]storeInfo{}
	for _, d := range docs {
		if d.Kind != kindSecretStore && d.Kind != kindClusterSecretStore {
			continue
		}
		ref := storeRefForStore(d)
		backend := ""
		if p, ok := resolveProvider(d.Spec.Provider); ok {
			backend = p.backend
		}
		stores[ref.ref()] = storeInfo{ref: ref, backend: backend}
	}
	refs := make([]string, 0, len(stores))
	for r := range stores {
		refs = append(refs, r)
	}
	sort.Strings(refs)
	for _, r := range refs {
		st := stores[r]
		g.Identities = append(g.Identities, identitysource.Identity{
			Ref:         st.ref.ref(),
			Type:        identitysource.PrincipalNHI,
			Kind:        identitysource.KindSecretStore,
			DisplayName: st.ref.displayName(st.backend),
			Source:      identitysource.SourceExternalSecrets,
			Attributes: map[string]string{
				"provider":   "eso",
				"backend":    st.backend,
				"store_kind": st.ref.kind,
			},
		})
	}
	return g, nil
}

// deriveEdges maps one ExternalSecret to its provisioning edges (one per
// data/dataFrom entry that names a backend key). It returns nothing when the store
// reference does not resolve, the target Secret name is empty, or the ExternalSecret
// names no backend key (neither data nor dataFrom) — skipped, never guessed.
//
// spec.target.creationPolicy=None means ESO does NOT create or write the target
// Secret (it expects one to pre-exist and manage itself); there is therefore no
// provisioning relationship to record, so those ExternalSecrets emit no edge. The
// provisioning policies (Owner/Merge/Orphan) DO hydrate the Secret from the backend
// and emit a write edge — verbatim from the source's own semantics, never guessed.
func deriveEdges(es document, now time.Time) []model.EdgeObservation {
	if strings.EqualFold(strings.TrimSpace(es.Spec.Target.CreationPolicy), "None") {
		return nil // ESO does not provision the Secret under creationPolicy=None
	}
	store, ok := storeRefForES(es)
	if !ok {
		return nil
	}
	secretName := targetSecretName(es)
	if secretName == "" {
		return nil
	}
	keys := remoteKeysOf(es)
	if len(keys) == 0 {
		return nil
	}
	resourceRef := namespaceOf(es) + "/" + secretName
	originRef := store.ref()
	out := make([]model.EdgeObservation, 0, len(keys))
	for _, key := range keys {
		out = append(out, model.EdgeObservation{
			OriginKind:   identity.OriginKind,
			OriginRef:    originRef,
			ResourceKind: resourceK8sSecret,
			ResourceRef:  resourceRef,
			Mode:         model.ModeWrite,
			Source:       signalESO,
			Confidence:   model.ConfidenceAttributed,
			ToolRef:      key,
			ObservedAt:   now,
		})
	}
	return out
}

// readDocs resolves the configured path to its files and decodes every ESO CRD
// document from them (multi-document YAML and JSON both decode through yaml.v3). A
// non-ESO document is skipped. Files are read in sorted order for deterministic
// output.
func (s *Source) readDocs() ([]document, error) {
	files, err := s.listFiles()
	if err != nil {
		return nil, err
	}
	var docs []document
	for _, f := range files {
		fileDocs, err := decodeFile(f)
		if err != nil {
			return nil, err
		}
		docs = append(docs, fileDocs...)
	}
	return docs, nil
}

// listFiles resolves the configured path to a sorted list of files (a directory
// contributes its *.yaml / *.yml / *.json entries; a file contributes itself).
func (s *Source) listFiles() ([]string, error) {
	fi, err := os.Stat(s.path)
	if err != nil {
		return nil, err
	}
	if !fi.IsDir() {
		return []string{s.path}, nil
	}
	entries, err := os.ReadDir(s.path)
	if err != nil {
		return nil, err
	}
	var files []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		n := e.Name()
		if strings.HasSuffix(n, ".yaml") || strings.HasSuffix(n, ".yml") || strings.HasSuffix(n, ".json") {
			files = append(files, filepath.Join(s.path, n))
		}
	}
	sort.Strings(files)
	return files, nil
}

// decodeFile reads one manifest file and decodes each YAML document in it (a file
// may hold several objects separated by `---`). JSON is a subset of YAML, so a
// single-object .json file decodes through the same path. A document that is not an
// ESO CRD this connector handles is skipped silently. An empty document (a stray
// `---` or a comment-only block) decodes to the zero value and is skipped.
func decodeFile(path string) ([]document, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, err
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	var out []document
	for {
		var d document
		if err := dec.Decode(&d); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, err
		}
		if isESODoc(d) {
			out = append(out, d)
		}
	}
	return out, nil
}

func (s *Source) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}
