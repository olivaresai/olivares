// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package skills

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"testing"
	"testing/fstest"
)

const damagePack = "ecc-security"

// damaged serves the real pack under a pin changed by edit, in place of the embedded catalog.
func damaged(t *testing.T, edit func(pin map[string]any, files fstest.MapFS)) {
	t.Helper()
	archive, err := fs.ReadFile(builtinEmbedded, "builtin/"+damagePack+".tar.gz")
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(archive)
	pin := map[string]any{
		"sources": map[string]any{"ecc": map[string]any{"repository": "https://example.test/ecc", "commit": "abc"}},
		"packs":   []any{map[string]any{"id": damagePack, "source": "ecc", "file": damagePack + ".tar.gz", "sha256": hex.EncodeToString(sum[:])}},
	}
	files := fstest.MapFS{"builtin/" + damagePack + ".tar.gz": {Data: archive}}
	edit(pin, files)
	raw, err := json.Marshal(pin)
	if err != nil {
		t.Fatal(err)
	}
	files["builtin/PIN.json"] = &fstest.MapFile{Data: raw}
	previous := builtinData
	builtinData = files
	t.Cleanup(func() { builtinData = previous })
}

func refusalCode(t *testing.T, err error) string {
	t.Helper()
	var refusal *ImportError
	if !errors.As(err, &refusal) {
		t.Fatalf("not an import refusal: %v", err)
	}
	return refusal.Code
}

func TestBuiltinImportRefusesADamagedCatalog(t *testing.T) {
	archivePath := "builtin/" + damagePack + ".tar.gz"
	for name, tc := range map[string]struct {
		edit func(pin map[string]any, files fstest.MapFS)
		code string
	}{
		"the pinned digest is wrong": {func(pin map[string]any, _ fstest.MapFS) {
			pin["packs"].([]any)[0].(map[string]any)["sha256"] = "0011"
		}, "source_changed"},
		"the archive changed after pinning": {func(_ map[string]any, files fstest.MapFS) {
			files[archivePath].Data = append(append([]byte{}, files[archivePath].Data...), 0)
		}, "source_changed"},
		"the pin carries no digest": {func(pin map[string]any, _ fstest.MapFS) {
			pin["packs"].([]any)[0].(map[string]any)["sha256"] = ""
		}, "source_unavailable"},
		"the pin names no commit": {func(pin map[string]any, _ fstest.MapFS) {
			pin["sources"].(map[string]any)["ecc"].(map[string]any)["commit"] = ""
		}, "source_unavailable"},
		"the pinned source is unknown": {func(pin map[string]any, _ fstest.MapFS) {
			pin["packs"].([]any)[0].(map[string]any)["source"] = "other"
		}, "source_unavailable"},
		"the archive is missing": {func(_ map[string]any, files fstest.MapFS) {
			delete(files, archivePath)
		}, "source_unavailable"},
	} {
		t.Run(name, func(t *testing.T) {
			damaged(t, tc.edit)
			if pack, _, err := importBuiltin(context.Background(), damagePack); err == nil || pack != nil || refusalCode(t, err) != tc.code {
				t.Fatalf("pack=%v err=%v, want refusal %s", pack != nil, err, tc.code)
			}
		})
	}
	t.Run("the pin is not JSON", func(t *testing.T) {
		damaged(t, func(map[string]any, fstest.MapFS) {})
		builtinData.(fstest.MapFS)["builtin/PIN.json"] = &fstest.MapFile{Data: []byte("{")}
		if _, _, err := importBuiltin(context.Background(), damagePack); err == nil || refusalCode(t, err) != "source_unavailable" {
			t.Fatalf("err=%v", err)
		}
	})
	t.Run("an undamaged copy imports", func(t *testing.T) {
		damaged(t, func(map[string]any, fstest.MapFS) {})
		pack, source, err := importBuiltin(context.Background(), damagePack)
		if err != nil || len(pack.Members) == 0 || source.Kind != "builtin" || source.ResolvedCommit != "abc" {
			t.Fatalf("control import failed: %v %+v", err, source)
		}
	})
}

// The glob in builtin.go decides what ships in the binary; the pin decides what can be installed.
// They must name the same files, and the agents and rules under data/ stay out of the binary.
func TestBuiltinEmbedHoldsExactlyThePinnedPacks(t *testing.T) {
	var pin builtinPin
	raw, err := fs.ReadFile(builtinEmbedded, "builtin/PIN.json")
	if err != nil || json.Unmarshal(raw, &pin) != nil {
		t.Fatalf("embedded pin unreadable: %v", err)
	}
	want := map[string]bool{}
	for _, p := range pin.Packs {
		want[p.File] = true
	}
	entries, err := fs.ReadDir(builtinEmbedded, "builtin")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, e := range entries {
		if e.Name() != "PIN.json" {
			got[e.Name()] = true
		}
	}
	if len(want) == 0 || len(got) != len(want) {
		t.Fatalf("embedded archives %d, pinned packs %d", len(got), len(want))
	}
	for name := range want {
		if !got[name] {
			t.Errorf("pinned archive %s is not embedded", name)
		}
	}
	if _, err := fs.Stat(builtinEmbedded, "builtin/data"); err == nil {
		t.Error("the agents and rules archives are embedded; nothing reads them")
	}
}
