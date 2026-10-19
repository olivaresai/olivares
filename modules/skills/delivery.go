// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package skills

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// DeliveryFile is a verified regular file relative to the native skills folder.
// The runtime writes these original bytes without interpreting or executing them.
type DeliveryFile struct {
	Kind    string
	Scoped  bool
	Imports bool
	Path    string
	Bytes   []byte
	Mode    fs.FileMode
}

// ReadSelectionFiles reads only an admitted immutable selection. Every revision
// is reconstructed with the import validator and its original manifest digest.
// Retiring a pack disables new selections; existing conversations keep their pin.
func (m *Module) ReadSelectionFiles(ctx context.Context, sc store.Scope, selection Selection) ([]DeliveryFile, error) {
	encoded, err := json.Marshal(selection.Members)
	if err != nil || selection.Digest != digest(encoded) || len(selection.Members) > MaxSelectedMembers {
		return nil, refuse("source_changed", "selection")
	}
	packs := map[string]*ValidatedPack{}
	packIDs := map[string]string{}
	files := map[string]DeliveryFile{}
	ruleRank := map[string]int{}
	for _, member := range selection.Members {
		if member.Kind == "rule" {
			for _, origin := range member.Origins {
				ruleRank[member.Name] = max(ruleRank[member.Name], targetRank[origin.Kind])
			}
		}
	}
	total := int64(0)
	for _, selected := range selection.Members {
		if !canonicalID(selected.PackID) || !canonicalID(selected.RevisionID) {
			return nil, refuse("source_changed", "selection")
		}
		pack := packs[selected.RevisionID]
		if pack == nil {
			repo, err := sc.Ext(RevisionKind)
			if err != nil {
				return nil, err
			}
			row, err := repo.Get(ctx, model.ID(selected.RevisionID))
			if err != nil {
				return nil, err
			}
			rev, err := revisionDTO(row)
			if err != nil {
				return nil, err
			}
			if rev.PackID != selected.PackID || rev.ManifestDigest != selected.ManifestDigest || rev.Validator != ValidatorVersion {
				return nil, refuse("source_changed", "revision")
			}
			pack, err = m.readArtifact(ctx, sc.Tenant(), rev, &total)
			if err != nil {
				return nil, err
			}
			packs[selected.RevisionID] = pack
			packIDs[selected.RevisionID] = rev.PackID
		}
		matched := false
		var verified Member
		if pack.ManifestDigest != selected.ManifestDigest || packIDs[selected.RevisionID] != selected.PackID {
			return nil, refuse("source_changed", "revision")
		}
		for _, member := range pack.Members {
			if member.Kind == selected.Kind && member.Name == selected.Name && member.Directory == selected.Directory && member.SkillDigest == selected.SkillDigest && member.ContentDigest == selected.ContentDigest {
				matched = true
				verified = member
				break
			}
		}
		if !matched {
			return nil, refuse("source_changed", "member")
		}
		for _, entry := range pack.Manifest {
			if !verified.owns(entry.Path) {
				continue
			}
			name := path.Join(selected.Name, strings.TrimPrefix(entry.Path, selected.Directory+"/"))
			if verified.Kind != "" {
				name = strings.TrimPrefix(selected.Name, verified.Kind+":") + ".md"
				if verified.Kind == "rule" {
					// Sort surviving rules by effective target rank, deduplicating
					// identical revisions before native Markdown composition.
					name = fmt.Sprintf("%02d/%s", ruleRank[selected.Name], name)
				}
			}
			body, ok := pack.File(entry.Path)
			if !ok {
				return nil, refuse("source_changed", "artifact file")
			}
			mode := fs.FileMode(0444)
			if entry.Mode&0111 != 0 {
				mode = 0555
			}
			key := verified.Kind + "\x00" + name
			if prior, ok := files[key]; ok && (!bytes.Equal(prior.Bytes, body) || prior.Mode != mode) {
				return nil, refuse("skill_name_conflict", selected.Name)
			}
			files[key] = DeliveryFile{Kind: verified.Kind, Scoped: verified.Kind == "rule" && slices.Contains(verified.Extensions, "paths"), Imports: verified.Kind == "rule" && fileImport.Match(body), Path: name, Bytes: body, Mode: mode}
		}
	}
	if len(files) > MaxFiles {
		return nil, refuse("import_limit", "delivery files")
	}
	out := make([]DeliveryFile, 0, len(files))
	for _, file := range files {
		out = append(out, file)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Kind+"/"+out[i].Path < out[j].Kind+"/"+out[j].Path })
	// Claude recursively discovers rule files without promising directory order.
	// One native rule document gives the selected bodies an explicit order. The
	// immutable catalog, revision diff and approval still bind the original bytes.
	var rules bytes.Buffer
	var scoped, imports bool
	kept := out[:0]
	for _, file := range out {
		if file.Kind != "rule" {
			kept = append(kept, file)
			continue
		}
		fmt.Fprintf(&rules, "\n\n# %s\n\n", file.Path[3:])
		rules.Write(file.Bytes)
		scoped = scoped || file.Scoped
		imports = imports || file.Imports
	}
	if rules.Len() > 0 {
		kept = append(kept, DeliveryFile{Kind: "rule", Scoped: scoped, Imports: imports, Path: "selection.md", Bytes: rules.Bytes(), Mode: 0444})
	}
	return kept, nil
}

func (m *Module) readArtifact(ctx context.Context, tenant model.TenantID, rev Revision, total *int64) (*ValidatedPack, error) {
	if !filepath.IsAbs(m.opts.ArtifactRoot) || tenant.IsZero() || len(rev.Manifest) > MaxFiles {
		return nil, refuse("source_unavailable", "artifact root")
	}
	root, err := os.OpenRoot(m.opts.ArtifactRoot)
	if err != nil {
		return nil, refuse("source_unavailable", "artifact root")
	}
	defer root.Close()
	tree := newTree()
	for _, entry := range rev.Manifest {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !safePath(entry.Path) || entry.Size < 0 || entry.Size > MaxFileBytes {
			return nil, refuse("source_changed", "manifest")
		}
		*total += entry.Size
		if *total > MaxExpandedBytes {
			return nil, refuse("import_limit", "delivery bytes")
		}
		name := path.Join(tenant.String(), rev.ID, entry.Path)
		// Reject links in every component, including the tenant and revision.
		for component := name; component != "."; component = path.Dir(component) {
			info, err := root.Lstat(component)
			if err != nil || info.Mode()&os.ModeSymlink != 0 {
				return nil, refuse("source_changed", "artifact path")
			}
			if component == name && !info.Mode().IsRegular() || component != name && !info.IsDir() {
				return nil, refuse("source_changed", "artifact path")
			}
		}
		file, err := root.Open(name)
		if err != nil {
			return nil, refuse("source_unavailable", "artifact file")
		}
		info, statErr := file.Stat()
		if statErr != nil || !info.Mode().IsRegular() || info.Size() != entry.Size || (info.Mode()&0111 != 0) != (entry.Mode&0111 != 0) {
			file.Close()
			return nil, refuse("source_changed", "artifact file")
		}
		body, readErr := io.ReadAll(io.LimitReader(file, entry.Size+1))
		closeErr := file.Close()
		if readErr != nil || closeErr != nil || int64(len(body)) != entry.Size || digest(body) != entry.Digest {
			return nil, refuse("source_changed", "artifact bytes")
		}
		if err := tree.entry(entry.Path, false); err != nil {
			return nil, err
		}
		if err := tree.file(entry.Path, body, fs.FileMode(entry.Mode)); err != nil {
			return nil, err
		}
	}
	pack, err := tree.validate(rev.Source.SourceDigest)
	if err != nil || pack.ManifestDigest != rev.ManifestDigest {
		return nil, refuse("source_changed", "artifact manifest")
	}
	return pack, nil
}
