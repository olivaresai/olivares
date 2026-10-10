// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/skills"
)

func TestSkillsCLILargeAdmittedManifestRemainsManageable(t *testing.T) {
	prepareDatalaneCLITest(t)
	var encoded bytes.Buffer
	z := zip.NewWriter(&encoded)
	for i := -1; i < 2000; i++ {
		name, body := "research/SKILL.md", "---\nname: research\ndescription: Review a fixture\n---\nReviewed instructions.\n"
		if i >= 0 {
			name = "research/" + strings.Repeat("a", 220) + "/" + strings.Repeat("b", 220) + fmt.Sprintf("/%04d.md", i)
			body = "retained support"
		}
		f, err := z.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.WriteString(f, body)
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	validated, err := skills.ImportArchive(t.Context(), bytes.NewReader(encoded.Bytes()), "zip", "")
	if err != nil {
		t.Fatal(err)
	}
	pack := skills.Pack{ID: model.NewID().String(), Name: "large", Version: 9}
	rev := skills.Revision{ID: model.NewID().String(), PackID: pack.ID, Manifest: validated.Manifest, ManifestDigest: validated.ManifestDigest, Members: validated.Members}
	installed, _ := json.Marshal(skills.InstallResult{State: "catalog_published", Pack: pack, Revision: rev})
	detail, _ := json.Marshal(skills.PackDetail{Pack: pack, Revisions: []skills.Revision{rev}})
	if len(installed) <= maxDatalaneResponseSize || len(detail) <= maxDatalaneResponseSize {
		t.Fatal("admitted fixture did not exceed the inherited response limit")
	}
	name := filepath.Join(t.TempDir(), "large.zip")
	if err := os.WriteFile(name, encoded.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	rec := newDatalaneRecorder(t, http.StatusOK, "")
	rec.respond = func(r datalaneRequest) (int, string) {
		switch {
		case r.Method == "GET":
			return http.StatusOK, string(detail)
		case r.Path == "/v1/m/skills/packs" && r.Method == "POST":
			return http.StatusCreated, string(installed)
		case r.Path == "/v1/m/skills/assignments" && r.Method == "POST":
			return http.StatusCreated, `{"state":"assignment_pinned"}`
		case r.Method == "DELETE" && r.Header.Get("If-Match") == "9":
			return http.StatusOK, `{"state":"catalog_retired"}`
		default:
			return http.StatusBadRequest, `{"error":"unexpected request"}`
		}
	}
	for _, args := range [][]string{
		{"skills", "install", "--archive", name},
		{"skills", "get", pack.ID},
		{"skills", "assign", pack.ID, "--workspace", model.NewID().String(), "--revision", rev.ID},
		{"skills", "rm", pack.ID},
	} {
		t.Run(args[1], func(t *testing.T) {
			if _, _, err := execDatalane(t, "", datalaneArgs(rec, args...)...); err != nil {
				t.Fatal(err)
			}
		})
	}
	rec.server.Close()
	if _, _, err := execDatalane(t, "", datalaneArgs(rec, "skills", "get", pack.ID)...); exitcode.From(err) != exitcode.Server {
		t.Fatalf("read transport failure changed its established exit code: %v", err)
	}
}
