// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
)

// turnlessConfigurer serves configuration calls without a live session turn: the
// API path under test is the one engine.configureSession enters after its check.
type turnlessConfigurer struct{ *engine }

func (e turnlessConfigurer) configureSession(w http.ResponseWriter, r *http.Request, _, launcher auth.Principal) {
	e.api.ServeSession(w, r, launcher)
}

// The configuration tools are the console's own routes reached with the session
// launcher's rights. For each tool, a launcher holding the right gets what the
// console gives that launcher, and a launcher without it gets the console's
// refusal: the same status, error code and message. What a session adds is listed
// by the console; it configures only its own workspace and never sends secrets.

// installSkillPack uploads a one-skill zip pack through the console's catalog
// import route and returns its first revision ID.
func installSkillPack(t *testing.T, eng *engine, token, tenant, name string) string {
	t.Helper()
	var archive bytes.Buffer
	zw := zip.NewWriter(&archive)
	file, err := zw.Create("research/SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write([]byte("---\nname: research\ndescription: Review primary sources.\nlicense: MIT\n---\nRead the cited source.\n")); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	var form bytes.Buffer
	mw := multipart.NewWriter(&form)
	_ = mw.WriteField("name", name)
	_ = mw.WriteField("format", "zip")
	part, err := mw.CreateFormFile("archive", "pack.zip")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = part.Write(archive.Bytes())
	_ = mw.Close()
	req := httptest.NewRequest(http.MethodPost, "/v1/m/skills/packs", &form)
	req.RemoteAddr = "10.0.0.1:1234"
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-Olivares-Tenant", tenant)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Idempotency-Key", "install-"+name)
	w := httptest.NewRecorder()
	eng.api.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("install pack %s = %d %s", name, w.Code, w.Body.String())
	}
	var result struct {
		Revision struct {
			ID string `json:"id"`
		} `json:"revision"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || result.Revision.ID == "" {
		t.Fatalf("install result %s: %v", w.Body.String(), err)
	}
	return result.Revision.ID
}
