// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package skills

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"slices"
	"testing"

	"github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func TestRevisionPrincipalCensusPreservesRulesVersion(t *testing.T) {
	st, err := engine.Open(context.Background(), store.Config{Engine: store.EngineSQLite, DSN: ":memory:"}, New(Options{}).RegisterSchema)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	census, ok := st.(store.CompositionCensus)
	if !ok {
		t.Fatal("store exposes no composition census")
	}
	var payload *model.ColumnDecl
	for _, d := range census.CensusDescriptors() {
		if d.Kind != RevisionKind {
			continue
		}
		if defects := d.PrincipalDefects(); len(defects) != 0 {
			t.Fatalf("revision principal census: %v", defects)
		}
		for _, f := range d.Fields {
			if f.Name == "payload" {
				payload = f.Principal
			}
		}
	}
	if payload == nil {
		t.Fatal("revision payload has no principal declaration")
	}
	actor := model.NewID()
	for _, version := range []int{0, activeRules} {
		original := Revision{ActiveRules: version, CreatedBy: "user:" + actor.String()}
		raw, err := json.Marshal(original)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(raw, []byte(`"active_rules"`)) != (version != 0) {
			t.Fatalf("legacy rules-version omission changed: %s", raw)
		}
		row := model.Record{"payload": string(raw), "created_by": original.CreatedBy}
		revision, err := revisionDTO(row)
		if err != nil || revision.ActiveRules != version {
			t.Fatalf("rules version %d read back as %d: %v", version, revision.ActiveRules, err)
		}
		if ids, err := payload.UserIDs(row, "payload"); err != nil || !slices.Equal(ids, []model.ID{actor}) {
			t.Fatalf("rules version %d lost its evidence actor: %v %v", version, ids, err)
		}
	}
}

// A revision published before a rule existed recorded nothing under it: pinning
// reads its own bytes. A revision at the current rule version is trusted as stored.
func TestFreshActiveFindsWhatAnOlderRevisionNeverRecorded(t *testing.T) {
	var b bytes.Buffer
	w := zip.NewWriter(&b)
	f, err := w.Create("risky/SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.Write([]byte("---\nname: risky\ndescription: d\n---\nRun:\n- ```!\nls\n```\n"))
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	pack, err := ImportArchive(context.Background(), bytes.NewReader(b.Bytes()), "zip", "")
	if err != nil {
		t.Fatal(err)
	}
	m := New(Options{ArtifactRoot: t.TempDir()})
	tenant, id := model.NewTenantID(), model.NewID()
	if _, err := m.stageArtifact(tenant, id, pack); err != nil {
		t.Fatal(err)
	}
	// What an old engine stored: members with no extensions recorded.
	old := Revision{ID: id.String(), Manifest: pack.Manifest, ManifestDigest: pack.ManifestDigest, Source: Source{SourceDigest: pack.SourceDigest}, Validator: ValidatorVersion,
		Members: []Member{{Name: "risky", Directory: "risky", Description: "d", SkillDigest: pack.Members[0].SkillDigest, ContentDigest: pack.Members[0].ContentDigest}}}
	got, err := m.freshActive(context.Background(), tenant, old)
	if err != nil || len(got["risky"]) == 0 {
		t.Fatalf("an older revision's fenced command was not found: %v %v", got, err)
	}
	// The same revision stamped at the current version is trusted as stored.
	current := old
	current.ActiveRules = activeRules
	if got, err := m.freshActive(context.Background(), tenant, current); err != nil || got != nil {
		t.Fatalf("a current revision was re-read: %v %v", got, err)
	}
}
