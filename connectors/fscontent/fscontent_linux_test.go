// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

//go:build linux

package fscontent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/olivaresai/olivares/sdk"
)

// TestPOSIXOwnerGroupACL proves owner/group/mode are mapped: a readable file owned by
// the test's uid/gid yields a Document ACL with a user principal and mode/owner attrs.
func TestPOSIXOwnerGroupACL(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("hi"), 0o640); err != nil {
		t.Fatal(err)
	}
	s := New()
	if err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{fRoot: root}}); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close(context.Background()) }()

	doc, err := s.Fetch(context.Background(), "a.txt")
	if err != nil {
		t.Fatal(err)
	}
	hasUser := false
	for _, ref := range doc.ACL {
		if strings.HasPrefix(ref, "user:") {
			hasUser = true
		}
	}
	if !hasUser {
		t.Errorf("expected a user: principal in the mapped ACL, got %v", doc.ACL)
	}
	// 0640 is group-readable → the group is a mapped principal too.
	hasGroup := false
	for _, ref := range doc.ACL {
		if strings.HasPrefix(ref, "group:") {
			hasGroup = true
		}
	}
	if !hasGroup {
		t.Errorf("a group-readable file should map its group, got %v", doc.ACL)
	}
	if doc.Attributes["mode"] == "" || doc.Attributes["owner"] == "" {
		t.Errorf("owner/mode provenance missing: %v", doc.Attributes)
	}
}

// TestXattrClassificationAndLabels proves the per-file classification xattr and the
// external-labels xattr are read. Skipped when the filesystem does not support user
// xattrs (e.g. some tmpfs), rather than failing.
func TestXattrClassificationAndLabels(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "doc.txt")
	if err := os.WriteFile(path, []byte("body"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := unix.Setxattr(path, "user.classification", []byte("confidential"), 0); err != nil {
		t.Skipf("filesystem does not support user xattrs: %v", err)
	}
	if err := unix.Setxattr(path, "user.olivares.labels", []byte("pii,secret"), 0); err != nil {
		t.Skipf("filesystem does not support user xattrs: %v", err)
	}

	s := New()
	if err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{fRoot: root}}); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close(context.Background()) }()

	doc, err := s.Fetch(context.Background(), "doc.txt")
	if err != nil {
		t.Fatal(err)
	}
	if doc.Classification != "confidential" {
		t.Errorf("classification = %q, want confidential", doc.Classification)
	}
	if len(doc.ExternalLabels) != 2 || doc.ExternalLabels[0] != "pii" {
		t.Errorf("external labels = %v, want [pii secret]", doc.ExternalLabels)
	}
}
