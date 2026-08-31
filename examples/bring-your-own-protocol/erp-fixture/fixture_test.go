// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package fixture

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestFixtureServesDocumentsACLsAndChanges(t *testing.T) {
	srv := NewServer()
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/fw/v1/documents")
	if err != nil {
		t.Fatalf("list documents: %v", err)
	}
	defer resp.Body.Close()
	var list struct {
		Documents []document `json:"documents"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(list.Documents) != 3 {
		t.Fatalf("documents = %d, want 3", len(list.Documents))
	}

	resp, err = http.Get(srv.URL + "/fw/v1/documents/po-1001/acl")
	if err != nil {
		t.Fatalf("fetch ACL: %v", err)
	}
	defer resp.Body.Close()
	var acl struct {
		ACLRefs []string `json:"acl_refs"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&acl); err != nil {
		t.Fatalf("decode ACL: %v", err)
	}
	if len(acl.ACLRefs) != 2 || acl.ACLRefs[0] != "group:engineering" {
		t.Fatalf("ACL refs = %#v", acl.ACLRefs)
	}

	resp, err = http.Get(srv.URL + "/fw/v1/changes")
	if err != nil {
		t.Fatalf("changes: %v", err)
	}
	defer resp.Body.Close()
	var feed struct {
		Changes     []change `json:"changes"`
		ResumeToken string   `json:"resume_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&feed); err != nil {
		t.Fatalf("decode changes: %v", err)
	}
	if len(feed.Changes) != 3 || feed.ResumeToken != "after-initial" {
		t.Fatalf("change feed = %#v token=%q", feed.Changes, feed.ResumeToken)
	}
}
