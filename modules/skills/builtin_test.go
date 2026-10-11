// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package skills_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/modules/skills"
)

func (h catalogHarness) installJSON(key, route string, body any) *httptest.ResponseRecorder {
	encoded, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", route, bytes.NewReader(encoded))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+h.token)
	req.Header.Set("X-Olivares-Tenant", h.tenant.String())
	req.Header.Set("Idempotency-Key", key)
	out := httptest.NewRecorder()
	h.server.ServeHTTP(out, req)
	return out
}

func builtinSource(ref string) map[string]string {
	return map[string]string{"kind": "builtin", "ref": ref}
}

func TestBuiltinPacksInstallAsPinnedRevisionsWithTheirLicense(t *testing.T) {
	pin := readPin(t)
	h := catalog(t)
	for _, entry := range pin.Packs {
		t.Run(entry.ID, func(t *testing.T) {
			reply := h.installJSON("install-"+entry.ID, "/v1/m/skills/packs", map[string]any{"name": entry.ID, "source": builtinSource(entry.ID)})
			var result skills.InstallResult
			if err := json.Unmarshal(reply.Body.Bytes(), &result); err != nil || reply.Code != http.StatusCreated {
				t.Fatalf("install: %d %s", reply.Code, reply.Body.String())
			}
			source := result.Revision.Source
			if source.Kind != "builtin" || source.RequestedRef != entry.ID || source.ResolvedCommit != pin.Sources[entry.Source].Commit ||
				source.Origin != pin.Sources[entry.Source].Repository || source.SourceDigest != entry.SHA256 {
				t.Fatalf("revision does not record the pinned source: %+v", source)
			}
			if len(result.Revision.Members) != entry.Skills || result.Revision.Validator != skills.ValidatorVersion {
				t.Fatalf("members = %d, pinned %d", len(result.Revision.Members), entry.Skills)
			}
			license := false
			for _, file := range result.Revision.Manifest {
				license = license || file.Path == "LICENSE"
			}
			if !license {
				t.Fatal("the pack does not carry its upstream LICENSE")
			}
		})
	}
	if len(pin.Packs) == 0 {
		t.Fatal("PIN.json lists no packs")
	}
}

func TestBuiltinPackUpdatesAsANewRevisionOfTheSamePack(t *testing.T) {
	h := catalog(t)
	body := map[string]any{"name": "ecc-security", "source": builtinSource("ecc-security")}
	var first, second skills.InstallResult
	if reply := h.installJSON("first", "/v1/m/skills/packs", body); reply.Code != http.StatusCreated || json.Unmarshal(reply.Body.Bytes(), &first) != nil {
		t.Fatalf("first install: %d %s", reply.Code, reply.Body.String())
	}
	reply := h.installJSON("second", "/v1/m/skills/packs/"+first.Pack.ID+"/revisions", body)
	if reply.Code != http.StatusCreated || json.Unmarshal(reply.Body.Bytes(), &second) != nil {
		t.Fatalf("update: %d %s", reply.Code, reply.Body.String())
	}
	if second.Pack.ID != first.Pack.ID || second.Revision.Number != 2 || second.Revision.ID == first.Revision.ID || second.Revision.ManifestDigest != first.Revision.ManifestDigest {
		t.Fatalf("update did not publish a second immutable revision of the same bytes: %+v", second.Revision)
	}
}

func TestBuiltinInstallRefusesWhatIsNotAPinnedPack(t *testing.T) {
	h := catalog(t)
	for i, tc := range []struct {
		name, code string
		source     map[string]string
	}{
		{"unknown id", "unsupported_source", builtinSource("not-a-pack")},
		{"no id", "invalid_request", map[string]string{"kind": "builtin"}},
		{"agents are data", "unsupported_source", builtinSource("ecc-agents")},
		{"a url", "invalid_request", map[string]string{"kind": "builtin", "ref": "ecc-security", "url": "https://example.com/x.git"}},
		{"a directory", "invalid_request", map[string]string{"kind": "builtin", "ref": "ecc-security", "directory": "x"}},
		{"a subdirectory", "invalid_request", map[string]string{"kind": "builtin", "ref": "ecc-security", "subdir": "x"}},
		{"a workspace", "invalid_request", map[string]string{"kind": "builtin", "ref": "ecc-security", "workspace_ref": "00000000-0000-4000-8000-000000000000"}},
		{"a wrong digest", "source_changed", map[string]string{"kind": "builtin", "ref": "ecc-security", "expected_digest": strings.Repeat("0", 64)}},
		{"a path-like id", "unsupported_source", builtinSource("../builtin/PIN")},
		{"a file name as id", "unsupported_source", builtinSource("ecc-security.tar.gz")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reply := h.installJSON(fmt.Sprintf("refuse-%d", i), "/v1/m/skills/packs", map[string]any{"name": "refused", "source": tc.source})
			if reply.Code != http.StatusBadRequest || !strings.Contains(reply.Body.String(), `"code":"`+tc.code+`"`) {
				t.Fatalf("want 400 %s, got %d %s", tc.code, reply.Code, reply.Body.String())
			}
		})
	}
	// The same names publish when the request is right: the refusals above were about the request.
	if reply := h.installJSON("accepted", "/v1/m/skills/packs", map[string]any{"name": "refused", "source": builtinSource("ecc-security")}); reply.Code != http.StatusCreated {
		t.Fatalf("control install: %d %s", reply.Code, reply.Body.String())
	}
	if list := h.request("GET", "/v1/m/skills/packs", nil, ""); strings.Count(list.Body.String(), `"name":"refused"`) != 1 {
		t.Fatalf("a refused request published a pack: %s", list.Body.String())
	}
}

func TestBuiltinInstallReplaysTheRecordedResult(t *testing.T) {
	h := catalog(t)
	body := map[string]any{"name": "ecc-database", "source": builtinSource("ecc-database")}
	first := h.installJSON("replay", "/v1/m/skills/packs", body)
	again := h.installJSON("replay", "/v1/m/skills/packs", body)
	if first.Code != http.StatusCreated || again.Code != http.StatusOK || again.Body.String() != first.Body.String() {
		t.Fatalf("replay: %d then %d", first.Code, again.Code)
	}
}

func TestBuiltinSourceIsPublishedInTheJSONImportContract(t *testing.T) {
	doc := api.ModuleOpenAPIDocument([]api.Module{skills.New(skills.Options{})})
	for _, route := range []string{"/v1/m/skills/packs", "/v1/m/skills/packs/{id}/revisions"} {
		post := doc["paths"].(map[string]any)["/v1/m/skills"+strings.TrimPrefix(route, "/v1/m/skills")].(map[string]any)["post"].(map[string]any)
		schema := post["requestBody"].(map[string]any)["content"].(map[string]any)["application/json"].(map[string]any)["schema"].(map[string]any)
		source := schema["properties"].(map[string]any)["source"].(map[string]any)
		if kinds := source["properties"].(map[string]any)["kind"].(map[string]any)["enum"]; !reflect.DeepEqual(kinds, []string{"git", "workspace", "builtin"}) {
			t.Fatalf("%s: source kinds = %v", route, kinds)
		}
		branches := source["oneOf"].([]any)
		last := branches[len(branches)-1].(map[string]any)
		if len(branches) != 3 || last["properties"].(map[string]any)["kind"].(map[string]any)["const"] != "builtin" || !reflect.DeepEqual(last["required"], []string{"ref"}) {
			t.Fatalf("%s: built-in branch missing: %v", route, branches)
		}
	}
}
