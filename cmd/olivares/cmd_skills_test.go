// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/skills"
)

func TestSkillsCLIImportDefaultsAndRefusal(t *testing.T) {
	prepareDatalaneCLITest(t)
	rec := newDatalaneRecorder(t, http.StatusCreated, `{"state":"catalog_published"}`)
	if _, _, err := execDatalane(t, "", datalaneArgs(rec, "skills", "install", "--git", "https://example.test/team/review.git")...); err != nil {
		t.Fatal(err)
	}
	var gitBody struct {
		Name   string `json:"name"`
		Source struct {
			Ref string `json:"ref"`
		} `json:"source"`
	}
	if err := json.Unmarshal(rec.last(t).Body, &gitBody); err != nil || gitBody.Name != "review" || gitBody.Source.Ref != "HEAD" {
		t.Fatalf("source defaults: %s", rec.last(t).Body)
	}
	folder := filepath.Join(t.TempDir(), "research")
	if err := os.Mkdir(folder, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(folder, "SKILL.md"), []byte("---\nname: research\ndescription: Review a fixture\n---\nKeep the source unchanged.\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := execDatalane(t, "", datalaneArgs(rec, "skills", "install", "--folder", folder)...); err != nil {
		t.Fatal(err)
	}
	request := rec.last(t)
	_, params, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil {
		t.Fatal(err)
	}
	reader := multipart.NewReader(bytes.NewReader(request.Body), params["boundary"])
	form, err := reader.ReadForm(skills.MaxSourceBytes)
	if err != nil {
		t.Fatal(err)
	}
	defer form.RemoveAll()
	if form.Value["name"][0] != "research" {
		t.Fatalf("folder name: %v", form.Value)
	}
	file, err := form.File["archive"][0].Open()
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	archive, err := io.ReadAll(file)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := skills.ImportArchive(t.Context(), bytes.NewReader(archive), "zip", form.Value["expected_digest"][0]); err != nil {
		t.Fatal(err)
	}
	before := rec.count()
	_, _, err = execDatalane(t, "", datalaneArgs(rec, "skills", "install", "--git", "https://example.test/review.git", "--folder", folder)...)
	if exitcode.From(err) != exitcode.Usage || rec.count() != before {
		t.Fatalf("ambiguous input: %v; requests %d -> %d", err, before, rec.count())
	}
}

func TestSkillsCLIRemovalReadsCurrentVersion(t *testing.T) {
	prepareDatalaneCLITest(t)
	id := model.NewID().String()
	rec := newDatalaneRecorder(t, http.StatusOK, "")
	rec.respond = func(r datalaneRequest) (int, string) {
		if r.Method == "GET" {
			return http.StatusOK, `{"pack":{"version":9},"revisions":[]}`
		}
		if r.Method != "DELETE" || r.Path != "/v1/m/skills/packs/"+id || r.Header.Get("If-Match") != "9" {
			t.Errorf("retirement request: %s %s If-Match=%s", r.Method, r.Path, r.Header.Get("If-Match"))
		}
		return http.StatusOK, `{"state":"catalog_retired"}`
	}
	if _, _, err := execDatalane(t, "", datalaneArgs(rec, "skills", "rm", id)...); err != nil {
		t.Fatal(err)
	}
	if rec.count() != 2 {
		t.Fatalf("removal requests = %d", rec.count())
	}
}

func TestSkillsCLIAssignsAndListsByDirectoryTargetAndPack(t *testing.T) {
	prepareDatalaneCLITest(t)
	pack, revision, group, agent := model.NewID().String(), model.NewID().String(), model.NewID().String(), model.NewID().String()
	rec := newDatalaneRecorder(t, http.StatusOK, "")
	rec.respond = func(r datalaneRequest) (int, string) {
		switch {
		case r.Method == "GET" && r.Path == "/v1/m/skills/packs/"+pack:
			return http.StatusOK, `{"pack":{"id":"` + pack + `","version":1},"revisions":[{"id":"` + revision + `"}]}`
		case r.Method == "POST":
			return http.StatusCreated, `{"state":"pinned"}`
		}
		return http.StatusOK, `{"items":[{"id":"a1","target_kind":"agent_group","target_id":"` + group + `","pack_id":"` + pack + `","pack_revision_id":"` + revision + `","version":1}],"has_more":false}`
	}
	if _, _, err := execDatalane(t, "", datalaneArgs(rec, "skills", "assign", pack, "--revision", revision, "--group", group)...); err != nil {
		t.Fatal(err)
	}
	if body := rec.jsonBody(t); body["target_kind"] != "agent_group" || body["target_id"] != group || body["pack_revision_id"] != revision {
		t.Fatalf("group assignment body: %v", body)
	}
	if _, _, err := execDatalane(t, "", datalaneArgs(rec, "skills", "assignments", "--agent", agent)...); err != nil {
		t.Fatal(err)
	}
	if q := rec.last(t).Query; rec.last(t).Path != "/v1/m/skills/assignments" || q.Get("target_kind") != "agent" || q.Get("target_id") != agent {
		t.Fatalf("agent listing query: %v", q)
	}
	out, _, err := execDatalane(t, "", datalaneArgs(rec, "skills", "assignments", "--pack", pack)...)
	if err != nil {
		t.Fatal(err)
	}
	if q := rec.last(t).Query; rec.last(t).Path != "/v1/m/skills/packs/"+pack+"/assignments" || q.Has("target_kind") || q.Has("target_id") {
		t.Fatalf("pack listing: %s %v", rec.last(t).Path, q)
	}
	if !strings.Contains(out, "agent_group") || !strings.Contains(out, group) {
		t.Fatalf("pack listing output names no target:\n%s", out)
	}
	before := rec.count()
	for _, args := range [][]string{
		{"skills", "assignments", "--pack", pack, "--workspace", group},
		{"skills", "assignments"},
		{"skills", "assignments", "--group", group, "--agent", agent},
		{"skills", "assign", pack, "--revision", revision},
	} {
		if _, _, err := execDatalane(t, "", datalaneArgs(rec, args...)...); exitcode.From(err) != exitcode.Usage {
			t.Fatalf("%v: %v", args, err)
		}
	}
	if rec.count() != before {
		t.Fatalf("refused commands sent %d requests", rec.count()-before)
	}
}
