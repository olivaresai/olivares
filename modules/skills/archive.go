// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package skills

import (
	"archive/zip"
	"context"
	"os"
)

// Zip packages the validated selected snapshot for CLI upload. The server
// independently validates it; the local path is never sent to the engine.
func (p *ValidatedPack) Zip(ctx context.Context) ([]byte, error) {
	var out limitedBuffer
	out.max = MaxSourceBytes
	w := zip.NewWriter(&out)
	for _, entry := range p.Manifest {
		if err := ctx.Err(); err != nil {
			_ = w.Close()
			return nil, err
		}
		body, ok := p.File(entry.Path)
		if !ok || !safePath(entry.Path) || digest(body) != entry.Digest || int64(len(body)) != entry.Size || (entry.Mode != 0644 && entry.Mode != 0755) {
			_ = w.Close()
			return nil, refuse("source_changed", "folder upload snapshot")
		}
		header := &zip.FileHeader{Name: entry.Path, Method: zip.Deflate}
		header.SetMode(os.FileMode(entry.Mode))
		file, err := w.CreateHeader(header)
		if err != nil {
			_ = w.Close()
			return nil, refuse("import_limit", "folder upload bytes")
		}
		if _, err := file.Write(body); err != nil {
			_ = w.Close()
			return nil, refuse("import_limit", "folder upload bytes")
		}
	}
	if err := w.Close(); err != nil {
		return nil, refuse("import_limit", "folder upload bytes")
	}
	return out.data.Bytes(), nil
}
