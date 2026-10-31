// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package skills

import (
	"errors"
	"os"
	"path"
	"sync"
)

var syncStagedFile = func(f *os.File) error { return f.Sync() }

// Bound open files and concurrent flushes without serializing large revisions or
// flushing unrelated data on the shared filesystem. All flushes finish before return.
func syncStagedTree(root *os.Root, revision string, manifest []FileEntry) error {
	for batch := 0; batch < len(manifest); batch += 8 {
		entries := manifest[batch:min(batch+8, len(manifest))]
		errs := make([]error, len(entries))
		var workers sync.WaitGroup
		for i, entry := range entries {
			workers.Go(func() {
				file, err := root.Open(path.Join(revision, entry.Path))
				if err != nil {
					errs[i] = err
					return
				}
				syncErr := syncStagedFile(file)
				closeErr := file.Close()
				errs[i] = errors.Join(syncErr, closeErr)
			})
		}
		workers.Wait()
		if err := errors.Join(errs...); err != nil {
			return err
		}
	}
	return nil
}
