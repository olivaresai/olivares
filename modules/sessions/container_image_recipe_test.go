// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The default session image has a recipe in this repository, and the package that
// installs the engine ships it: the name the engine looks for (DefaultContainerImage)
// would otherwise name an image nothing can produce. This pins the parts of the recipe
// a build cannot tell apart from a working one.

const (
	sessionImageRecipe = "packaging/container/session/Containerfile"
	sessionImageDst    = "/usr/share/olivares/session-image/Containerfile"
)

func repoFile(t *testing.T, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("%s: %v", rel, err)
	}
	return string(b)
}

// instructions are the recipe's lines without comments and blank lines, continuation
// lines joined into the instruction they continue.
func instructions(recipe string) []string {
	var out []string
	var open string
	for _, line := range strings.Split(recipe, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasSuffix(line, "\\") {
			open += strings.TrimSuffix(line, "\\") + " "
			continue
		}
		out = append(out, open+line)
		open = ""
	}
	return out
}

func TestSessionImageRecipe(t *testing.T) {
	froms, installs := 0, 0
	for _, in := range instructions(repoFile(t, sessionImageRecipe)) {
		switch {
		case strings.HasPrefix(in, "FROM "):
			froms++
			if !regexp.MustCompile(`^FROM \S+@sha256:[0-9a-f]{64}( AS \S+)?$`).MatchString(in) {
				t.Errorf("a base image is not pinned by digest: %q", in)
			}
		case strings.Contains(in, "apt-get install"):
			installs++
			// The how-to promises a shell, git, ssh, curl, jq and rg; the rest make a
			// coding session workable (TLS trust, a pager, ps). Each is one field of
			// the joined instruction, so a dropped package fails here instead of in
			// someone's session.
			fields := map[string]bool{}
			for _, field := range strings.Fields(in) {
				fields[field] = true
			}
			for _, tool := range []string{"ca-certificates", "curl", "git", "jq", "less", "openssh-client", "procps", "ripgrep"} {
				if !fields[tool] {
					t.Errorf("the install line does not install %s: %q", tool, in)
				}
			}
		}
		// The runner supplies these: the tool and its policy come from the host, the
		// user, home and program from the launch.
		for _, forbidden := range []string{"USER ", "ENTRYPOINT", "CMD ", "/etc/claude-code"} {
			if strings.Contains(in, forbidden) {
				t.Errorf("the recipe has %q in %q: the tool, its settings and the launch belong to the host and the runner", forbidden, in)
			}
		}
	}
	if froms == 0 || installs == 0 {
		t.Errorf("the recipe has %d FROM and %d install lines; it needs a pinned base and the tools", froms, installs)
	}
}

func TestSessionImageRecipeIsShippedWithThePackages(t *testing.T) {
	var recipe struct {
		Nfpms []struct {
			Formats  []string `json:"formats"`
			Contents []struct {
				Src      string `json:"src"`
				Dst      string `json:"dst"`
				Packager string `json:"packager"`
			} `json:"contents"`
		} `json:"nfpms"`
	}
	if err := json.Unmarshal([]byte(repoFile(t, "packaging/nfpm/packages.json")), &recipe); err != nil {
		t.Fatal(err)
	}
	if len(recipe.Nfpms) == 0 {
		t.Fatal("packages.json lists no package")
	}
	// Every package ships the recipe, Arch included: doctor's remediation names the
	// installed path on every kind of install, so one package without it is a dead end.
	for _, pkg := range recipe.Nfpms {
		found := false
		for _, c := range pkg.Contents {
			if c.Dst == sessionImageDst {
				found = true
				if c.Src != sessionImageRecipe || c.Packager != "" {
					t.Fatalf("%s comes from %q for packager %q, want %s for all", sessionImageDst, c.Src, c.Packager, sessionImageRecipe)
				}
			}
		}
		if !found {
			t.Errorf("the %v packages do not install %s", pkg.Formats, sessionImageDst)
		}
	}
}

// The how-to names the build command for the default image; the command and the
// constant the engine looks for must not drift apart.
func TestSessionImageDocsNameTheDefaultImage(t *testing.T) {
	doc := repoFile(t, "docs-site/src/content/docs/how-to/run-claude-code-with-olivares.md")
	for _, want := range []string{
		"build -f " + sessionImageDst + " -t " + DefaultContainerImage + " " + filepath.Dir(sessionImageDst),
		"build -f " + sessionImageRecipe + " -t " + DefaultContainerImage + " " + filepath.Dir(sessionImageRecipe),
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("the how-to does not show %q", want)
		}
	}
}
