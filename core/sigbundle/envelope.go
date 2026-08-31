// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sigbundle

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"
	"time"
)

// envelope.go is the on-disk shape a signed bundle takes for sneakernet across an
// air gap: a single gzip-compressed tar of a JSON control record, its detached
// signature, and the payload files the record binds by SHA-256.
//
//	manifest.json          the Manifest (control record)
//	manifest.json.sig      detached Ed25519 over SigningInput(tag, manifest.json bytes)
//	<entry.Name>...        one file per Manifest.Entries[i], bound by sha256
//
// Verification is OFFLINE and fail-closed, in this order: (1) verify the signature
// over the domain-separated manifest bytes BEFORE parsing them; (2) parse/validate
// the manifest shape; (3) re-digest every payload file and refuse a mismatch. A
// consumer that only needs the manifest can stop after (2); one that extracts a
// payload MUST pass through (3).

// ManifestSchemaVersion is the current envelope control-record schema. A verifier
// refuses a manifest whose major schema it does not understand rather than guess.
const ManifestSchemaVersion = 1

// maxManifestBytes bounds the control record read from an untrusted tar so a hostile
// bundle cannot exhaust memory before the signature is even checked.
const maxManifestBytes = 4 << 20

// maxBundleBytes bounds the TOTAL decompressed payload read from an untrusted bundle, and
// maxBundleEntries bounds the entry COUNT — both BEFORE the signature is verified. Without
// them a gzip-bomb (tiny compressed → gigabytes decompressed, or millions of tiny entries)
// OOMs the importer pre-verification (F9). A legitimate signed bundle is a small control
// record + a handful of detached signatures/attestations, far under these ceilings.
const (
	maxBundleBytes   = 64 << 20 // 64 MiB total decompressed across all entries
	maxBundleEntries = 4096     // distinct tar entries
)

// Manifest is the bundle control record. It is the ONLY thing signed; the payload
// files are bound to it by their SHA-256, so authenticating the manifest with one
// signature transitively authenticates every payload once its digest matches.
type Manifest struct {
	SchemaVersion int    `json:"schema_version"`
	Kind          string `json:"kind"` // producer-defined bundle kind (e.g. "ddil")
	CreatedAt     string `json:"created_at"`
	// Expires is an optional freshness bound (TUF timestamp-lite): a bundle past this
	// instant is refused. Omitted means no freshness check.
	Expires *string `json:"expires,omitempty"`
	Notes   string  `json:"notes,omitempty"`
	Entries []Entry `json:"entries"`
}

// Entry binds one payload file to the manifest by digest.
type Entry struct {
	Name   string `json:"name"`   // in-bundle path, forward-slash, no traversal
	SHA256 string `json:"sha256"` // lowercase hex over the file's bytes
	Size   int64  `json:"size"`
}

// bundleError classes so callers can branch on why a bundle was rejected.
type bundleError struct{ msg string }

func (e *bundleError) Error() string { return e.msg }

func bundleErr(format string, a ...any) error { return &bundleError{msg: fmt.Sprintf(format, a...)} }

// Payload is one file to place in a bundle.
type Payload struct {
	Name string
	Body []byte
}

// Write serializes a signed bundle to w: it builds the manifest from the payloads
// (digesting each), signs the manifest under tag, and streams the tar.gz. Entries are
// sorted by name so the output is deterministic for identical inputs.
func Write(w io.Writer, tag, kind string, createdAt time.Time, expires *time.Time, notes string, payloads []Payload, priv ed25519.PrivateKey) error {
	if !validTag(tag) {
		return ErrUnknownTag
	}
	if len(priv) != ed25519.PrivateKeySize {
		return bundleErr("sigbundle: invalid signing key")
	}
	sorted := append([]Payload(nil), payloads...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })

	m := Manifest{
		SchemaVersion: ManifestSchemaVersion,
		Kind:          kind,
		CreatedAt:     createdAt.UTC().Format(time.RFC3339),
		Notes:         notes,
		Entries:       make([]Entry, 0, len(sorted)),
	}
	if expires != nil {
		s := expires.UTC().Format(time.RFC3339)
		m.Expires = &s
	}
	seen := map[string]bool{}
	for _, p := range sorted {
		if err := validEntryName(p.Name); err != nil {
			return err
		}
		if seen[p.Name] {
			return bundleErr("sigbundle: duplicate payload name %q", p.Name)
		}
		seen[p.Name] = true
		sum := sha256.Sum256(p.Body)
		m.Entries = append(m.Entries, Entry{Name: p.Name, SHA256: hex.EncodeToString(sum[:]), Size: int64(len(p.Body))})
	}

	manifestBytes, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	sig := Sign(tag, manifestBytes, priv)

	gz := gzip.NewWriter(w)
	tw := tar.NewWriter(gz)
	if err := writeTarFile(tw, "manifest.json", manifestBytes); err != nil {
		return err
	}
	if err := writeTarFile(tw, "manifest.json.sig", sig); err != nil {
		return err
	}
	for _, p := range sorted {
		if err := writeTarFile(tw, p.Name, p.Body); err != nil {
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	return gz.Close()
}

// Opened is the result of verifying and reading a bundle: the authenticated manifest
// and the verified payloads, keyed by entry name.
type Opened struct {
	Manifest Manifest
	Payloads map[string][]byte
}

// Read verifies and reads a signed bundle from r against pub, under tag. Order:
// signature-before-parse, then per-payload digest binding, then optional freshness.
// A nil key fails closed. now is the clock used for the freshness check.
func Read(r io.Reader, tag string, pub ed25519.PublicKey, now time.Time) (Opened, error) {
	if !validTag(tag) {
		return Opened{}, ErrUnknownTag
	}
	gz, err := gzip.NewReader(r)
	if err != nil {
		return Opened{}, bundleErr("sigbundle: not a gzip bundle: %v", err)
	}
	defer func() { _ = gz.Close() }()
	// Bound the TOTAL decompressed stream BEFORE the signature check (F9): a gzip-bomb
	// otherwise OOMs the importer pre-verification. The +1 lets us distinguish "at the ceiling"
	// (a legitimate max-size bundle) from "over it" (the LimitReader yields early EOF, which the
	// tar reader surfaces as a truncation error — fail-closed).
	tr := tar.NewReader(io.LimitReader(gz, maxBundleBytes+1))

	var (
		manifestBytes []byte
		sig           []byte
		files         = map[string][]byte{}
		entries       int
	)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return Opened{}, bundleErr("sigbundle: corrupt or oversized tar: %v", err)
		}
		// Entry-count ceiling: a bundle of millions of tiny entries would grow the files map
		// (and per-entry allocations) unboundedly even under the byte ceiling.
		if entries++; entries > maxBundleEntries {
			return Opened{}, bundleErr("sigbundle: bundle exceeds %d entries", maxBundleEntries)
		}
		if hdr.Typeflag != tar.TypeReg {
			// Directories, symlinks, devices — a signed bundle is flat regular files.
			return Opened{}, bundleErr("sigbundle: bundle entry %q is not a regular file", hdr.Name)
		}
		name := path.Clean(hdr.Name)
		if err := validEntryName(name); err != nil && name != "manifest.json" && name != "manifest.json.sig" {
			return Opened{}, err
		}
		limit := int64(maxManifestBytes)
		if name != "manifest.json" && name != "manifest.json.sig" {
			// Payload files are bounded by the manifest's declared Size, but the
			// manifest is not yet authenticated here, so cap by a generous absolute
			// bound derived from the declared header size to avoid an unbounded read.
			if hdr.Size < 0 {
				return Opened{}, bundleErr("sigbundle: negative entry size for %q", name)
			}
			limit = hdr.Size + 1
		}
		body, err := io.ReadAll(io.LimitReader(tr, limit))
		if err != nil {
			return Opened{}, bundleErr("sigbundle: read entry %q: %v", name, err)
		}
		switch name {
		case "manifest.json":
			manifestBytes = body
		case "manifest.json.sig":
			sig = body
		default:
			if _, dup := files[name]; dup {
				return Opened{}, bundleErr("sigbundle: duplicate entry %q", name)
			}
			files[name] = body
		}
	}

	if manifestBytes == nil || sig == nil {
		return Opened{}, bundleErr("sigbundle: bundle is missing manifest.json or its signature")
	}
	if len(manifestBytes) > maxManifestBytes {
		return Opened{}, bundleErr("sigbundle: manifest exceeds %d bytes", maxManifestBytes)
	}
	// (1) signature BEFORE parse.
	if err := Verify(tag, manifestBytes, sig, pub); err != nil {
		return Opened{}, err
	}
	// (2) parse + validate shape.
	m, err := parseManifest(manifestBytes)
	if err != nil {
		return Opened{}, err
	}
	// (3) freshness.
	if m.Expires != nil {
		exp, perr := time.Parse(time.RFC3339, *m.Expires)
		if perr != nil {
			return Opened{}, bundleErr("sigbundle: manifest expires is not an RFC3339 time")
		}
		if now.After(exp) {
			return Opened{}, bundleErr("sigbundle: bundle expired at %s", *m.Expires)
		}
	}
	// (4) per-payload digest binding: every declared entry must be present and match;
	// no undeclared payload may ride along.
	out := make(map[string][]byte, len(m.Entries))
	declared := map[string]bool{}
	for _, e := range m.Entries {
		declared[e.Name] = true
		body, ok := files[e.Name]
		if !ok {
			return Opened{}, bundleErr("sigbundle: manifest declares %q but the bundle omits it", e.Name)
		}
		if int64(len(body)) != e.Size {
			return Opened{}, bundleErr("sigbundle: entry %q size %d != declared %d", e.Name, len(body), e.Size)
		}
		sum := sha256.Sum256(body)
		if subtle.ConstantTimeCompare([]byte(hex.EncodeToString(sum[:])), []byte(strings.ToLower(e.SHA256))) != 1 {
			return Opened{}, bundleErr("sigbundle: entry %q digest mismatch", e.Name)
		}
		out[e.Name] = body
	}
	for name := range files {
		if !declared[name] {
			return Opened{}, bundleErr("sigbundle: undeclared payload %q is not bound by the manifest", name)
		}
	}
	return Opened{Manifest: m, Payloads: out}, nil
}

func parseManifest(b []byte) (Manifest, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	var m Manifest
	if err := dec.Decode(&m); err != nil {
		return Manifest{}, bundleErr("sigbundle: manifest is not valid JSON: %v", err)
	}
	if m.SchemaVersion != ManifestSchemaVersion {
		return Manifest{}, bundleErr("sigbundle: manifest schema_version %d unsupported (this build understands %d)", m.SchemaVersion, ManifestSchemaVersion)
	}
	if strings.TrimSpace(m.Kind) == "" {
		return Manifest{}, bundleErr("sigbundle: manifest has no kind")
	}
	if _, err := time.Parse(time.RFC3339, m.CreatedAt); err != nil {
		return Manifest{}, bundleErr("sigbundle: manifest created_at is not an RFC3339 time")
	}
	seen := map[string]bool{}
	for i, e := range m.Entries {
		if err := validEntryName(e.Name); err != nil {
			return Manifest{}, err
		}
		if seen[e.Name] {
			return Manifest{}, bundleErr("sigbundle: manifest entry %q is duplicated", e.Name)
		}
		seen[e.Name] = true
		sum := strings.ToLower(strings.TrimSpace(e.SHA256))
		if len(sum) != 2*sha256.Size || !isHex(sum) {
			return Manifest{}, bundleErr("sigbundle: manifest entry %d has a non-SHA-256 digest", i)
		}
		if e.Size < 0 {
			return Manifest{}, bundleErr("sigbundle: manifest entry %q has a negative size", e.Name)
		}
		m.Entries[i].SHA256 = sum
	}
	return m, nil
}

// validEntryName refuses anything that is not a safe, flat-ish relative path: no
// absolute paths, no "..", no leading slash, no backslashes (a Windows-style path could
// escape on extraction). The two control files are validated separately by the reader.
func validEntryName(name string) error {
	if name == "" {
		return bundleErr("sigbundle: empty entry name")
	}
	if name == "manifest.json" || name == "manifest.json.sig" {
		return bundleErr("sigbundle: %q is a reserved control-file name", name)
	}
	if strings.ContainsRune(name, '\\') {
		return bundleErr("sigbundle: entry name %q contains a backslash", name)
	}
	if path.IsAbs(name) || strings.HasPrefix(name, "/") {
		return bundleErr("sigbundle: entry name %q is absolute", name)
	}
	if name != path.Clean(name) {
		return bundleErr("sigbundle: entry name %q is not clean", name)
	}
	for _, seg := range strings.Split(name, "/") {
		if seg == ".." {
			return bundleErr("sigbundle: entry name %q escapes the bundle root", name)
		}
	}
	return nil
}

func writeTarFile(tw *tar.Writer, name string, body []byte) error {
	hdr := &tar.Header{Name: name, Mode: 0o600, Size: int64(len(body)), Typeflag: tar.TypeReg}
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	_, err := tw.Write(body)
	return err
}

func isHex(s string) bool {
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return len(s) > 0
}
