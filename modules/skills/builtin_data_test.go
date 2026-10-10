// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package skills_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/modules/skills"
)

// The commits are written out here on purpose: a pin that moves must fail this
// test and be reviewed, not follow PIN.json silently.
const (
	eccCommit  = "c05b2d6614f62f6db0047669aa4eefb223d478f9"
	mattCommit = "d81f3a183412e71a5b1e84ca21bc1a35eea03a60"
)

type pinEntry struct {
	ID       string   `json:"id"`
	Source   string   `json:"source"`
	File     string   `json:"file"`
	SHA256   string   `json:"sha256"`
	Skills   int      `json:"skills"`
	Files    int      `json:"files"`
	Licenses []string `json:"licenses"`
}
type builtinPin struct {
	Schema  int `json:"schema"`
	Sources map[string]struct {
		Repository string `json:"repository"`
		Commit     string `json:"commit"`
		License    string `json:"license"`
	} `json:"sources"`
	Packs []pinEntry `json:"packs"`
	Data  []pinEntry `json:"data"`
}

func readPin(t *testing.T) builtinPin {
	t.Helper()
	raw, err := os.ReadFile("builtin/PIN.json")
	if err != nil {
		t.Fatal(err)
	}
	var pin builtinPin
	if err := json.Unmarshal(raw, &pin); err != nil {
		t.Fatal(err)
	}
	return pin
}

func TestBuiltinPinNamesTheReleasedSourcesAndTheirFullSelection(t *testing.T) {
	pin := readPin(t)
	if pin.Schema != 1 {
		t.Fatalf("PIN.json schema %d is not the one this loader reads", pin.Schema)
	}
	ids := map[string]bool{}
	for _, p := range pin.Packs {
		ids[p.ID] = true
		if p.File != p.ID+".tar.gz" || pin.Sources[p.Source].Commit == "" {
			t.Fatalf("pack %s: file %q or source %q is not pinned", p.ID, p.File, p.Source)
		}
	}
	for _, p := range pin.Data {
		ids[p.ID] = true
		if p.File != "data/"+p.ID+".tar.gz" || pin.Sources[p.Source].Commit == "" {
			t.Fatalf("data %s: file %q or source %q is not pinned", p.ID, p.File, p.Source)
		}
	}
	if len(ids) != len(pin.Packs)+len(pin.Data) {
		t.Fatal("PIN.json repeats an id")
	}
	if pin.Sources["ecc"].Commit != eccCommit || pin.Sources["mattpocock-skills"].Commit != mattCommit {
		t.Fatalf("pinned commits changed: %+v", pin.Sources)
	}
	skillsBySource := map[string]int{}
	for _, p := range pin.Packs {
		skillsBySource[p.Source] += p.Skills
	}
	// ECC v2.2.3 ships 293 skills and 8 are excluded (README.md, "Not shipped"); the two
	// mattpocock categories carry 20 and 7 and 1 is excluded. The rest are in exactly one pack.
	if skillsBySource["ecc"] != 285 || skillsBySource["mattpocock-skills"] != 26 || len(pin.Packs) != 23 || len(pin.Data) != 2 {
		t.Fatalf("catalog selection changed: skills=%v packs=%d data=%d", skillsBySource, len(pin.Packs), len(pin.Data))
	}
}

func TestBuiltinDataArchivesMatchThePinAndCarryTheirLicense(t *testing.T) {
	for _, entry := range readPin(t).Data {
		t.Run(entry.ID, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join("builtin", entry.File))
			if err != nil {
				t.Fatal(err)
			}
			sum := sha256.Sum256(raw)
			if hex.EncodeToString(sum[:]) != entry.SHA256 {
				t.Fatalf("%s differs from its pin", entry.File)
			}
			gz, err := gzip.NewReader(bytes.NewReader(raw))
			if err != nil {
				t.Fatal(err)
			}
			tr := tar.NewReader(gz)
			names := map[string]bool{}
			var license []byte
			for {
				h, err := tr.Next()
				if err == io.EOF {
					break
				}
				if err != nil {
					t.Fatal(err)
				}
				names[h.Name] = true
				if h.Name == "LICENSE" {
					if license, err = io.ReadAll(tr); err != nil {
						t.Fatal(err)
					}
				}
			}
			if want, err := os.ReadFile("../../LICENSES/third-party/" + entry.Source + ".txt"); err != nil || !bytes.Equal(license, want) {
				t.Fatalf("%s: its LICENSE is not LICENSES/third-party/%s.txt (%v)", entry.ID, entry.Source, err)
			}
			if !names["LICENSE"] || len(names) != entry.Files {
				t.Fatalf("%s: LICENSE=%v files=%d, pinned %d", entry.ID, names["LICENSE"], len(names), entry.Files)
			}
		})
	}
}

// Every shipped source needs its license text and its NOTICE entry in the one
// NOTICE file each edition's generated notice starts from (scripts/license-gate.py).
func TestBuiltinSourcesAreNoticedWithTheirLicenseText(t *testing.T) {
	pin := readPin(t)
	notice, err := os.ReadFile("../../NOTICE")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range pin.Packs {
		raw, err := os.ReadFile(filepath.Join("builtin", entry.File))
		if err != nil {
			t.Fatal(err)
		}
		verified, err := skills.ImportArchive(context.Background(), bytes.NewReader(raw), "tar.gz", entry.SHA256)
		if err != nil {
			var refusal *skills.ImportError
			if errors.As(err, &refusal) {
				err = fmt.Errorf("%w (code %q, detail %q)", err, refusal.Code, refusal.Detail)
			}
			t.Errorf("%s: %v", entry.ID, err)
			continue
		}
		if license, _ := verified.File("LICENSE"); !licenseMatches(t, entry.Source, license) {
			t.Errorf("%s: its LICENSE is not LICENSES/third-party/%s.txt", entry.ID, entry.Source)
		}
		// A skill under another license needs its own NOTICE entry before it ships.
		for _, m := range verified.Members {
			if m.License != "" && m.License != "MIT" {
				t.Errorf("%s/%s declares license %q", entry.ID, m.Name, m.License)
			}
		}
	}
	for id, source := range pin.Sources {
		text, err := os.ReadFile(filepath.Join("../../LICENSES/third-party", id+".txt"))
		if err != nil || !bytes.HasPrefix(text, []byte(source.License+" License")) {
			t.Errorf("%s: license text missing or not %s: %v", id, source.License, err)
		}
		if !strings.Contains(string(notice), source.Repository) || !strings.Contains(string(notice), "LICENSES/third-party/"+id+".txt") {
			t.Errorf("NOTICE does not name %s and its license text", id)
		}
	}
}

func licenseMatches(t *testing.T, source string, got []byte) bool {
	t.Helper()
	want, err := os.ReadFile("../../LICENSES/third-party/" + source + ".txt")
	if err != nil {
		t.Fatal(err)
	}
	return bytes.Equal(got, want)
}
