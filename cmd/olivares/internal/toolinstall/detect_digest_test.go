// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package toolinstall

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// dev#2614: Detect hashes every vendor-default and PATH candidate it finds, which the CLI's
// JSON reports. A caller that keeps only origin, match and the executable bit asks for
// NoDigest and reads none of them; everything else about the candidates is the same.
func TestDetectNoDigestReadsNoCandidateAndChangesNothingElse(t *testing.T) {
	f := newFixture(t)
	home, pathDir := t.TempDir(), t.TempDir()
	vendor := filepath.Join(home, ".local", "share", "claude", "versions", "2.1.292")
	link := filepath.Join(home, ".local", "bin", "claude")
	for _, d := range []string{filepath.Dir(vendor), filepath.Dir(link)} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range []string{vendor, filepath.Join(pathDir, "claude")} {
		if err := os.WriteFile(p, []byte("#!/bin/sh\n# "+p+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(vendor, link); err != nil {
		t.Fatal(err)
	}
	opts := DetectOptions{Driver: "claude", Home: home, PathEnv: pathDir}

	var with []Candidate
	if n := hashesDuring(t, func() {
		var err error
		if with, err = f.engine.Detect(context.Background(), opts); err != nil {
			t.Fatal(err)
		}
	}); n == 0 {
		t.Fatal("the default detection hashed nothing: this test measures nothing")
	}
	if len(with) != 2 {
		t.Fatalf("candidates %+v, want the vendor file (the symlink is its alias) and the PATH copy", with)
	}
	for _, c := range with {
		if len(c.SHA256) != 64 {
			t.Fatalf("the default detection dropped the sha256 of %s: %+v", c.Path, c)
		}
	}

	opts.NoDigest = true
	var without []Candidate
	if n := hashesDuring(t, func() {
		var err error
		if without, err = f.engine.Detect(context.Background(), opts); err != nil {
			t.Fatal(err)
		}
	}); n != 0 {
		t.Fatalf("a detection without digests hashed %d file(s)", n)
	}
	for i := range with {
		with[i].SHA256 = ""
	}
	if !reflect.DeepEqual(with, without) {
		t.Fatalf("NoDigest changed the observation beyond the digest:\n with:    %+v\n without: %+v", with, without)
	}

	// Probing and manifest corroboration compare the digest: asking for both is a mistake, not a silent skip.
	for name, bad := range map[string]DetectOptions{
		"probe":      {Probe: true},
		"probe path": {ProbePaths: []string{vendor}},
		"material":   {Material: &Material{}},
	} {
		bad.Driver, bad.Home, bad.PathEnv, bad.NoDigest = "claude", home, pathDir, true
		if _, err := f.engine.Detect(context.Background(), bad); KindOf(err) != KindInvalidRequest {
			t.Fatalf("%s with NoDigest: %v, want %s", name, err, KindInvalidRequest)
		}
	}
}
