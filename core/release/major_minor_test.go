// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package release

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestMajorMinorReleaseGrammar(t *testing.T) {
	for _, s := range []string{"1.0", "1.1", "1.10", "1.299", "2.0"} {
		if _, err := ParseVersion(s); err != nil {
			t.Errorf("ParseVersion(%q): %v", s, err)
		}
	}
	for _, s := range []string{"1.0.1", "26.10.2", "v1.0", "1.0-rc.1", "1.0+meta", "+1.0", "1.-1", " 1.0", "1.0\n"} {
		if _, err := ParseVersion(s); err == nil {
			t.Errorf("ParseVersion(%q) accepted a non-release version", s)
		}
	}
	previous, _ := ParseVersion("1.9")
	for _, s := range []string{"1.10", "1.299", "2.0"} {
		next, _ := ParseVersion(s)
		if !previous.Newer(next) || next.Newer(previous) {
			t.Errorf("numeric ordering failed: 1.9 -> %s", s)
		}
	}
}

func TestMajorMinorArtifactURL(t *testing.T) {
	l, err := ResolveChannel("https://github.com/olivaresai/olivares", ChannelStable)
	if err != nil {
		t.Fatal(err)
	}
	for _, version := range []string{"1.0", "1.10", "2.0"} {
		got, err := l.ArtifactURL(version, "olivares.tar.gz")
		if err != nil || !strings.Contains(got, "/download/"+version+"/olivares.tar.gz") {
			t.Errorf("ArtifactURL(%q) = %q, %v", version, got, err)
		}
	}
	for _, version := range []string{"1.0.1", "26.10.2"} {
		if _, err := l.ArtifactURL(version, "olivares.tar.gz"); err == nil {
			t.Errorf("ArtifactURL(%q) accepted a patch release", version)
		}
	}
}

func TestManifestReleaseVersionGrammar(t *testing.T) {
	valid := []string{"1.0", "1.1", "1.10", "1.299", "2.0"}
	invalid := []string{"dev", "vdev", "v", "1.0.1", "26.10.2", "v1.0", "1.0-rc.1", "1.0+meta", " 1.0", "1.0\n", " ", ""}
	for _, field := range []string{"version", "min_version"} {
		for i, value := range append(valid, invalid...) {
			t.Run(field+"/"+value, func(t *testing.T) {
				m := goodManifest()
				if field == "version" {
					m.Version = value
				} else {
					m.MinVersion = value
				}
				b, err := json.Marshal(m)
				if err != nil {
					t.Fatal(err)
				}
				_, err = ParseManifest(b)
				wantValid := i < len(valid) || (field == "min_version" && value == "")
				if (err == nil) != wantValid {
					t.Fatalf("ParseManifest %s=%q: %v; want valid=%t", field, value, err, wantValid)
				}
				if err != nil && !strings.Contains(err.Error(), field) {
					t.Fatalf("error does not identify %s: %v", field, err)
				}
			})
		}
	}
}
