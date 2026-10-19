// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build linux

package skills_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/olivaresai/olivares/modules/skills"
	"golang.org/x/sys/unix"
)

func TestSkillsFolderSnapshotRefusesLinks(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "research")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(harmless), 0600); err != nil {
		t.Fatal(err)
	}
	p, err := skills.ImportFolder(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	zipPack, err := skills.ImportArchive(context.Background(), bytes.NewReader(archive(t, []string{"research/SKILL.md"}, []string{harmless})), "zip", "")
	if err != nil {
		t.Fatal(err)
	}
	if p.ManifestDigest != zipPack.ManifestDigest {
		t.Fatal("folder bytes do not match uploaded bytes")
	}
	secret := filepath.Join(root, "outside")
	if err := os.WriteFile(secret, []byte("private fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"symlink", "hardlink"} {
		t.Run(kind, func(t *testing.T) {
			name := filepath.Join(dir, "link")
			var err error
			if kind == "symlink" {
				err = os.Symlink(secret, name)
			} else {
				err = os.Link(secret, name)
			}
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Remove(name) })
			if p, err := skills.ImportFolder(context.Background(), dir); p != nil || err == nil {
				t.Fatal("linked content was imported")
			}
		})
	}
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(dir, alias); err != nil {
		t.Fatal(err)
	}
	if p, err := skills.ImportFolder(context.Background(), alias); p != nil || err == nil {
		t.Fatal("symlink root was imported")
	}
}

func TestSkillsFolderDoesNotImportParentsOfSkillAndAccountTrees(t *testing.T) {
	root := t.TempDir()
	pack := filepath.Join(root, "engineering")
	for _, dir := range []string{"research", "account"} {
		if err := os.MkdirAll(filepath.Join(pack, dir), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(pack, "research", "SKILL.md"), []byte(harmless), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pack, "account", "token"), []byte("private fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	if p, err := skills.ImportFolder(context.Background(), pack); p != nil || err == nil {
		t.Fatal("selected parent included a non-skill account tree")
	}
}

// Observe the real file-close event and change that file at the next context
// checkpoint. This models a source writer between two file reads without timing
// sleeps or an importer-internal callback.
type changingFolderContext struct {
	context.Context
	watch   int
	file    string
	changed bool
	failure error
}

func (c *changingFolderContext) Err() error {
	if !c.changed {
		var events [4096]byte
		n, err := unix.Read(c.watch, events[:])
		if err != nil && !errors.Is(err, unix.EAGAIN) {
			c.failure = err
		}
		for offset := 0; offset+unix.SizeofInotifyEvent <= n; {
			mask := binary.NativeEndian.Uint32(events[offset+4 : offset+8])
			length := int(binary.NativeEndian.Uint32(events[offset+12 : offset+16]))
			if mask&unix.IN_CLOSE_NOWRITE != 0 {
				c.changed = true
				c.failure = os.WriteFile(c.file, []byte(harmless+"changed after its read\n"), 0600)
				break
			}
			offset += unix.SizeofInotifyEvent + length
		}
	}
	return c.Context.Err()
}
func TestSkillsFolderRefusesFilesChangedAfterTheirRead(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "research")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "SKILL.md")
	if err := os.WriteFile(file, []byte(harmless), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "support.md"), []byte("support fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	watch, err := unix.InotifyInit1(unix.IN_CLOEXEC | unix.IN_NONBLOCK)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(watch)
	if _, err := unix.InotifyAddWatch(watch, file, unix.IN_CLOSE_NOWRITE); err != nil {
		t.Fatal(err)
	}
	ctx := &changingFolderContext{Context: context.Background(), watch: watch, file: file}
	pack, err := skills.ImportFolder(ctx, dir)
	if ctx.failure != nil {
		t.Fatal(ctx.failure)
	}
	if !ctx.changed {
		t.Fatal("source-change fixture was not exercised")
	}
	var refusal *skills.ImportError
	if pack != nil || !errors.As(err, &refusal) || refusal.Code != "source_changed" {
		t.Fatalf("file changed after read was published: %v", err)
	}
}

func TestNativeComponentFolderSnapshotsMatchArchives(t *testing.T) {
	for name, paths := range map[string][]string{
		"native": {"agents/review.md", "commands/review.md", "rules/common/testing.md"},
		"mixed":  {"skills/research/SKILL.md", "commands/review.md"},
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			bodies := make([]string, len(paths))
			for i, name := range paths {
				body := "# Instructions\nReview the project.\n"
				if name == "agents/review.md" {
					body = "---\nname: review\ndescription: Review changes.\n---\nRead changes.\n"
				} else if name == "skills/research/SKILL.md" {
					body = harmless
				}
				bodies[i] = body
				file := filepath.Join(root, filepath.FromSlash(name))
				if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(file, []byte(body), 0600); err != nil {
					t.Fatal(err)
				}
			}
			folder, err := skills.ImportFolder(t.Context(), root)
			if err != nil {
				t.Fatalf("native component folder refused: %v", err)
			}
			zipPack, err := skills.ImportArchive(t.Context(), bytes.NewReader(archive(t, paths, bodies)), "zip", "")
			if err != nil || folder.ManifestDigest != zipPack.ManifestDigest {
				t.Fatalf("folder/archive manifests differ: %+v %v", zipPack, err)
			}
		})
	}
}

func TestNativeComponentFoldersRefuseAccountFilesBeforeReading(t *testing.T) {
	for _, name := range []string{"rules/account/token", "agents/account/token", "commands/token", "skills/account/token"} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			file := filepath.Join(root, filepath.FromSlash(name))
			if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(file, []byte("private fixture"), 0600); err != nil {
				t.Fatal(err)
			}
			watch, err := unix.InotifyInit1(unix.IN_CLOEXEC | unix.IN_NONBLOCK)
			if err != nil {
				t.Fatal(err)
			}
			defer unix.Close(watch)
			if _, err := unix.InotifyAddWatch(watch, file, unix.IN_ACCESS); err != nil {
				t.Fatal(err)
			}
			if pack, err := skills.ImportFolder(t.Context(), root); pack != nil || err == nil {
				t.Fatal("native namespace admitted a non-instruction account file")
			}
			var events [4096]byte
			if n, err := unix.Read(watch, events[:]); n != 0 && !errors.Is(err, unix.EAGAIN) {
				t.Fatalf("account bytes were read before refusal: %d %v", n, err)
			}
		})
	}
}
