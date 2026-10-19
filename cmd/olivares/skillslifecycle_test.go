// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

const (
	lifePack     = "01a1320c-0000-7000-8000-000000000001"
	lifeOther    = "01a1320c-0000-7000-8000-000000000002"
	lifeRevOld   = "01a1320c-0000-7000-8000-0000000000a1"
	lifeRevNew   = "01a1320c-0000-7000-8000-0000000000a2"
	lifeRevOther = "01a1320c-0000-7000-8000-0000000000b1"
	lifeFolder   = "01a1320c-0000-7000-8000-0000000000f1"
	lifeWS       = "01a1320c-0000-7000-8000-0000000000c1"
)

// lifecycleServer answers the skills routes the lifecycle verbs read, from fixed
// state: two packs, the first at its second revision, and the assignments each
// test installs per target ("kind:id").
type lifecycleServer struct {
	assignments map[string]string
	t           *testing.T
}

func (s lifecycleServer) respond(r datalaneRequest) (int, string) {
	switch {
	case r.Method == "GET" && r.Path == "/v1/m/skills/packs":
		return 200, `{"items":[
			{"id":"` + lifePack + `","name":"ecc-workflow","state":"enabled","latest_revision_id":"` + lifeRevNew + `","latest_revision":2,"version":2},
			{"id":"` + lifeOther + `","name":"team-skills","state":"enabled","latest_revision_id":"` + lifeRevOther + `","latest_revision":1,"version":1}],"has_more":false}`
	case r.Method == "GET" && r.Path == "/v1/m/skills/packs/"+lifePack:
		return 200, `{"pack":{"id":"` + lifePack + `","name":"ecc-workflow","state":"enabled","latest_revision_id":"` + lifeRevNew + `","latest_revision":2,"version":2},
			"revisions":[
			 {"id":"` + lifeRevOld + `","pack_id":"` + lifePack + `","number":1,"members":[{"name":"tdd"},{"name":"review"}]},
			 {"id":"` + lifeRevNew + `","pack_id":"` + lifePack + `","number":2,"members":[{"name":"tdd"},{"name":"review"},{"name":"plan"}]}],
			"has_more_revisions":false}`
	case r.Method == "GET" && r.Path == "/v1/m/skills/packs/"+lifeOther:
		return 200, `{"pack":{"id":"` + lifeOther + `","name":"team-skills","state":"enabled","latest_revision_id":"` + lifeRevOther + `","latest_revision":1,"version":1},
			"revisions":[{"id":"` + lifeRevOther + `","pack_id":"` + lifeOther + `","number":1,"members":[{"name":"deploy","active_content":["allowed-tools"]},{"name":"review"}]}],
			"has_more_revisions":false}`
	case r.Method == "GET" && r.Path == "/v1/m/skills/assignments":
		items := s.assignments[r.Query.Get("target_kind")+":"+r.Query.Get("target_id")]
		if items == "" {
			items = "[]"
		}
		return 200, `{"items":` + items + `,"has_more":false}`
	case r.Method == "GET" && r.Path == "/v1/m/skills/packs/"+lifePack+"/assignments":
		return 200, `{"items":[
			{"id":"as-1","target_kind":"folder","target_id":"` + lifeFolder + `","pack_id":"` + lifePack + `","pack_revision_id":"` + lifeRevOld + `","members":["tdd","review"],"version":3},
			{"id":"as-2","target_kind":"workspace","target_id":"` + lifeWS + `","pack_id":"` + lifePack + `","pack_revision_id":"` + lifeRevNew + `","members":["tdd"],"version":1}],"has_more":false}`
	case r.Method == "PUT" || r.Method == "DELETE" || r.Method == "POST":
		return 200, `{"state":"ok"}`
	}
	s.t.Errorf("unexpected request %s %s", r.Method, r.Path)
	return 500, `{}`
}

func lifecycleRecorder(t *testing.T, assignments map[string]string) *datalaneRecorder {
	t.Helper()
	prepareDatalaneCLITest(t)
	rec := newDatalaneRecorder(t, 200, "")
	rec.respond = lifecycleServer{assignments: assignments, t: t}.respond
	return rec
}

func (r *datalaneRecorder) sent(method string) []datalaneRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []datalaneRequest
	for _, req := range r.requests {
		if req.Method == method {
			out = append(out, req)
		}
	}
	return out
}

func TestSkillsCLIUpdateChecksTheRecordedSourceAndPrintsWhatToApply(t *testing.T) {
	prepareDatalaneCLITest(t)
	rec := newDatalaneRecorder(t, 200, `{"state":"available","pack":{"id":"`+lifePack+`","name":"ecc-workflow"},
		"current":{"revision_id":"`+lifeRevOld+`","number":1,"resolved_commit":"aaaaaaa","source_digest":"d1"},
		"source":{"kind":"git","origin":"https://example.test/r.git","requested_ref":"HEAD","subdir":"skills"},
		"candidate":{"source_digest":"packbytes","manifest_digest":"cafe00","resolved_commit":"bbbbbbb"},
		"diff":{"members":[{"name":"plan","change":"added","new_active_content":["allowed-tools"],"active_content":["allowed-tools"]}],
		        "files":[{"path":"plan/SKILL.md","change":"added","diff":"--- /dev/null\n+++ b/plan/SKILL.md\n@@ -0,0 +1,1 @@\n+hello\n"}]}}`)
	out, _, err := execDatalane(t, "", datalaneArgs(rec, "skills", "update", lifePack)...)
	if err != nil {
		t.Fatal(err)
	}
	req := rec.last(t)
	var body map[string]any
	if req.Method != "POST" || req.Path != "/v1/m/skills/packs/"+lifePack+"/update" || json.Unmarshal(req.Body, &body) != nil || body["apply"] == true {
		t.Fatalf("check request: %s %s %s", req.Method, req.Path, req.Body)
	}
	for _, want := range []string{"update available", "directory skills", "digest: cafe00", "plan/SKILL.md", "allowed-tools", "--apply --expected-digest cafe00"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "+hello") {
		t.Fatalf("text diff printed without --diff:\n%s", out)
	}
	full, _, err := execDatalane(t, "", datalaneArgs(rec, "skills", "update", lifePack, "--diff")...)
	if err != nil || !strings.Contains(full, "+hello") {
		t.Fatalf("--diff: %v\n%s", err, full)
	}
}

func TestSkillsCLIUpdateAppliesOnlyAReviewedDigest(t *testing.T) {
	prepareDatalaneCLITest(t)
	rec := newDatalaneRecorder(t, 201, `{"state":"catalog_published","revision":{"number":2}}`)
	before := rec.count()
	if _, _, err := execDatalane(t, "", datalaneArgs(rec, "skills", "update", lifePack, "--apply")...); exitcode.From(err) != exitcode.Usage || rec.count() != before {
		t.Fatalf("apply without a reviewed digest: %v, requests %d -> %d", err, before, rec.count())
	}
	if _, _, err := execDatalane(t, "", datalaneArgs(rec, "skills", "update", lifePack, "--apply", "--expected-digest", "cafe00", "--idempotency-key", "up-1")...); err != nil {
		t.Fatal(err)
	}
	req := rec.last(t)
	body := rec.jsonBody(t)
	if req.Path != "/v1/m/skills/packs/"+lifePack+"/update" || body["apply"] != true || body["expected_digest"] != "cafe00" || req.Header.Get("Idempotency-Key") != "up-1" {
		t.Fatalf("apply request: %s %v %v", req.Path, body, req.Header)
	}
	// A source flag still publishes the named source, as before.
	if _, _, err := execDatalane(t, "", datalaneArgs(rec, "skills", "update", lifePack, "--builtin", "ecc-security")...); err != nil {
		t.Fatal(err)
	}
	if got := rec.last(t).Path; got != "/v1/m/skills/packs/"+lifePack+"/revisions" {
		t.Fatalf("explicit source update went to %s", got)
	}
}

func TestSkillsCLIDiffComparesTwoRevisions(t *testing.T) {
	prepareDatalaneCLITest(t)
	rec := newDatalaneRecorder(t, 200, `{"pack_id":"`+lifePack+`","from":{"number":1},"to":{"number":2},
		"members":[{"name":"plan","change":"added"}],
		"files":[{"path":"plan/SKILL.md","change":"added","new_size":5,"diff":"@@ x\n+hello\n"}]}`)
	out, _, err := execDatalane(t, "", datalaneArgs(rec, "skills", "diff", lifePack, "--from", lifeRevOld, "--to", lifeRevNew)...)
	if err != nil {
		t.Fatal(err)
	}
	req := rec.last(t)
	if req.Method != "GET" || req.Path != "/v1/m/skills/packs/"+lifePack+"/diff" || req.Query.Get("from") != lifeRevOld || req.Query.Get("to") != lifeRevNew {
		t.Fatalf("diff request: %s %s %v", req.Method, req.Path, req.Query)
	}
	if !strings.Contains(out, "plan") || !strings.Contains(out, "plan/SKILL.md") {
		t.Fatalf("diff output:\n%s", out)
	}
}

func TestSkillsCLIPinMovesEveryPinOfAPackToOneRevision(t *testing.T) {
	rec := lifecycleRecorder(t, nil)
	out, _, err := execDatalane(t, "", datalaneArgs(rec, "skills", "pin", lifePack)...)
	if err != nil {
		t.Fatal(err)
	}
	puts := rec.sent("PUT")
	if len(puts) != 1 || puts[0].Path != "/v1/m/skills/assignments/as-1" || puts[0].Header.Get("If-Match") != "3" {
		t.Fatalf("pins moved: %+v", puts)
	}
	var body struct {
		Kind    string   `json:"target_kind"`
		ID      string   `json:"target_id"`
		Rev     string   `json:"pack_revision_id"`
		Members []string `json:"members"`
	}
	if err := json.Unmarshal(puts[0].Body, &body); err != nil || body.Rev != lifeRevNew || body.Kind != "folder" || body.ID != lifeFolder || strings.Join(body.Members, ",") != "tdd,review" {
		t.Fatalf("pin body: %s", puts[0].Body)
	}
	if !strings.Contains(out, "as-1") {
		t.Fatalf("output does not say what moved:\n%s", out)
	}

	// Rolling back is the same verb with an older revision; --dry-run writes nothing.
	rec = lifecycleRecorder(t, nil)
	if _, _, err := execDatalane(t, "", datalaneArgs(rec, "skills", "pin", lifePack, "--revision", lifeRevOld, "--dry-run")...); err != nil {
		t.Fatal(err)
	}
	if len(rec.sent("PUT")) != 0 {
		t.Fatal("--dry-run wrote")
	}
	rec = lifecycleRecorder(t, nil)
	if _, _, err := execDatalane(t, "", datalaneArgs(rec, "skills", "pin", lifePack, "--revision", lifeRevOld)...); err != nil {
		t.Fatal(err)
	}
	puts = rec.sent("PUT")
	if len(puts) != 1 || puts[0].Path != "/v1/m/skills/assignments/as-2" {
		t.Fatalf("rollback moved: %+v", puts)
	}
}

func TestSkillsCLISelectionNamesTheTreeAndWhereEachSkillComesFrom(t *testing.T) {
	prepareDatalaneCLITest(t)
	rec := newDatalaneRecorder(t, 200, `{"digest":"d","members":[
		{"pack_id":"`+lifePack+`","pack_revision_id":"`+lifeRevNew+`","name":"tdd","directory":"tdd","origins":[{"target_kind":"workspace","target_id":"`+lifeWS+`"},{"target_kind":"folder","target_id":"`+lifeFolder+`"}]}]}`)
	out, _, err := execDatalane(t, "", datalaneArgs(rec, "skills", "selection", "--folder", lifeFolder, "--workspace", lifeWS)...)
	if err != nil {
		t.Fatal(err)
	}
	var req datalaneRequest
	for _, r := range rec.sent("GET") {
		if r.Path == "/v1/m/skills/selection" {
			req = r
		}
	}
	got := strings.Join(req.Query["target"], " ")
	if req.Path == "" || !strings.Contains(got, "folder:"+lifeFolder) || !strings.Contains(got, "workspace:"+lifeWS) {
		t.Fatalf("selection request: %+v", req)
	}
	if !strings.Contains(out, "tdd") || !strings.Contains(out, "folder") {
		t.Fatalf("selection output:\n%s", out)
	}
	before := rec.count()
	if _, _, err := execDatalane(t, "", datalaneArgs(rec, "skills", "selection")...); exitcode.From(err) != exitcode.Usage || rec.count() != before {
		t.Fatalf("no target: %v", err)
	}
}

func TestSkillsCLIEnableResolvesTheSkillToItsPackAndPinsOnlyThat(t *testing.T) {
	rec := lifecycleRecorder(t, nil)
	if _, _, err := execDatalane(t, "", datalaneArgs(rec, "skills", "enable", "tdd", "plan", "--folder", lifeFolder)...); err != nil {
		t.Fatal(err)
	}
	posts := rec.sent("POST")
	if len(posts) != 1 || posts[0].Path != "/v1/m/skills/assignments" {
		t.Fatalf("assignments created: %+v", posts)
	}
	var body struct {
		Kind    string   `json:"target_kind"`
		ID      string   `json:"target_id"`
		Rev     string   `json:"pack_revision_id"`
		Members []string `json:"members"`
		Approve bool     `json:"approve_active_content"`
	}
	if err := json.Unmarshal(posts[0].Body, &body); err != nil || body.Kind != "folder" || body.ID != lifeFolder || body.Rev != lifeRevNew || strings.Join(body.Members, ",") != "plan,tdd" || body.Approve {
		t.Fatalf("pin: %s", posts[0].Body)
	}

	// The same name in two packs is the caller's to decide, and nothing is written.
	rec = lifecycleRecorder(t, nil)
	_, _, err := execDatalane(t, "", datalaneArgs(rec, "skills", "enable", "review", "--folder", lifeFolder)...)
	if exitcode.From(err) != exitcode.Usage || !strings.Contains(err.Error(), "--pack") || len(rec.sent("POST"))+len(rec.sent("PUT")) != 0 {
		t.Fatalf("ambiguous skill: %v", err)
	}
	// --pack settles it.
	if _, _, err := execDatalane(t, "", datalaneArgs(rec, "skills", "enable", "review", "--pack", lifeOther, "--folder", lifeFolder, "--approve-active-content")...); err != nil {
		t.Fatal(err)
	}
	var approved map[string]any
	if json.Unmarshal(rec.sent("POST")[0].Body, &approved) != nil || approved["pack_revision_id"] != lifeRevOther || approved["approve_active_content"] != true {
		t.Fatalf("pack choice: %s", rec.sent("POST")[0].Body)
	}
	// An unknown skill is not found.
	_, _, err = execDatalane(t, "", datalaneArgs(rec, "skills", "enable", "nonexistent", "--folder", lifeFolder)...)
	if exitcode.From(err) != exitcode.NotFound {
		t.Fatalf("unknown skill: %v", err)
	}
}

func TestSkillsCLIEnableExtendsAnExistingPinWithoutMovingItsRevision(t *testing.T) {
	existing := `[{"id":"as-9","target_kind":"folder","target_id":"` + lifeFolder + `","pack_id":"` + lifePack + `","pack_revision_id":"` + lifeRevNew + `","members":["tdd"],"version":4}]`
	rec := lifecycleRecorder(t, map[string]string{"folder:" + lifeFolder: existing})
	if _, _, err := execDatalane(t, "", datalaneArgs(rec, "skills", "enable", "plan", "--folder", lifeFolder)...); err != nil {
		t.Fatal(err)
	}
	puts := rec.sent("PUT")
	if len(puts) != 1 || puts[0].Path != "/v1/m/skills/assignments/as-9" || puts[0].Header.Get("If-Match") != "4" {
		t.Fatalf("extend: %+v", puts)
	}
	var body struct {
		Rev     string   `json:"pack_revision_id"`
		Members []string `json:"members"`
	}
	if err := json.Unmarshal(puts[0].Body, &body); err != nil || body.Rev != lifeRevNew || strings.Join(body.Members, ",") != "plan,tdd" {
		t.Fatalf("extend body: %s", puts[0].Body)
	}
	if len(rec.sent("POST")) != 0 {
		t.Fatal("a second assignment was created for the same pack and target")
	}
}

func TestSkillsCLIDisableRemovesTheSkillFromThePinAndDropsAnEmptyPin(t *testing.T) {
	pins := `[{"id":"as-1","target_kind":"folder","target_id":"` + lifeFolder + `","pack_id":"` + lifePack + `","pack_revision_id":"` + lifeRevNew + `","members":["tdd","plan"],"version":2},
	          {"id":"as-2","target_kind":"folder","target_id":"` + lifeFolder + `","pack_id":"` + lifeOther + `","pack_revision_id":"` + lifeRevOther + `","members":["deploy"],"version":5}]`
	rec := lifecycleRecorder(t, map[string]string{"folder:" + lifeFolder: pins})
	if _, _, err := execDatalane(t, "", datalaneArgs(rec, "skills", "disable", "plan", "deploy", "--folder", lifeFolder)...); err != nil {
		t.Fatal(err)
	}
	puts, dels := rec.sent("PUT"), rec.sent("DELETE")
	if len(puts) != 1 || puts[0].Path != "/v1/m/skills/assignments/as-1" || puts[0].Header.Get("If-Match") != "2" {
		t.Fatalf("narrowed pin: %+v", puts)
	}
	var body struct {
		Members []string `json:"members"`
	}
	if err := json.Unmarshal(puts[0].Body, &body); err != nil || len(body.Members) != 1 || body.Members[0] != "tdd" {
		t.Fatalf("remaining members: %s", puts[0].Body)
	}
	if len(dels) != 1 || dels[0].Path != "/v1/m/skills/assignments/as-2" || dels[0].Header.Get("If-Match") != "5" {
		t.Fatalf("emptied pin: %+v", dels)
	}
	// A skill the target does not pin is named, and nothing is written.
	rec = lifecycleRecorder(t, map[string]string{"folder:" + lifeFolder: pins})
	_, _, err := execDatalane(t, "", datalaneArgs(rec, "skills", "disable", "ghost", "--folder", lifeFolder)...)
	if exitcode.From(err) != exitcode.NotFound || len(rec.sent("PUT"))+len(rec.sent("DELETE")) != 0 {
		t.Fatalf("skill not on the target: %v", err)
	}
}

func TestSkillsCLIUpdatePrintsAnApplyCommandThatCarriesTheRefAndSubdirItWasChecked(t *testing.T) {
	prepareDatalaneCLITest(t)
	rec := newDatalaneRecorder(t, 200, `{"state":"available","pack":{"id":"`+lifePack+`","name":"ecc-workflow"},
		"source":{"kind":"git","origin":"https://example.test/r.git","requested_ref":"v2","subdir":"skills"},
		"current":{"revision_id":"`+lifeRevOld+`","number":1},
		"candidate":{"manifest_digest":"cafe00"},"diff":{"members":[],"files":[]}}`)
	out, _, err := execDatalane(t, "", datalaneArgs(rec, "skills", "update", lifePack, "--ref", "v2", "--subdir", "skills")...)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "olivares skills update "+lifePack+" --ref v2 --subdir skills --apply --expected-digest cafe00") {
		t.Fatalf("the printed command would apply a different source than it checked:\n%s", out)
	}
	rec.status, rec.body = 200, `{"state":"surprise","pack":{},"source":{},"current":{}}`
	if _, _, err := execDatalane(t, "", datalaneArgs(rec, "skills", "update", lifePack)...); exitcode.From(err) != exitcode.Server {
		t.Fatalf("an unknown state was reported as current: %v", err)
	}
}

func TestSkillsCLIDisableWritesNothingWhenOneNameIsNotPinned(t *testing.T) {
	pins := `[{"id":"as-1","target_kind":"folder","target_id":"` + lifeFolder + `","pack_id":"` + lifePack + `","pack_revision_id":"` + lifeRevNew + `","members":["tdd","plan"],"version":2}]`
	rec := lifecycleRecorder(t, map[string]string{"folder:" + lifeFolder: pins})
	_, _, err := execDatalane(t, "", datalaneArgs(rec, "skills", "disable", "plan", "typo", "--folder", lifeFolder)...)
	if exitcode.From(err) != exitcode.NotFound || len(rec.sent("PUT"))+len(rec.sent("DELETE")) != 0 {
		t.Fatalf("a typo disabled the other skill: %v, writes %d", err, len(rec.sent("PUT"))+len(rec.sent("DELETE")))
	}
}

func TestSkillsCLIEnableWritesNothingWhenALaterPackNeedsApproval(t *testing.T) {
	rec := lifecycleRecorder(t, nil)
	_, _, err := execDatalane(t, "", datalaneArgs(rec, "skills", "enable", "plan", "deploy", "--folder", lifeFolder)...)
	if exitcode.From(err) != exitcode.Usage || !strings.Contains(err.Error(), "approve-active-content") {
		t.Fatalf("an unapproved skill: %v", err)
	}
	if len(rec.sent("POST"))+len(rec.sent("PUT")) != 0 {
		t.Fatalf("the first pack was written before the second was refused: %d POST, %d PUT", len(rec.sent("POST")), len(rec.sent("PUT")))
	}
}

func TestSkillsCLIEnableKeepsTheRevisionAnExistingPinHolds(t *testing.T) {
	existing := `[{"id":"as-9","target_kind":"folder","target_id":"` + lifeFolder + `","pack_id":"` + lifePack + `","pack_revision_id":"` + lifeRevOld + `","members":["tdd"],"version":4}]`
	rec := lifecycleRecorder(t, map[string]string{"folder:" + lifeFolder: existing})
	if _, _, err := execDatalane(t, "", datalaneArgs(rec, "skills", "enable", "review", "--pack", lifePack, "--folder", lifeFolder)...); err != nil {
		t.Fatal(err)
	}
	var body struct {
		Rev     string   `json:"pack_revision_id"`
		Members []string `json:"members"`
	}
	puts := rec.sent("PUT")
	if len(puts) != 1 || json.Unmarshal(puts[0].Body, &body) != nil || body.Rev != lifeRevOld || strings.Join(body.Members, ",") != "review,tdd" {
		t.Fatalf("extending a pin on revision 1 moved it: %+v", puts)
	}
	// A skill only the newer revision has is not smuggled in by moving the pin.
	_, _, err := execDatalane(t, "", datalaneArgs(rec, "skills", "enable", "plan", "--pack", lifePack, "--folder", lifeFolder)...)
	if exitcode.From(err) != exitcode.Usage || !strings.Contains(err.Error(), "skills pin") {
		t.Fatalf("a skill missing from the pinned revision: %v", err)
	}
}

func TestSkillsCLIPinReportsWhatCannotMoveAndDryRunAgrees(t *testing.T) {
	prepareDatalaneCLITest(t)
	rec := newDatalaneRecorder(t, 200, "")
	server := lifecycleServer{t: t}
	rec.respond = func(r datalaneRequest) (int, string) {
		if r.Method == "GET" && r.Path == "/v1/m/skills/packs/"+lifePack+"/assignments" {
			return 200, `{"items":[
				{"id":"as-5","target_kind":"folder","target_id":"` + lifeFolder + `","pack_id":"` + lifePack + `","pack_revision_id":"` + lifeRevNew + `","members":["plan"],"version":2},
				{"id":"as-6","target_kind":"workspace","target_id":"` + lifeWS + `","pack_id":"` + lifePack + `","pack_revision_id":"` + lifeRevNew + `","members":["tdd"],"version":1}],"has_more":false}`
		}
		return server.respond(r)
	}
	for _, dry := range []bool{true, false} {
		args := []string{"skills", "pin", lifePack, "--revision", lifeRevOld}
		if dry {
			args = append(args, "--dry-run")
		}
		out, _, err := execDatalane(t, "", datalaneArgs(rec, args...)...)
		if exitcode.From(err) != exitcode.Degraded || !strings.Contains(out, "failed") || !strings.Contains(out, `skill "plan" is not in revision 1`) {
			t.Fatalf("dry-run=%v: %v\n%s", dry, err, out)
		}
	}
	puts := rec.sent("PUT")
	if len(puts) != 1 || puts[0].Path != "/v1/m/skills/assignments/as-6" {
		t.Fatalf("only the pin that can move moved, and only in the real run: %+v", puts)
	}
}

func TestSkillsCLIDisableWithAPackChangesOnlyThatPacksPin(t *testing.T) {
	pins := `[{"id":"as-1","target_kind":"folder","target_id":"` + lifeFolder + `","pack_id":"` + lifePack + `","pack_revision_id":"` + lifeRevNew + `","members":["tdd","review"],"version":2},
	          {"id":"as-2","target_kind":"folder","target_id":"` + lifeFolder + `","pack_id":"` + lifeOther + `","pack_revision_id":"` + lifeRevOther + `","members":["review"],"version":5}]`
	rec := lifecycleRecorder(t, map[string]string{"folder:" + lifeFolder: pins})
	if _, _, err := execDatalane(t, "", datalaneArgs(rec, "skills", "disable", "review", "--pack", lifeOther, "--folder", lifeFolder)...); err != nil {
		t.Fatal(err)
	}
	dels, puts := rec.sent("DELETE"), rec.sent("PUT")
	if len(puts) != 0 || len(dels) != 1 || dels[0].Path != "/v1/m/skills/assignments/as-2" {
		t.Fatalf("the other pack's pin changed: PUT %+v DELETE %+v", puts, dels)
	}
	// A name the chosen pack does not pin is not found, even if another pack pins it.
	_, _, err := execDatalane(t, "", datalaneArgs(rec, "skills", "disable", "tdd", "--pack", lifeOther, "--folder", lifeFolder)...)
	if exitcode.From(err) != exitcode.NotFound {
		t.Fatalf("a skill of another pack: %v", err)
	}
}

func TestSkillsCLIProfileTargetAcrossAssignmentAndLifecycleVerbs(t *testing.T) {
	profile := "ppf_" + lifeFolder
	t.Run("assign", func(t *testing.T) {
		rec := lifecycleRecorder(t, nil)
		if _, _, err := execDatalane(t, "", datalaneArgs(rec, "skills", "assign", lifePack, "--revision", lifeRevNew, "--profile", profile)...); err != nil {
			t.Fatal(err)
		}
		if body := rec.jsonBody(t); body["target_kind"] != "profile" || body["target_id"] != profile {
			t.Fatalf("profile body: %v", body)
		}
	})
	for _, verb := range []string{"assignments", "enable", "disable", "selection"} {
		t.Run(verb, func(t *testing.T) {
			pins := `[{"id":"as-profile","target_kind":"profile","target_id":"` + profile + `","pack_id":"` + lifePack + `","pack_revision_id":"` + lifeRevOld + `","members":["tdd","review"],"version":3}]`
			if verb == "enable" {
				pins = strings.Replace(pins, `["tdd","review"]`, `["tdd"]`, 1)
			}
			rec := lifecycleRecorder(t, map[string]string{"profile:" + profile: pins})
			recBase := rec.respond
			rec.respond = func(r datalaneRequest) (int, string) {
				if r.Path == "/v1/m/skills/selection" {
					return 200, `{"digest":"one","members":[]}`
				}
				return recBase(r)
			}
			args := []string{"skills", verb, "--profile", profile}
			if verb == "enable" {
				args = append(args, "review", "--pack", lifePack)
			}
			if verb == "disable" {
				args = append(args, "review")
			}
			if _, _, err := execDatalane(t, "", datalaneArgs(rec, args...)...); err != nil {
				t.Fatal(err)
			}
			found := false
			for _, req := range rec.sent("GET") {
				if req.Path == "/v1/m/skills/assignments" {
					found = req.Query.Get("target_kind") == "profile" && req.Query.Get("target_id") == profile
				}
				if req.Path == "/v1/m/skills/selection" {
					found = req.Query.Get("target") == "profile:"+profile
				}
			}
			if !found {
				t.Fatalf("no profile query: %+v", rec.sent("GET"))
			}
			if verb == "enable" || verb == "disable" {
				if body := rec.jsonBody(t); body["target_kind"] != "profile" || body["target_id"] != profile {
					t.Fatalf("profile edit: %v", body)
				}
			}
		})
	}
}
