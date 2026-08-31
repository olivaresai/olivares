// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package content holds the helpers shared by the Olivares AI knowledge DATA
// connectors (gdrive, confluence, notion, sharepoint, s3content) that implement
// contentsource.Source. It keeps the secret-by-reference rule and the export-file
// reader in one place so every data connector enforces them identically.
//
// It imports only the standard library — no SDK, no engine — so it never becomes
// a data path and the Apache boundary stays trivially clean.
package content

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Bounds keep a single field from an export from ballooning a Document or its
// provenance. They are conservative; a connector trims/truncates to them.
const (
	// MaxTitleLen bounds a document title.
	MaxTitleLen = 512
	// MaxRefLen bounds a natural reference (doc id, space ref, acl entry).
	MaxRefLen = 1024
	// MaxBodyBytes bounds a single document body a connector returns (1 MiB). A
	// larger source object is a streaming concern documented as a follow-up; a
	// connector truncates and marks it rather than buffering unbounded content.
	MaxBodyBytes = 1 << 20
	// MaxACLEntries bounds the permission references on one document.
	MaxACLEntries = 256
)

// credentialRefSchemes is the closed allow-list of secret-store reference schemes
// a data connector's credential setting may use. A credential is ALWAYS a
// reference into a secret store, NEVER the secret itself (docs/SECURITY-HARDENING.md): the config
// field can only ever hold a pointer, so a cleartext token is rejected at Open.
// This validation is the connector-side belt; the ENGINE'S secret resolver
// (core/secret) is what actually opens the reference to the live value
// before Open and refuses an inline literal in any declared-secret field.
var credentialRefSchemes = map[string]bool{
	"vault":              true,
	"infisical":          true,
	"aws-secretsmanager": true,
	"gcp-secretmanager":  true,
	"azure-keyvault":     true,
	"k8s-secret":         true,
	"env":                true,
	"file":               true,
}

// ValidateCredentialRef returns a non-empty message when ref is a non-empty value
// that is not a valid secret-store reference. An empty ref is allowed (a connector
// reading a local export needs no credential); a non-empty ref MUST be
// "<scheme>:<locator>" with an allow-listed scheme and not itself look like raw
// credential material.
func ValidateCredentialRef(ref string) string {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return ""
	}
	if len(ref) > MaxRefLen {
		return "credential_ref too long"
	}
	scheme, locator, ok := strings.Cut(ref, ":")
	if !ok || !credentialRefSchemes[strings.ToLower(strings.TrimSpace(scheme))] || strings.TrimSpace(locator) == "" {
		return "credential_ref must be a secret-store reference of the form <scheme>:<locator> (e.g. vault:secret/data/gdrive#token); a cleartext secret is never accepted"
	}
	if looksLikeRawSecret(locator) {
		return "credential_ref locator looks like a raw credential, not a reference"
	}
	return ""
}

// looksLikeRawSecret heuristically rejects a long high-entropy blob pasted where a
// reference belongs. It is defense-in-depth on top of the scheme allow-list, not a
// classifier.
func looksLikeRawSecret(s string) bool {
	t := strings.TrimSpace(s)
	if len(t) < 40 || strings.ContainsAny(t, " \t\n/") {
		return false
	}
	n := 0
	for _, r := range t {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '+' || r == '=' || r == '_' || r == '-' {
			n++
		}
	}
	return float64(n)/float64(len(t)) > 0.9
}

// ExportFiles resolves a configured path to a sorted list of files to parse. A
// directory contributes its entries whose name ends in one of suffixes (in name
// order); a single file contributes itself. It is read-only.
func ExportFiles(path string, suffixes ...string) ([]string, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !fi.IsDir() {
		return []string{path}, nil
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}
	var files []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		for _, suf := range suffixes {
			if strings.HasSuffix(name, suf) {
				files = append(files, filepath.Join(path, name))
				break
			}
		}
	}
	sort.Strings(files)
	return files, nil
}

// maxExportBytes bounds a whole export file (16 MiB) so a pathological file
// cannot exhaust memory.
const maxExportBytes = MaxBodyBytes * 16

// ErrExportTooLarge is returned when an export file exceeds the read cap.
var ErrExportTooLarge = errors.New("content: export file exceeds size cap")

// ReadJSON reads and unmarshals one export file into v. It bounds the read so a
// malformed or huge file cannot exhaust memory.
func ReadJSON(path string, v any) error {
	f, err := os.Open(path) //nolint:gosec // path comes from operator config, read-only
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, maxExportBytes+1))
	if err != nil {
		return err
	}
	if int64(len(data)) > maxExportBytes {
		return ErrExportTooLarge
	}
	return json.Unmarshal(data, v)
}

// Truncate bounds a string to n runes, appending nothing (the caller decides how
// to mark truncation). It is rune-safe so it never splits a multi-byte character.
func Truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// CleanACL bounds and de-duplicates a list of permission references, trimming each
// and dropping empties, so a document's ACL stays small and well-formed.
func CleanACL(refs []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(refs))
	for _, r := range refs {
		r = strings.TrimSpace(r)
		if r == "" || seen[r] {
			continue
		}
		if len(r) > MaxRefLen {
			r = Truncate(r, MaxRefLen)
		}
		seen[r] = true
		out = append(out, r)
		if len(out) >= MaxACLEntries {
			break
		}
	}
	return out
}
