// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package release

import (
	"crypto/ed25519"
	"crypto/rand"
	"strings"
	"testing"
)

func TestMonthlyReleaseOrdering(t *testing.T) {
	groups := [][]string{{"v26.9.0"}, {"26.10.0", "26.10"}, {"26.10.1"}, {"26.11"}, {"26.11.1"}, {"26.12"}, {"27.1"}}
	for i, left := range groups {
		for j, right := range groups {
			for _, a := range left {
				for _, b := range right {
					av, err := ParseVersion(a)
					if err != nil {
						t.Fatalf("parse %s: %v", a, err)
					}
					bv, err := ParseVersion(b)
					if err != nil {
						t.Fatalf("parse %s: %v", b, err)
					}
					want := 0
					if i < j {
						want = -1
					}
					if i > j {
						want = 1
					}
					if got := Compare(av, bv); got != want {
						t.Fatalf("Compare(%s,%s)=%d want %d", a, b, got, want)
					}
					if av.Raw != a || bv.Raw != b {
						t.Fatal("display version was normalized")
					}
				}
			}
		}
	}
}

func TestMonthlySignedManifestUpgrade(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	m := goodManifest()
	m.Version = "26.11"
	m.MinVersion = "26.10"
	for i := range m.Artifacts {
		m.Artifacts[i].Filename = strings.ReplaceAll(m.Artifacts[i].Filename, "26.8.0", "26.11")
	}
	body, sig := signManifestBytes(t, m, priv)
	verified, err := VerifyManifest(body, sig, pub)
	if err != nil {
		t.Fatal(err)
	}
	for _, current := range []string{"26.10.0", "26.10.1", "26.11", "26.11.1"} {
		p, err := verified.PlanUpgrade(current, "linux", "amd64", "monthly-node", m.ReleasedAt)
		if err != nil {
			t.Fatal(err)
		}
		want := 1
		if current == "26.11" {
			want = 0
		}
		if current == "26.11.1" {
			want = -1
		}
		if p.Direction != want || p.MinTooOld || !p.HasArtifact {
			t.Fatalf("%s -> 26.11: %+v", current, p)
		}
	}
	if verified.Version != "26.11" {
		t.Fatal("signed display version changed")
	}
}

func TestMonthlyChannelArtifactURLs(t *testing.T) {
	for _, row := range []struct{ version, tag string }{{"26.11", "26.11"}, {"26.11.1", "26.11.1"}, {"26.10.0", "26.10.0"}, {"26.9.0", "v26.9.0"}} {
		l, err := ResolveChannel("https://github.com/olivaresai/olivares", ChannelStable)
		if err != nil {
			t.Fatal(err)
		}
		name := "olivares_" + row.version + "_linux_amd64.tar.gz"
		got, err := l.ArtifactURL(row.version, name)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(got, "/download/"+row.tag+"/"+name) {
			t.Fatalf("wrong artifact address %s", got)
		}
	}
}
