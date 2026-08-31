// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package fscontent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"

	"github.com/olivaresai/olivares/connectors/contentsource"
	"github.com/olivaresai/olivares/sdk"
)

// Name is the connector's globally unique identifier.
const Name = "olivares.fs-content"

// listPageSize is how many DocRefs one List page returns.
const listPageSize = 100

// ErrDocumentNotFound is returned by Fetch for an id the walk did not index.
var ErrDocumentNotFound = errors.New("fscontent: document not found")

// Source is the filesystem knowledge content source. The zero value is not usable;
// call New. It holds a confined os.Root over the configured directory and an in-memory
// index of the walked files (metadata only — bodies are read lazily at Fetch).
type Source struct {
	sc        *sourceConfig
	root      *os.Root
	index     []fileEntry
	byRel     map[string]fileEntry
	stats     walkStats
	extractor contentsource.RichDocExtractor
}

var (
	_ contentsource.Source               = (*Source)(nil)
	_ contentsource.CompletenessReporter = (*Source)(nil)
)

// Option configures a Source at construction.
type Option func(*Source)

// WithExtractor injects the sandboxed rich-document extractor the engine provides.
// With it set, the connector ingests OOXML files (DOCX/PPTX/XLSX) by handing their
// bytes to the out-of-process extractor; without it, rich documents are skipped and
// counted (the connectors module must never build the extractor itself — it lives in
// the AGPL engine, which owns the plugin-confinement sandbox). A nil extractor is
// ignored, so the option is always safe to pass.
func WithExtractor(ext contentsource.RichDocExtractor) Option {
	return func(s *Source) {
		if ext != nil {
			s.extractor = ext
		}
	}
}

// New returns a filesystem content source.
func New(opts ...Option) *Source {
	s := &Source{}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// Descriptor returns the connector's self-description and configuration schema.
func (s *Source) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:        Name,
		Version:     "0.1.0",
		APIVersion:  sdk.APIVersion,
		Type:        sdk.TypeContentSource,
		Title:       "File server (local / NFS / SMB content)",
		Surfaces:    []string{"knowledge.document"},
		Description: "Ingests files from a directory tree as governed knowledge documents (read confined to the root by construction — symlink-escape and traversal refused — with POSIX owner/group/ACL mapped to Document ACLs and xattr classification). Distinct from the filelog log SINK.",
		ConfigFields: []sdk.ConfigField{
			{Key: fMode, Type: sdk.FieldString, Default: "live", Description: "\"live\" (default; reads the real tree) or \"export\" (declared; a snapshot is never presented as live)"},
			{Key: fRoot, Type: sdk.FieldString, Description: "the directory tree to ingest (a local path or an NFS/SMB mount). All access is confined to this root."},
			{Key: fInclude, Type: sdk.FieldString, Description: "comma-separated globs (relative to root or basename) to include; empty = everything"},
			{Key: fExclude, Type: sdk.FieldString, Description: "comma-separated globs to exclude"},
			{Key: fMaxFileBytes, Type: sdk.FieldString, Default: "1048576", Description: "per-file body cap in bytes (larger files are truncated and marked); hard-capped at 1 MiB"},
			{Key: fMaxFiles, Type: sdk.FieldString, Default: "100000", Description: "maximum files the walk ingests (I/O budget for a large mount)"},
			{Key: fMaxTotalBytes, Type: sdk.FieldString, Default: "1073741824", Description: "total read-budget in bytes for a full walk"},
			{Key: fTextOnly, Type: sdk.FieldBool, Default: "true", Description: "ingest only text/document extensions; binaries are skipped and counted"},
			{Key: fMapPOSIXACL, Type: sdk.FieldBool, Default: "true", Description: "map POSIX owner/group + POSIX.1e ACL entries to Document ACLs"},
			{Key: fClassifyDef, Type: sdk.FieldString, Description: "default classification label for every file (an xattr overrides it per file)"},
			{Key: fClassXattr, Type: sdk.FieldString, Default: "user.classification", Description: "xattr read as a per-file classification label"},
			{Key: fLabelsXattr, Type: sdk.FieldString, Default: "user.olivares.labels", Description: "xattr read as comma-separated external sensitivity labels for retrieval DLP"},
			{Key: fUserPrefix, Type: sdk.FieldString, Default: "user:", Description: "prefix for a user principal reference in the mapped ACL"},
			{Key: fGroupPrefix, Type: sdk.FieldString, Default: "group:", Description: "prefix for a group principal reference in the mapped ACL"},
			{Key: fSpaceRef, Type: sdk.FieldString, Description: "logical container label for provenance (defaults to file:<root>)"},
		},
	}
}

// Kind declares this source ingests knowledge documents.
func (s *Source) Kind() contentsource.ContentClass { return contentsource.ClassDocument }

// Open validates configuration, opens the confined root, and walks the tree into an
// index (metadata only). A source with no root opens as an EMPTY source (declared
// offline), never a hard failure.
func (s *Source) Open(_ context.Context, cfg sdk.Config) error {
	sc, err := parseConfig(cfg)
	if err != nil {
		return err
	}
	// Rich-document extraction is enabled iff the engine injected an extractor. The
	// walk uses this to decide whether OOXML files are indexed at all (otherwise they
	// remain a counted binary skip, as before this connector could extract them).
	sc.extractor = s.extractor
	sc.extractRichDocs = s.extractor != nil
	s.sc = &sc
	s.byRel = map[string]fileEntry{}
	if sc.root == "" {
		return nil
	}
	fi, err := os.Stat(sc.root)
	if err != nil {
		return fmt.Errorf("fscontent: cannot stat root %q: %w", sc.root, err)
	}
	if !fi.IsDir() {
		return fmt.Errorf("fscontent: root %q is not a directory", sc.root)
	}
	root, err := os.OpenRoot(sc.root)
	if err != nil {
		return fmt.Errorf("fscontent: cannot open root %q: %w", sc.root, err)
	}
	s.root = root

	entries, stats, err := walk(root, &sc)
	if err != nil {
		_ = root.Close()
		s.root = nil
		return err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].rel < entries[j].rel })
	s.index = entries
	s.stats = stats
	for _, e := range entries {
		s.byRel[e.rel] = e
	}
	return nil
}

// List returns one page of document references from the walked index.
func (s *Source) List(ctx context.Context, cursor string) ([]contentsource.DocRef, string, error) {
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	start := 0
	if cursor != "" {
		n, err := strconv.Atoi(cursor)
		if err != nil || n < 0 {
			return nil, "", fmt.Errorf("fscontent: invalid cursor %q", cursor)
		}
		start = n
	}
	if start >= len(s.index) {
		return nil, "", nil
	}
	end := start + listPageSize
	if end > len(s.index) {
		end = len(s.index)
	}
	refs := make([]contentsource.DocRef, 0, end-start)
	for _, e := range s.index[start:end] {
		refs = append(refs, contentsource.DocRef{
			DocID:       e.rel,
			Title:       e.title,
			ContentType: e.contentType,
			ModifiedAt:  e.modTime,
		})
	}
	next := ""
	if end < len(s.index) {
		next = strconv.Itoa(end)
	}
	return refs, next, nil
}

// Fetch reads one indexed file's body confined to the root and returns its governed
// Document. Even a hostile docID is safe: os.Root refuses to open anything outside the
// root, so a traversal / symlink-escape id cannot read a foreign file.
func (s *Source) Fetch(ctx context.Context, docID string) (contentsource.Document, error) {
	if err := ctx.Err(); err != nil {
		return contentsource.Document{}, err
	}
	fe, ok := s.byRel[docID]
	if !ok || s.root == nil {
		return contentsource.Document{}, ErrDocumentNotFound
	}
	return readDocument(ctx, s.root, s.sc, fe)
}

// ListingComplete reports whether the boot walk enumerated the whole tree (no I/O
// budget cut-off and no swallowed read error). It is the contentsource.
// CompletenessReporter capability: when false, the knowledge module must NOT
// orphan-delete against this source's listing, because an absent document may be one
// the walk failed to reach (a transient NFS/SMB error) rather than one truly removed —
// deleting it would destroy data on a source blip. An unopened / rootless source is
// trivially complete (it lists nothing and deletes nothing).
func (s *Source) ListingComplete() bool { return s.stats.complete() }

// Close releases the confined root.
func (s *Source) Close(context.Context) error {
	if s.root != nil {
		err := s.root.Close()
		s.root = nil
		return err
	}
	return nil
}
