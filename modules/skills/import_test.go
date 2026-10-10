// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package skills_test

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/modules/skills"
)

func archive(t *testing.T, names, contents []string) []byte {
	t.Helper()
	var b bytes.Buffer
	w := zip.NewWriter(&b)
	for i, name := range names {
		f, err := w.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write([]byte(contents[i])); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

const harmless = "---\nname: research\ndescription: Review primary sources.\nlicense: MIT\n---\nRead references/guide.md.\n"

func TestSkillsImportNeverExecutesSource(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "must-not-execute")
	data := archive(t, []string{"research/SKILL.md", "research/scripts/run.sh", "research/references/guide.md", "LICENSE"}, []string{harmless, "#!/bin/sh\nprintf executed > '" + marker + "'\n", "fixture marker", "MIT fixture"})
	pack, err := skills.ImportArchive(context.Background(), bytes.NewReader(data), "zip", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(pack.Members) != 1 || pack.Members[0].Name != "research" {
		t.Fatalf("members: %+v", pack.Members)
	}
	if len(pack.Manifest) != 4 || pack.ManifestDigest == "" || pack.SourceDigest == "" {
		t.Fatalf("manifest: %+v", pack)
	}
	body, ok := pack.File("research/SKILL.md")
	if !ok || !bytes.Equal(body, []byte(harmless)) {
		t.Fatal("original instructions were altered")
	}
	body[0] = 'X'
	unchanged, _ := pack.File("research/SKILL.md")
	if !bytes.Equal(unchanged, []byte(harmless)) {
		t.Fatal("caller mutated validated content")
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("import executed a source script")
	}
	if len(pack.Members[0].Scripts) != 1 {
		t.Fatal("script must be visible in preview")
	}
}

func TestSkillsArchiveBoundsAndTraversal(t *testing.T) {
	cases := []struct {
		name   string
		paths  []string
		bodies []string
		code   string
	}{
		{"traversal", []string{"../research/SKILL.md"}, []string{harmless}, "unsafe_pack"},
		{"absolute", []string{"/research/SKILL.md"}, []string{harmless}, "unsafe_pack"},
		{"backslash", []string{"research\\SKILL.md"}, []string{harmless}, "unsafe_pack"},
		{"dot alias", []string{"research/./SKILL.md"}, []string{harmless}, "unsafe_pack"},
		{"duplicate", []string{"research/SKILL.md", "research/SKILL.md"}, []string{harmless, harmless}, "unsafe_pack"},
		{"case alias", []string{"research/SKILL.md", "Research/support.md"}, []string{harmless, "text"}, "unsafe_pack"},
		{"parent replaced", []string{"research", "research/SKILL.md"}, []string{"text", harmless}, "unsafe_pack"},
		{"credential directory", []string{"research/SKILL.md", "research/.git/config"}, []string{harmless, "private"}, "unsafe_pack"},
		{"deep paths", []string{"research/" + strings.Repeat("a/", 16) + "guide.md", "research/SKILL.md"}, []string{"text", harmless}, "unsafe_pack"},
		{"oversize skill", []string{"research/SKILL.md"}, []string{harmless + strings.Repeat("x", skills.MaxSkillBytes)}, "import_limit"},
		{"oversize expanded file", []string{"research/SKILL.md", "research/bomb"}, []string{harmless, strings.Repeat("x", skills.MaxFileBytes+1)}, "import_limit"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p, err := skills.ImportArchive(context.Background(), bytes.NewReader(archive(t, c.paths, c.bodies)), "zip", "")
			var refusal *skills.ImportError
			if p != nil || !errors.As(err, &refusal) || refusal.Code != c.code {
				t.Fatalf("want no pack and %s; got %+v, %v", c.code, p, err)
			}
		})
	}
	for _, typ := range []byte{tar.TypeSymlink, tar.TypeLink, tar.TypeChar, tar.TypeBlock, tar.TypeFifo} {
		t.Run(fmt.Sprintf("tar-type-%d", typ), func(t *testing.T) {
			data := tarArchive(t, []*tar.Header{{Name: "research/SKILL.md", Mode: 0644, Size: int64(len(harmless)), Typeflag: tar.TypeReg}, {Name: "research/bad", Mode: 0644, Typeflag: typ, Linkname: "/etc/passwd"}}, []string{harmless, ""})
			if p, err := skills.ImportArchive(context.Background(), bytes.NewReader(data), "tar.gz", ""); p != nil || err == nil {
				t.Fatal("unsafe tar entry was accepted")
			}
		})
	}
	var z bytes.Buffer
	zw := zip.NewWriter(&z)
	h := &zip.FileHeader{Name: "research/SKILL.md"}
	h.SetMode(os.ModeSymlink | 0777)
	f, err := zw.CreateHeader(h)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.Write([]byte("/etc/passwd"))
	_ = zw.Close()
	if p, err := skills.ImportArchive(context.Background(), bytes.NewReader(z.Bytes()), "zip", ""); p != nil || err == nil {
		t.Fatal("zip symlink was accepted")
	}
}

func TestSkillsPortableFrontmatter(t *testing.T) {
	for _, body := range []string{
		"name: Research\ndescription: text", "name: research\ndescription: ''", "name: research\nname: research\ndescription: text",
		"name: research\ndescription: &a text\nmetadata: {author: *a}", "name: research\ndescription: !custom text",
		"name: research\ndescription: text\nmetadata: {version: 1}",
		"name: wrong-folder\ndescription: text", "name: research\ndescription: " + strings.Repeat("x", 1025),
	} {
		t.Run(body[:min(35, len(body))], func(t *testing.T) {
			data := archive(t, []string{"research/SKILL.md"}, []string{"---\n" + body + "\n---\ntext"})
			if p, err := skills.ImportArchive(context.Background(), bytes.NewReader(data), "zip", ""); p != nil || err == nil {
				t.Fatal("invalid YAML was accepted")
			}
		})
	}
	data := archive(t, []string{"research/SKILL.md", "research/agents/openai.yaml", "research/.claude-plugin/plugin.json"}, []string{strings.Replace(harmless, "license: MIT", "license: MIT\nallowed-tools: Bash\nmodel: powerful", 1), "dependencies:\n  tools: []\n", "{}"})
	p, err := skills.ImportArchive(context.Background(), bytes.NewReader(data), "zip", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Members[0].Extensions) != 4 {
		t.Fatalf("unsupported declarations disappeared: %+v", p.Members[0])
	}
}

func tarArchive(t *testing.T, headers []*tar.Header, bodies []string) []byte {
	t.Helper()
	var b bytes.Buffer
	gz := gzip.NewWriter(&b)
	tw := tar.NewWriter(gz)
	for i, h := range headers {
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(bodies[i])); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestSkillsManifestIsIndependentOfArchiveOrder(t *testing.T) {
	first, err := skills.ImportArchive(context.Background(), bytes.NewReader(archive(t, []string{"research/SKILL.md", "LICENSE"}, []string{harmless, "MIT"})), "zip", "")
	if err != nil {
		t.Fatal(err)
	}
	second, err := skills.ImportArchive(context.Background(), bytes.NewReader(tarArchive(t, []*tar.Header{{Name: "LICENSE", Mode: 0644, Size: 3}, {Name: "research/SKILL.md", Mode: 0644, Size: int64(len(harmless))}}, []string{"MIT", harmless})), "tar.gz", "")
	if err != nil {
		t.Fatal(err)
	}
	if first.ManifestDigest != second.ManifestDigest {
		t.Fatal("equivalent ZIP and tar content have different identity")
	}
	if first.SourceDigest == second.SourceDigest {
		t.Fatal("original source artifacts were not distinguished")
	}
	if p, err := skills.ImportArchive(context.Background(), bytes.NewReader(archive(t, []string{"research/SKILL.md"}, []string{harmless})), "zip", strings.Repeat("0", 64)); p != nil || err == nil {
		t.Fatal("expected digest mismatch was ignored")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if p, err := skills.ImportArchive(ctx, bytes.NewReader(nil), "zip", ""); p != nil || !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation was ignored")
	}
}

func TestSkillsPackWrapperPreservesBytesAndCanonicalManifest(t *testing.T) {
	one, err := skills.ImportArchive(context.Background(), bytes.NewReader(archive(t, []string{"research/SKILL.md", "LICENSE"}, []string{harmless, "MIT"})), "zip", "")
	if err != nil {
		t.Fatal(err)
	}
	wrapped, err := skills.ImportArchive(context.Background(), bytes.NewReader(archive(t, []string{"engineering/research/SKILL.md", "engineering/LICENSE"}, []string{harmless, "MIT"})), "zip", "")
	if err != nil {
		t.Fatal(err)
	}
	if one.ManifestDigest != wrapped.ManifestDigest {
		t.Fatal("pack wrapper changed selected content identity")
	}
	mixed := archive(t, []string{"engineering/research/SKILL.md", "another/auth.txt"}, []string{harmless, "private fixture"})
	if p, err := skills.ImportArchive(context.Background(), bytes.NewReader(mixed), "zip", ""); p != nil || err == nil {
		t.Fatal("unselected repository material was imported")
	}
}

func TestSkillsMemberDigestIncludesSupportingFiles(t *testing.T) {
	first, err := skills.ImportArchive(context.Background(), bytes.NewReader(archive(t, []string{"research/SKILL.md", "research/support.md"}, []string{harmless, "support one"})), "zip", "")
	if err != nil {
		t.Fatal(err)
	}
	second, err := skills.ImportArchive(context.Background(), bytes.NewReader(archive(t, []string{"research/support.md", "research/SKILL.md"}, []string{"support two", harmless})), "zip", "")
	if err != nil {
		t.Fatal(err)
	}
	if first.Members[0].SkillDigest != second.Members[0].SkillDigest || first.Members[0].ContentDigest == "" || first.Members[0].ContentDigest == second.Members[0].ContentDigest {
		t.Fatal("member identity ignores supporting files")
	}
}
