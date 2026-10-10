// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package skills

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"io/fs"
)

// The built-in catalog is vendored data (builtin/README.md): packs of upstream skills
// pinned by commit and archive digest in builtin/PIN.json. A pack is validated by
// ImportArchive like any uploaded archive, against the pinned digest, and is
// published as an immutable revision. Nothing in it runs at install.
//
//go:embed builtin/PIN.json builtin/*.tar.gz
var builtinEmbedded embed.FS

// builtinData is the embedded catalog; a test replaces it to damage a pin.
var builtinData fs.FS = builtinEmbedded

type builtinPin struct {
	Sources map[string]struct {
		Repository string `json:"repository"`
		Commit     string `json:"commit"`
	} `json:"sources"`
	Packs []struct {
		ID     string `json:"id"`
		Source string `json:"source"`
		File   string `json:"file"`
		SHA256 string `json:"sha256"`
	} `json:"packs"`
}

// importBuiltin validates the built-in pack named id. An unpinned, unreadable or
// altered pack is refused; there is no fallback to another source.
func importBuiltin(ctx context.Context, id string) (*ValidatedPack, Source, error) {
	raw, err := fs.ReadFile(builtinData, "builtin/PIN.json")
	var pin builtinPin
	if err != nil || json.Unmarshal(raw, &pin) != nil {
		return nil, Source{}, refuse("source_unavailable", "built-in pin unreadable")
	}
	for _, entry := range pin.Packs {
		if entry.ID != id {
			continue
		}
		source, known := pin.Sources[entry.Source]
		if !known || source.Commit == "" {
			return nil, Source{}, refuse("source_unavailable", "built-in pin names no source commit for "+id)
		}
		// ImportArchive skips the digest check when none is given: the pin must carry one.
		if entry.SHA256 == "" {
			return nil, Source{}, refuse("source_unavailable", "built-in pin has no digest for "+id)
		}
		archive, err := fs.ReadFile(builtinData, "builtin/"+entry.File)
		if err != nil {
			return nil, Source{}, refuse("source_unavailable", "built-in archive missing for "+id)
		}
		pack, err := ImportArchive(ctx, bytes.NewReader(archive), "tar.gz", entry.SHA256)
		if err != nil {
			return nil, Source{}, err
		}
		return pack, Source{Kind: "builtin", Origin: source.Repository, RequestedRef: entry.ID, ResolvedCommit: source.Commit, SourceDigest: pack.SourceDigest}, nil
	}
	return nil, Source{}, refuse("unsupported_source", "built-in pack")
}
