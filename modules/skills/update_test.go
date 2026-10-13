// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package skills_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/modules/skills"
)

// revisionDiff and updateReply mirror the wire shapes the console and CLI read.
type revisionDiff struct {
	PackID string `json:"pack_id"`
	From   *struct {
		RevisionID string `json:"revision_id"`
		Number     int64  `json:"number"`
	} `json:"from"`
	To struct {
		RevisionID string `json:"revision_id"`
		Number     int64  `json:"number"`
	} `json:"to"`
	Members []skills.MemberChange `json:"members"`
	Files   []skills.FileChange   `json:"files"`
}

type updateReply struct {
	State   string `json:"state"`
	Current struct {
		RevisionID     string `json:"revision_id"`
		Number         int64  `json:"number"`
		ResolvedCommit string `json:"resolved_commit"`
	} `json:"current"`
	Source    skills.Source `json:"source"`
	Candidate *struct {
		ManifestDigest string `json:"manifest_digest"`
		ResolvedCommit string `json:"resolved_commit"`
	} `json:"candidate"`
	Diff *skills.PackDiff `json:"diff"`
}

func (h catalogHarness) get(t *testing.T, route string, into any) int {
	t.Helper()
	reply := h.request("GET", route, nil, "")
	if into != nil && reply.Code == http.StatusOK {
		if err := json.Unmarshal(reply.Body.Bytes(), into); err != nil {
			t.Fatalf("%s: %v\n%s", route, err, reply.Body.String())
		}
	}
	return reply.Code
}

func TestSkillsDiffRouteComparesRevisionsAndDefaultsToThePreviousOne(t *testing.T) {
	h := catalog(t)
	first := h.install(t, "diff-1", "body one\n")
	second := h.upload(t, "diff-2", "", first.Pack.ID, archive(t, []string{"research/SKILL.md", "research/new.md"}, []string{strings.Replace(harmless, "Read references/guide.md.", "Read references/guide.md twice.", 1), "added support\n"}))
	if second.Code != http.StatusCreated {
		t.Fatalf("second revision: %d %s", second.Code, second.Body.String())
	}
	var next skills.InstallResult
	if err := json.Unmarshal(second.Body.Bytes(), &next); err != nil {
		t.Fatal(err)
	}
	route := "/v1/m/skills/packs/" + first.Pack.ID + "/diff"

	var defaulted revisionDiff
	if code := h.get(t, route, &defaulted); code != http.StatusOK {
		t.Fatalf("default diff: %d", code)
	}
	if defaulted.From == nil || defaulted.From.RevisionID != first.Revision.ID || defaulted.To.RevisionID != next.Revision.ID || defaulted.To.Number != 2 {
		t.Fatalf("default sides: %+v", defaulted)
	}
	paths := map[string]string{}
	for _, f := range defaulted.Files {
		paths[f.Path] = f.Change
	}
	if paths["research/new.md"] != "added" || paths["research/SKILL.md"] != "changed" {
		t.Fatalf("files: %v", paths)
	}

	var explicit revisionDiff
	if code := h.get(t, route+"?from="+first.Revision.ID+"&to="+next.Revision.ID, &explicit); code != http.StatusOK || len(explicit.Files) != len(defaulted.Files) {
		t.Fatalf("explicit diff: %d %+v", code, explicit)
	}
	var reversed revisionDiff
	if code := h.get(t, route+"?from="+next.Revision.ID+"&to="+first.Revision.ID, &reversed); code != http.StatusOK {
		t.Fatalf("a rollback is read the same way: %d", code)
	}
	reversedPaths := map[string]string{}
	for _, f := range reversed.Files {
		reversedPaths[f.Path] = f.Change
	}
	if reversedPaths["research/new.md"] != "removed" {
		t.Fatalf("reversed files: %v", reversedPaths)
	}

	// The first revision has no predecessor: everything it holds is new.
	var initial revisionDiff
	if code := h.get(t, route+"?to="+first.Revision.ID, &initial); code != http.StatusOK || initial.From != nil || len(initial.Files) == 0 || initial.Files[0].Change != "added" {
		t.Fatalf("first revision diff: %d %+v", code, initial)
	}
}

func TestSkillsDiffRouteRefusesForeignAndMalformedRevisions(t *testing.T) {
	h := catalog(t)
	a := h.install(t, "diff-a", "a body\n")
	b := h.upload(t, "diff-b", "other", "", archive(t, []string{"research/SKILL.md"}, []string{harmless}))
	var other skills.InstallResult
	if err := json.Unmarshal(b.Body.Bytes(), &other); err != nil || b.Code != http.StatusCreated {
		t.Fatalf("second pack: %d %v", b.Code, err)
	}
	route := "/v1/m/skills/packs/" + a.Pack.ID + "/diff"
	for name, query := range map[string]string{
		"revision of another pack": "?to=" + other.Revision.ID,
		"unknown revision":         "?to=00000000-0000-4000-8000-000000000000",
		"malformed revision":       "?from=not-an-id",
	} {
		if code := h.get(t, route+query, nil); code != http.StatusNotFound && code != http.StatusBadRequest {
			t.Fatalf("%s: status %d", name, code)
		}
	}
	anonymous := httptest.NewRecorder()
	h.server.ServeHTTP(anonymous, httptest.NewRequest("GET", route, nil))
	if anonymous.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous diff: %d", anonymous.Code)
	}
}

// gitUpdateHarness serves a Git fixture the test can commit to, and installs its
// research directory the way a user does: a ref, a subdirectory and a name.
func gitUpdateHarness(t *testing.T) (h catalogHarness, repo string, installed skills.InstallResult) {
	t.Helper()
	repo, _, source := gitFixture(t)
	g, _ := gitImporter(t, source)
	h = catalogOptions(t, skills.Options{Git: &g, Targets: workspaceAuthority{}})
	reply := h.installJSON("update-git-install", "/v1/m/skills/packs", map[string]any{
		"name":   "fixture",
		"source": map[string]string{"kind": "git", "url": "https://example.com/fixture.git", "ref": "HEAD", "subdir": "research"},
	})
	if reply.Code != http.StatusCreated {
		t.Fatalf("git install: %d %s", reply.Code, reply.Body.String())
	}
	if err := json.Unmarshal(reply.Body.Bytes(), &installed); err != nil {
		t.Fatal(err)
	}
	return h, repo, installed
}

func commitFixture(t *testing.T, repo, file, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(repo, "research", file), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	fixtureGit(t, repo, nil, "add", "research")
	fixtureGit(t, repo, nil, "commit", "--quiet", "-m", "update "+file)
}

func (h catalogHarness) update(key, pack string, body map[string]any) *httptest.ResponseRecorder {
	return h.installJSON(key, "/v1/m/skills/packs/"+pack+"/update", body)
}

func TestSkillsUpdateChecksTheRecordedSourceAndAppliesOnlyWhatWasReviewed(t *testing.T) {
	h, repo, installed := gitUpdateHarness(t)
	if installed.Revision.Source.Subdir != "research" {
		t.Fatalf("the selected subdirectory is not recorded: %+v", installed.Revision.Source)
	}
	packRoute := "/v1/m/skills/packs/" + installed.Pack.ID
	h.assign(t, h.workspace(t, false), installed.Revision.ID)

	// Nothing new upstream: the check says so and publishes nothing.
	var current updateReply
	reply := h.update("check-1", installed.Pack.ID, map[string]any{})
	if err := json.Unmarshal(reply.Body.Bytes(), &current); err != nil || reply.Code != http.StatusOK || current.State != "current" || current.Current.Number != 1 {
		t.Fatalf("current: %d %s", reply.Code, reply.Body.String())
	}

	commitFixture(t, repo, "SKILL.md", strings.Replace(harmless, "Read references/guide.md.", "Read references/guide.md, then summarize.", 1))
	commitFixture(t, repo, "extra.md", "a new support file\n")

	var available updateReply
	reply = h.update("check-2", installed.Pack.ID, map[string]any{})
	if err := json.Unmarshal(reply.Body.Bytes(), &available); err != nil || reply.Code != http.StatusOK || available.State != "available" {
		t.Fatalf("available: %d %s", reply.Code, reply.Body.String())
	}
	if available.Candidate == nil || available.Candidate.ManifestDigest == "" || available.Candidate.ResolvedCommit == "" || available.Candidate.ResolvedCommit == installed.Revision.Source.ResolvedCommit {
		t.Fatalf("candidate: %+v", available.Candidate)
	}
	if available.Source.Kind != "git" || available.Source.Subdir != "research" || available.Source.RequestedRef != "HEAD" {
		t.Fatalf("the check does not say which tree it compared: %+v", available.Source)
	}
	if available.Diff == nil || len(available.Diff.Files) != 2 {
		t.Fatalf("diff: %+v", available.Diff)
	}
	changed := map[string]string{}
	for _, f := range available.Diff.Files {
		changed[f.Path] = f.Change
		if f.Path == "research/SKILL.md" && !strings.Contains(f.Diff, "+Read references/guide.md, then summarize.") {
			t.Fatalf("reviewable text missing: %q", f.Diff)
		}
	}
	if changed["research/extra.md"] != "added" || changed["research/SKILL.md"] != "changed" {
		t.Fatalf("changes: %v", changed)
	}
	var detail skills.PackDetail
	if code := h.get(t, packRoute, &detail); code != http.StatusOK || len(detail.Revisions) != 1 {
		t.Fatalf("a check published a revision: %d %+v", code, detail.Pack)
	}

	// Applying demands the digest the reviewer saw, so a source that moved after the
	// review cannot slip in under it.
	if refused := h.update("apply-1", installed.Pack.ID, map[string]any{"apply": true}); refused.Code != http.StatusBadRequest {
		t.Fatalf("apply without a reviewed digest: %d %s", refused.Code, refused.Body.String())
	}
	if stale := h.update("apply-2", installed.Pack.ID, map[string]any{"apply": true, "expected_digest": strings.Repeat("0", 64)}); stale.Code != http.StatusBadRequest || !strings.Contains(stale.Body.String(), "source_changed") {
		t.Fatalf("apply of an unreviewed digest: %d %s", stale.Code, stale.Body.String())
	}
	if code := h.get(t, packRoute, &detail); code != http.StatusOK || len(detail.Revisions) != 1 {
		t.Fatalf("a refused apply published a revision: %d", code)
	}

	apply := map[string]any{"apply": true, "expected_digest": available.Candidate.ManifestDigest}
	published := h.update("apply-3", installed.Pack.ID, apply)
	var result skills.InstallResult
	if err := json.Unmarshal(published.Body.Bytes(), &result); err != nil || published.Code != http.StatusCreated || result.State != "catalog_published" {
		t.Fatalf("apply: %d %s", published.Code, published.Body.String())
	}
	if result.Revision.Number != 2 || result.Revision.Source.ResolvedCommit != available.Candidate.ResolvedCommit || result.Revision.Source.Subdir != "research" || result.Revision.ID == installed.Revision.ID {
		t.Fatalf("applied revision: %+v", result.Revision)
	}
	// A lost response is retried with the same key and returns the same revision.
	if replay := h.update("apply-3", installed.Pack.ID, apply); replay.Code != http.StatusOK || replay.Body.String() != published.Body.String() {
		t.Fatalf("replay: %d %s", replay.Code, replay.Body.String())
	}
	// Applying never moves a pin: the one made before the update keeps its revision.
	pins := h.packUses(t, installed.Pack.ID, url.Values{"limit": {"10"}})
	if len(pins.Items) != 1 || pins.Items[0].RevisionID != installed.Revision.ID {
		t.Fatalf("an update moved a pin: %+v", pins.Items)
	}
	// The old revision is retained and the check is current again.
	if code := h.get(t, packRoute, &detail); code != http.StatusOK || len(detail.Revisions) != 2 || detail.Pack.LatestRevisionID != result.Revision.ID {
		t.Fatalf("history: %d %+v", code, detail.Pack)
	}
	reply = h.update("check-3", installed.Pack.ID, map[string]any{})
	if err := json.Unmarshal(reply.Body.Bytes(), &current); err != nil || current.State != "current" || current.Current.Number != 2 {
		t.Fatalf("after apply: %d %s", reply.Code, reply.Body.String())
	}
	// Applying a digest that is already the latest publishes nothing.
	if again := h.update("apply-4", installed.Pack.ID, apply); again.Code != http.StatusConflict {
		t.Fatalf("apply of the current revision: %d %s", again.Code, again.Body.String())
	}
}

func TestSkillsUpdateReadsASourceItCanReadAgainOnly(t *testing.T) {
	h := catalog(t)
	uploaded := h.install(t, "upload-install", "uploaded body\n")
	reply := h.update("update-upload", uploaded.Pack.ID, map[string]any{})
	if reply.Code != http.StatusBadRequest || !strings.Contains(reply.Body.String(), "update_unsupported") {
		t.Fatalf("an uploaded archive cannot be read again: %d %s", reply.Code, reply.Body.String())
	}
	missing := h.update("update-missing", "00000000-0000-4000-8000-000000000000", map[string]any{})
	if missing.Code != http.StatusNotFound {
		t.Fatalf("unknown pack: %d %s", missing.Code, missing.Body.String())
	}
}

func TestSkillsDiffRouteDefaultsToTheRevisionBeforeTheLatestOfThree(t *testing.T) {
	h := catalog(t)
	one := h.install(t, "three-1", "support one")
	var latest skills.InstallResult
	ids := []string{one.Revision.ID}
	for i, content := range []string{"support two", "support three"} {
		reply := h.upload(t, "three-"+strconv.Itoa(i+2), "", one.Pack.ID, archive(t, []string{"research/SKILL.md", "research/support.md"}, []string{harmless, content}))
		if err := json.Unmarshal(reply.Body.Bytes(), &latest); err != nil || reply.Code != http.StatusCreated {
			t.Fatalf("revision %d: %d %s", i+2, reply.Code, reply.Body.String())
		}
		ids = append(ids, latest.Revision.ID)
	}
	var got revisionDiff
	if code := h.get(t, "/v1/m/skills/packs/"+one.Pack.ID+"/diff", &got); code != http.StatusOK {
		t.Fatalf("diff: %d", code)
	}
	if got.From == nil || got.From.RevisionID != ids[1] || got.To.RevisionID != ids[2] || got.From.Number != 2 || got.To.Number != 3 {
		t.Fatalf("compared %+v with %+v, want revisions 2 and 3", got.From, got.To)
	}
}

func TestSkillsUpdateRefusesARefOnASourceThatHasNone(t *testing.T) {
	h := catalog(t)
	reply := h.installJSON("builtin-for-ref", "/v1/m/skills/packs", map[string]any{"name": "ecc-security", "source": map[string]string{"kind": "builtin", "ref": "ecc-security"}})
	var installed skills.InstallResult
	if err := json.Unmarshal(reply.Body.Bytes(), &installed); err != nil || reply.Code != http.StatusCreated {
		t.Fatalf("builtin install: %d %s", reply.Code, reply.Body.String())
	}
	if refused := h.update("ref-on-builtin", installed.Pack.ID, map[string]any{"ref": "v2"}); refused.Code != http.StatusBadRequest {
		t.Fatalf("a ref on a built-in pack: %d %s", refused.Code, refused.Body.String())
	}
	// The built-in pack is read from this binary's own catalog: current.
	var current updateReply
	check := h.update("builtin-current", installed.Pack.ID, map[string]any{})
	if err := json.Unmarshal(check.Body.Bytes(), &current); err != nil || check.Code != http.StatusOK || current.State != "current" || current.Source.Kind != "builtin" {
		t.Fatalf("built-in check: %d %s", check.Code, check.Body.String())
	}
}
