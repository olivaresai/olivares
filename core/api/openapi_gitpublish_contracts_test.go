// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"net/http"
	"testing"
)

func TestGitpublishRequestBodyCensus(t *testing.T) {
	t.Parallel()
	bodyful := []string{
		http.MethodPost + " /targets", http.MethodPut + " /targets/{id}",
		http.MethodPost + " /targets/{id}/pushes", http.MethodPost + " /targets/{id}/pull-requests",
		http.MethodPost + " /targets/{id}/merges", http.MethodPost + " /intents/{id}/abandon",
	}
	bodyless := []string{http.MethodDelete + " /targets/{id}", http.MethodPost + " /intents/{id}/reconcile"}
	check := func(route string, want gitpublishRequestBodyKind) {
		var method, pattern string
		for i := range route {
			if route[i] == ' ' {
				method, pattern = route[:i], route[i+1:]
				break
			}
		}
		r := moduleRoute{ns: "gitpublish", method: method, pattern: pattern}
		decl, ok := gitpublishRequestBodyDeclarationFor(r)
		if !ok || decl.kind != want {
			t.Fatalf("%s = (%#v, %t), want %v", route, decl, ok, want)
		}
		if _, has := gitpublishRequestBody(r); has != (want == gitpublishBodyful) {
			t.Fatalf("%s body presence = %t", route, has)
		}
		if want == gitpublishBodyful && decl.schema["additionalProperties"] != false {
			t.Fatalf("%s: the decoder is strict", route)
		}
	}
	for _, r := range bodyful {
		check(r, gitpublishBodyful)
	}
	for _, r := range bodyless {
		check(r, gitpublishBodyless)
	}
	for _, s := range []map[string]any{gitpublishTargetSchema(true), gitpublishPushSchema(), gitpublishPullRequestSchema(), gitpublishMergeSchema()} {
		props := s["properties"].(map[string]any)
		for _, forbidden := range []string{"credential_ref", "api_base", "installation_id", "local_path", "repo_path"} {
			if _, ok := props[forbidden]; ok {
				t.Fatalf("schema accepts %s", forbidden)
			}
		}
	}
	if _, ok := gitpublishRequestBodyDeclarationFor(moduleRoute{ns: "other", method: http.MethodPost, pattern: "/targets"}); ok {
		t.Fatal("another namespace matched")
	}
}
