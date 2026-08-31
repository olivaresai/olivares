// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package sops

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/identitysource"
	"github.com/olivaresai/olivares/connectors/internal/identity"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// Name is the connector's globally unique identifier.
const Name = "olivares.sops"

// signalSOPS is the SignalSource every SOPS edge carries (provenance: parsed from
// SOPS GitOps metadata, distinct from a CloudTrail or pgAudit signal).
const signalSOPS = model.SignalSource("sops")

// Source is the SOPS+age GitOps metadata connector. It satisfies
// sdk.SourceConnector (the recipient→file provisioning edges) and
// identitysource.GraphProvider (the secret_store recipient inventory).
// It reads ONLY cleartext metadata: it never decrypts, never reads an encrypted
// body, and never emits the per-recipient `enc` data key. The zero value is not
// usable; call New.
type Source struct {
	path string
	now  func() time.Time // injectable clock (tests); nil => time.Now
}

// Compile-time proof that Source satisfies both contracts.
var (
	_ sdk.SourceConnector          = (*Source)(nil)
	_ identitysource.GraphProvider = (*Source)(nil)
)

// New returns a sops source.
func New() *Source { return &Source{} }

// Descriptor returns the connector's self-description and configuration schema.
func (s *Source) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:        Name,
		Version:     "0.1.0",
		APIVersion:  sdk.APIVersion,
		Type:        sdk.TypeSource,
		Title:       "SOPS + age (GitOps secrets metadata)",
		Description: "Reads SOPS GitOps metadata (which recipient keys can decrypt which files) from a checked-out repo, read-only. Never decrypts, never reads an encrypted value, never emits the encrypted data key.",
		ConfigFields: []sdk.ConfigField{
			{Key: "path", Type: sdk.FieldString, Required: true, Description: "A SOPS-encrypted file, or a directory (a GitOps repo) to walk for *.sops.yaml rules and SOPS-encrypted YAML/JSON files."},
		},
	}
}

// Open reads and validates configuration.
func (s *Source) Open(_ context.Context, cfg sdk.Config) error {
	s.path = strings.TrimSpace(cfg.Get("path"))
	if s.path == "" {
		return errors.New("sops: path is required")
	}
	return nil
}

// Close releases resources; this connector holds none between Gather runs.
func (s *Source) Close(context.Context) error { return nil }

// Gather walks the configured path and, for every SOPS-ENCRYPTED file, emits one
// model.EdgeObservation per recipient that can decrypt it (recipient → file, read).
// It is a batch source: it returns nil when the tree is exhausted. It reads ONLY
// the cleartext `sops:` metadata block of each encrypted file and never the
// encrypted body or the per-recipient `enc` data key.
func (s *Source) Gather(ctx context.Context, sink sdk.Sink) error {
	root, err := s.root()
	if err != nil {
		return err
	}
	files, err := s.listFiles()
	if err != nil {
		return err
	}
	observedAt := s.clock().UTC()
	for _, f := range files {
		if err := ctx.Err(); err != nil {
			return err
		}
		data, err := os.ReadFile(f)
		if err != nil {
			return err
		}
		recips, ok := parseEncrypted(data)
		if !ok {
			continue // a .sops.yaml rules file or a plain file: not an encrypted edge.
		}
		resourceRef := relRef(root, f)
		for _, r := range recips {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := sink.Emit(ctx, model.EdgeObservation{
				OriginKind:   identity.OriginKind,
				OriginRef:    r.ref,
				ResourceKind: resourceKindFile,
				ResourceRef:  resourceRef,
				Mode:         model.ModeRead,
				Source:       signalSOPS,
				Confidence:   model.ConfidenceAttributed,
				ToolRef:      r.typ,
				ObservedAt:   observedAt,
			}); err != nil {
				return err
			}
		}
	}
	return nil
}

// Snapshot exposes every DISTINCT recipient seen — across the encrypted files AND
// the `.sops.yaml` rules — as a secret_store NHI, converging on the
// SAME ref string used as the edge OriginRef. With no configured path it returns an
// empty graph (offline). It carries the recipient's PUBLIC identifier and type
// only — never the `enc` data key or any other key material.
func (s *Source) Snapshot(_ context.Context) (identitysource.Graph, error) {
	g := identitysource.Graph{Source: identitysource.SourceSOPS, CapturedAt: s.clock().UTC()}
	if s.path == "" {
		return g, nil
	}
	files, err := s.listFiles()
	if err != nil {
		return identitysource.Graph{}, err
	}
	recips := map[string]recipient{}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			return identitysource.Graph{}, err
		}
		if rs, ok := parseEncrypted(data); ok {
			for _, r := range rs {
				recips[r.ref] = r
			}
			continue
		}
		if filepath.Base(f) == sopsRulesFile {
			if rs, ok := parseRules(data); ok {
				for _, r := range rs {
					recips[r.ref] = r
				}
			}
		}
	}
	refs := make([]string, 0, len(recips))
	for ref := range recips {
		refs = append(refs, ref)
	}
	sort.Strings(refs)
	for _, ref := range refs {
		r := recips[ref]
		g.Identities = append(g.Identities, identitysource.Identity{
			Ref:         r.ref,
			Type:        identitysource.PrincipalNHI,
			Kind:        identitysource.KindSecretStore,
			DisplayName: r.displayName(),
			Source:      identitysource.SourceSOPS,
			Attributes:  map[string]string{"provider": "sops", "recipient_type": r.typ},
		})
	}
	return g, nil
}

// sopsRulesFile is the well-known name of a SOPS creation_rules configuration file.
const sopsRulesFile = ".sops.yaml"

// listFiles resolves the configured path to a sorted list of candidate files. A
// file contributes itself; a directory is walked, contributing every `.sops.yaml`
// rules file and every *.yaml/*.yml/*.json file (the only shapes a top-level
// `sops:` block can live in for this connector). Other files are ignored.
func (s *Source) listFiles() ([]string, error) {
	fi, err := os.Stat(s.path)
	if err != nil {
		return nil, err
	}
	if !fi.IsDir() {
		return []string{s.path}, nil
	}
	var files []string
	err = filepath.WalkDir(s.path, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if isCandidate(d.Name()) {
			files = append(files, p)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	return files, nil
}

// isCandidate reports whether a filename may carry SOPS metadata this connector
// reads: the `.sops.yaml` rules file, or any YAML/JSON document (which may embed a
// top-level `sops:` block). The encrypted-vs-not decision is made by the parser,
// not the extension.
func isCandidate(name string) bool {
	if name == sopsRulesFile {
		return true
	}
	switch strings.ToLower(filepath.Ext(name)) {
	case ".yaml", ".yml", ".json":
		return true
	default:
		return false
	}
}

// root returns the directory the ResourceRef paths are made relative to: the
// configured directory itself, or the directory containing the configured single
// file.
func (s *Source) root() (string, error) {
	fi, err := os.Stat(s.path)
	if err != nil {
		return "", err
	}
	if fi.IsDir() {
		return s.path, nil
	}
	return filepath.Dir(s.path), nil
}

// relRef returns the clean, forward-slash path of file relative to root, for use as
// a stable ResourceRef. It falls back to the file's base name if relativization
// fails (e.g. across volumes), never to an absolute path.
func relRef(root, file string) string {
	rel, err := filepath.Rel(root, file)
	if err != nil {
		rel = filepath.Base(file)
	}
	return filepath.ToSlash(filepath.Clean(rel))
}

func (s *Source) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}
