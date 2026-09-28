// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package portal

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestServe_ReReadsThePublishedSelectionWhenItChanges(t *testing.T) {
	if selectionPoll <= 0 || selectionPoll > time.Minute {
		t.Fatalf("the console reads the published selection again every %v", selectionPoll)
	}
	local := strings.Replace(publishedSelection, `"listen": "management"`, `"listen": "local"`, 1)

	t.Run("the same file is not a change", func(t *testing.T) {
		path := publish(t, t.TempDir(), publishedSelection)
		ownedByRoot(t, path)
		watch := watchSelection(path)
		if watch.Changed() || watch.Changed() {
			t.Fatal("an unchanged selection was read as changed")
		}
		writeFile(t, path, []byte(publishedSelection), 0o644)
		if watch.Changed() {
			t.Fatal("the same bytes written again were read as a change")
		}
	})

	t.Run("a rewritten selection is a change", func(t *testing.T) {
		path := publish(t, t.TempDir(), publishedSelection)
		ownedByRoot(t, path)
		watch := watchSelection(path)
		writeFile(t, path, []byte(local), 0o644)
		if !watch.Changed() {
			t.Fatal("a selection rewritten from management to local was not read as a change")
		}
	})

	t.Run("a selection that appears or disappears is a change", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "selection.json")
		watch := watchSelection(path)
		if watch.Changed() {
			t.Fatal("no selection, still none, was read as a change")
		}
		publish(t, dir, publishedSelection)
		ownedByRoot(t, path)
		if !watch.Changed() {
			t.Fatal("a selection published after the console started was not read as a change")
		}
		gone := watchSelection(path)
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if !gone.Changed() {
			t.Fatal("a removed selection was not read as a change")
		}
	})

	t.Run("a selection that stops passing its checks is a change", func(t *testing.T) {
		path := publish(t, t.TempDir(), publishedSelection)
		ownedByRoot(t, path)
		watch := watchSelection(path)
		if err := os.Chmod(path, 0o664); err != nil {
			t.Fatal(err)
		}
		if !watch.Changed() {
			t.Fatal("a selection that became group-writable was not read as a change")
		}
	})

	t.Run("the serving console stops at the first tick that sees a change", func(t *testing.T) {
		path := publish(t, t.TempDir(), publishedSelection)
		ownedByRoot(t, path)
		watch := watchSelection(path)
		ticks := make(chan time.Time)
		defer close(ticks)
		changed := selectionChanges(watch, ticks)
		stopped := func() bool {
			select {
			case <-changed:
				return true
			default:
				return false
			}
		}
		// A tick is taken only after the reading of the one before it ended, so after two ticks
		// the first reading has been decided.
		ticks <- time.Time{}
		ticks <- time.Time{}
		if stopped() {
			t.Fatal("the console stopped without a change")
		}
		writeFile(t, path, []byte(local), 0o644)
		ticks <- time.Time{}
		ticks <- time.Time{}
		if !stopped() {
			t.Fatal("the console kept serving a selection that was rewritten")
		}
		if never := selectionChanges(watch, nil); never == nil {
			t.Fatal("without ticks the console has no channel to wait on")
		}
	})
}
