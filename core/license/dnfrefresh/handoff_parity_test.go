// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build unix

package dnfrefresh

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestPublishRefusesASymlinkedDirectory: the directory is opened with O_DIRECTORY|O_NOFOLLOW, so a
// symlink writes nothing, neither through the link nor into its target.
func TestPublishRefusesASymlinkedDirectory(t *testing.T) {
	real := handoffDir(t)
	link := filepath.Join(t.TempDir(), "olivares-dnf-handoff-link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if err := Publish(link, issuedDocument(t)); !errors.Is(err, ErrHandoffCustody) {
		t.Fatalf("Publish through a symlinked directory = %v, want ErrHandoffCustody", err)
	}
	if got := listDir(t, real); len(got) != 0 {
		t.Fatalf("the symlink's target received %v", got)
	}
}

// TestPublishRefusesAnotherOwnersDirectory: fstat requires the running uid. Another uid writes nothing.
func TestPublishRefusesAnotherOwnersDirectory(t *testing.T) {
	dir := handoffDir(t)
	saved := getuid
	getuid = func() int { return os.Getuid() + 1 }
	t.Cleanup(func() { getuid = saved })
	if err := Publish(dir, issuedDocument(t)); !errors.Is(err, ErrHandoffCustody) {
		t.Fatalf("Publish into another uid's directory = %v, want ErrHandoffCustody", err)
	}
	if got := listDir(t, dir); len(got) != 0 {
		t.Fatalf("wrong owner wrote %v", got)
	}
}

// TestPublishRefusesAPlantedTemporarySymlink: .<cycle>.tmp is created with O_NOFOLLOW|O_EXCL, so a
// symlink under that name refuses and its target is unchanged. A symlink at <cycle>.json is replaced
// by the regular 0600 file; its target is unchanged.
func TestPublishRefusesAPlantedTemporarySymlink(t *testing.T) {
	dir := handoffDir(t)
	victim := filepath.Join(t.TempDir(), "victim")
	if err := os.WriteFile(victim, []byte("victim"), 0o600); err != nil {
		t.Fatal(err)
	}
	tmp := filepath.Join(dir, "."+validCycle+".tmp")
	if err := os.Symlink(victim, tmp); err != nil {
		t.Fatal(err)
	}
	if err := Publish(dir, issuedDocument(t)); !errors.Is(err, ErrHandoffCustody) {
		t.Fatalf("planted tmp symlink: %v, want ErrHandoffCustody", err)
	}
	if b, _ := os.ReadFile(victim); string(b) != "victim" {
		t.Fatal("the temporary symlink's target was written")
	}
	if err := os.Remove(tmp); err != nil {
		t.Fatal(err)
	}
	final := filepath.Join(dir, validCycle+".json")
	if err := os.Symlink(victim, final); err != nil {
		t.Fatal(err)
	}
	if err := Publish(dir, issuedDocument(t)); err != nil {
		t.Fatalf("publish over a planted final symlink: %v", err)
	}
	if b, _ := os.ReadFile(victim); string(b) != "victim" {
		t.Fatal("the final symlink's target was written")
	}
	info, err := os.Lstat(final)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		t.Fatalf("final is %v %v, want a regular 0600 file", info, err)
	}
}

// maxClaims returns claims whose JSON is exactly 768 bytes, so the credential is 1073 bytes.
func maxClaims(t *testing.T) map[string]any {
	t.Helper()
	exp := testNow.Unix() + 86400
	c := map[string]any{
		"s": "olivares.ai/apt-download/v1", "aud": CredentialAudience, "cls": "current", "h": "sub_1",
		"d": "dep_7", "k": "kid_7", "ep": 3, "lin": map[string]any{"serial": "conn_production_dep_7_2", "issue_seq": 2},
		"set": "biz+reg", "su": []any{"entitled-security", "entitled-stable"}, "ctx": testContext, "rsn": "current",
		"iat": testNow.Unix(), "exp": exp, "jti": "j",
	}
	b, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	c["jti"] = "j" + strings.Repeat("x", 768-len(b))
	if b, err = json.Marshal(c); err != nil || len(b) != 768 {
		t.Fatalf("claims are %d bytes (%v)", len(b), err)
	}
	return c
}

// TestMaximumCredentialIsAcceptedAndPublishedSilently: a credential of exactly 1073 bytes with valid
// claims is accepted, its handoff stays within 4096 bytes, and publishing it prints nothing.
func TestMaximumCredentialIsAcceptedAndPublishedSilently(t *testing.T) {
	obj := answerFixture(t, func(c map[string]any) {
		for k, v := range maxClaims(t) {
			c[k] = v
		}
	}, nil)
	cred, _ := obj["dnf_credential"].(string)
	if len(cred) != 1073 {
		t.Fatalf("credential is %d bytes, want 1073", len(cred))
	}
	a, err := CheckAnswer(obj, testBinding, testNow)
	if err != nil {
		t.Fatalf("a valid 1073-byte credential: %v", err)
	}
	doc := Issued(validCycle, nil, a, testNow)
	data, err := doc.Marshal()
	if err != nil || len(data) > MaxHandoffBytes {
		t.Fatalf("handoff %d bytes, %v", len(data), err)
	}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	savedOut, savedErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = w, w
	perr := Publish(handoffDir(t), doc)
	os.Stdout, os.Stderr = savedOut, savedErr
	_ = w.Close()
	printed, _ := io.ReadAll(r)
	_ = r.Close()
	if perr != nil {
		t.Fatalf("publish: %v", perr)
	}
	if strings.Contains(string(printed), CredentialPrefix) {
		t.Fatal("publishing printed a download credential")
	}
}
