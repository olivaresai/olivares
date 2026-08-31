// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package supportbundle assembles the closed, allowlisted diagnostic archive
// shared by the CLI and the console HTTP endpoint.
package supportbundle

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/olivaresai/olivares/core/secret"
)

const (
	// ToolVersion identifies the support-bundle archive contract.
	ToolVersion = "olivares.support.bundle.v1"
	// Notice records the bundle's secret-reference and key-material posture.
	Notice = "secret references (file:/env:/store:) are recorded verbatim and were NOT resolved; signing/sealer keys are excluded"
)

// Section describes one diagnostic payload in manifest.json.
type Section struct {
	Path       string `json:"path"`
	Source     string `json:"source"`
	Bytes      int    `json:"bytes"`
	SHA256     string `json:"sha256"`
	Redactions int    `json:"redactions"`
}

// RedactionSummary aggregates the archive's redaction counts.
type RedactionSummary struct {
	TotalRedactions  int `json:"total_redactions"`
	SectionsScrubbed int `json:"sections_scrubbed"`
}

// Manifest is the integrity manifest stored in every support bundle.
type Manifest struct {
	ToolVersion     string           `json:"tool_version"`
	OlivaresVersion string           `json:"olivares_version"`
	CreatedAt       string           `json:"created_at"`
	Sections        []Section        `json:"sections"`
	Redaction       RedactionSummary `json:"redaction_summary"`
	Notice          string           `json:"notice"`
}

type entry struct {
	path       string
	source     string
	data       []byte
	redactions int
}

// Assembler accepts only paths in the diagnostic archive contract. It never
// walks a directory or copies an arbitrary source file into the archive.
type Assembler struct {
	entries map[string]entry
}

// NewAssembler returns an empty support-bundle assembler.
func NewAssembler() *Assembler {
	return &Assembler{entries: make(map[string]entry)}
}

// Add adds one already-redacted diagnostic payload to the bundle.
func (a *Assembler) Add(name, source string, data []byte, redactions int) error {
	if a == nil {
		return fmt.Errorf("support bundle: nil assembler")
	}
	clean, err := cleanPath(name)
	if err != nil {
		return err
	}
	if !pathAllowed(clean) {
		return fmt.Errorf("support bundle: path %q is not in the diagnostic allowlist", clean)
	}
	if redactions < 0 {
		return fmt.Errorf("support bundle: negative redaction count for %q", clean)
	}
	if _, exists := a.entries[clean]; exists {
		return fmt.Errorf("support bundle: duplicate path %q", clean)
	}
	a.entries[clean] = entry{
		path: clean, source: source, data: append([]byte(nil), data...), redactions: redactions,
	}
	return nil
}

// Paths returns the archive's diagnostic section paths in deterministic order.
func (a *Assembler) Paths() []string {
	if a == nil {
		return []string{}
	}
	paths := make([]string, 0, len(a.entries))
	for name := range a.entries {
		paths = append(paths, name)
	}
	sort.Strings(paths)
	return paths
}

// Write writes an atomic 0600 tar.gz with deterministic entry ordering and
// metadata. containsSensitive must be the caller's canonical final content
// guard. The returned digest covers the exact manifest.json bytes in the
// archive.
func Write(
	outPath, olivaresVersion string,
	createdAt time.Time,
	assembler *Assembler,
	containsSensitive func(string) bool,
) (string, error) {
	if assembler == nil {
		return "", fmt.Errorf("support bundle: nil assembler")
	}
	if strings.TrimSpace(outPath) == "" {
		return "", fmt.Errorf("support bundle: output path is empty")
	}
	if containsSensitive == nil {
		return "", fmt.Errorf("support bundle: sensitive-content guard is not configured")
	}

	paths := assembler.Paths()
	manifest := Manifest{
		ToolVersion: ToolVersion, OlivaresVersion: olivaresVersion,
		CreatedAt: createdAt.UTC().Format(time.RFC3339), Notice: Notice,
	}
	for _, name := range paths {
		entry := assembler.entries[name]
		if containsSensitive(GuardText(entry.path, entry.data)) {
			return "", fmt.Errorf("support bundle: refusing to emit %s: unredacted secret/PII detected", entry.path)
		}
		sum := sha256.Sum256(entry.data)
		manifest.Sections = append(manifest.Sections, Section{
			Path: entry.path, Source: entry.source, Bytes: len(entry.data),
			SHA256: hex.EncodeToString(sum[:]), Redactions: entry.redactions,
		})
		manifest.Redaction.TotalRedactions += entry.redactions
		if entry.redactions > 0 {
			manifest.Redaction.SectionsScrubbed++
		}
	}
	manifestBytes, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return "", fmt.Errorf("support bundle: marshal manifest: %w", err)
	}
	manifestBytes = append(manifestBytes, '\n')
	manifestSum := sha256.Sum256(manifestBytes)

	archiveEntries := make(map[string][]byte, len(assembler.entries)+1)
	for name, entry := range assembler.entries {
		archiveEntries[name] = entry.data
	}
	archiveEntries["manifest.json"] = manifestBytes
	archivePaths := make([]string, 0, len(archiveEntries))
	for name := range archiveEntries {
		archivePaths = append(archivePaths, name)
	}
	sort.Strings(archivePaths)

	outPath = filepath.Clean(outPath)
	dir := filepath.Dir(outPath)
	tmp, err := os.CreateTemp(dir, ".olivares-support-*.tmp")
	if err != nil {
		return "", fmt.Errorf("support bundle: create output: %w", err)
	}
	tmpName := tmp.Name()
	keep := false
	defer func() {
		_ = tmp.Close()
		if !keep {
			_ = os.Remove(tmpName)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		return "", fmt.Errorf("support bundle: protect output: %w", err)
	}

	gz := gzip.NewWriter(tmp)
	gz.Header.ModTime = time.Time{}
	gz.Header.Name = ""
	gz.Header.Comment = ""
	tw := tar.NewWriter(gz)
	for _, name := range archivePaths {
		if _, err := safeJoin(dir, name); err != nil {
			return "", err
		}
		if name != "manifest.json" && !pathAllowed(name) {
			return "", fmt.Errorf("support bundle: path %q is not in the diagnostic allowlist", name)
		}
		if err := writeTarBytes(tw, name, archiveEntries[name]); err != nil {
			return "", fmt.Errorf("support bundle: write %q: %w", name, err)
		}
	}
	if err := tw.Close(); err != nil {
		return "", fmt.Errorf("support bundle: close tar: %w", err)
	}
	if err := gz.Close(); err != nil {
		return "", fmt.Errorf("support bundle: close gzip: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return "", fmt.Errorf("support bundle: sync output: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("support bundle: close output: %w", err)
	}
	if err := os.Rename(tmpName, outPath); err != nil {
		return "", fmt.Errorf("support bundle: install output: %w", err)
	}
	keep = true
	return hex.EncodeToString(manifestSum[:]), nil
}

// GuardText leaves content unchanged except for effective-config values that
// the contract deliberately records verbatim as exact secret references.
// Blinding those reference locators lets the final guard inspect every other
// byte without treating a handle as a secret value.
func GuardText(entryPath string, data []byte) string {
	if entryPath != "config/effective.txt" {
		return string(data)
	}
	text := string(data)
	var out strings.Builder
	for len(text) > 0 {
		line := text
		rest := ""
		if i := strings.IndexByte(text, '\n'); i >= 0 {
			line, rest = text[:i+1], text[i+1:]
		}
		text = rest

		ending := ""
		body := line
		if strings.HasSuffix(body, "\n") {
			body = strings.TrimSuffix(body, "\n")
			ending = "\n"
		}
		if strings.HasSuffix(body, "\r") {
			body = strings.TrimSuffix(body, "\r")
			ending = "\r" + ending
		}
		key, value, found := strings.Cut(body, "=")
		if found && !strings.HasPrefix(strings.TrimSpace(key), "#") &&
			(IsExactSecretReference(value) || isRedactionMarker(value)) {
			out.WriteString(body[:strings.IndexByte(body, '=')+1])
			out.WriteString("x")
			out.WriteString(ending)
			continue
		}
		out.WriteString(line)
	}
	return out.String()
}

func isRedactionMarker(value string) bool {
	v := strings.TrimSpace(value)
	if len(v) >= 2 && ((v[0] == '"' && v[len(v)-1] == '"') || (v[0] == '\'' && v[len(v)-1] == '\'')) {
		v = strings.TrimSpace(v[1 : len(v)-1])
	}
	v = strings.ToLower(v)
	return v == "<redacted>" || v == "[redacted]" ||
		strings.HasPrefix(v, "[redacted:") && strings.HasSuffix(v, "]")
}

// IsExactSecretReference reports whether value is one closed-grammar
// <scheme>:<locator> reference with no surrounding whitespace or inline
// credentials.
func IsExactSecretReference(value string) bool {
	v := value
	if len(v) >= 2 && ((v[0] == '"' && v[len(v)-1] == '"') || (v[0] == '\'' && v[len(v)-1] == '\'')) {
		v = v[1 : len(v)-1]
	}
	if v == "" || strings.IndexFunc(v, unicode.IsSpace) >= 0 || secret.ContainsInlineCredential(v) {
		return false
	}
	scheme, locator, found := strings.Cut(v, ":")
	if !found || locator == "" || !secret.IsReference(v) {
		return false
	}
	if strings.EqualFold(scheme, secret.SchemeEnv) {
		return isPOSIXEnvironmentName(locator)
	}
	return true
}

func isPOSIXEnvironmentName(name string) bool {
	if name == "" || !isASCIIAlpha(name[0]) && name[0] != '_' {
		return false
	}
	for i := 1; i < len(name); i++ {
		if !isASCIIAlpha(name[i]) && (name[i] < '0' || name[i] > '9') && name[i] != '_' {
			return false
		}
	}
	return true
}

func isASCIIAlpha(c byte) bool {
	return c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z'
}

func writeTarBytes(tw *tar.Writer, name string, data []byte) error {
	if tw == nil {
		return fmt.Errorf("nil tar writer")
	}
	if _, err := cleanPath(name); err != nil {
		return err
	}
	if err := tw.WriteHeader(&tar.Header{
		Name: name, Mode: 0o600, Size: int64(len(data)), Typeflag: tar.TypeReg,
		ModTime: time.Time{}, AccessTime: time.Time{}, ChangeTime: time.Time{}, Format: tar.FormatUSTAR,
	}); err != nil {
		return err
	}
	_, err := tw.Write(data)
	return err
}

func cleanPath(name string) (string, error) {
	if name == "" || strings.ContainsRune(name, '\x00') {
		return "", fmt.Errorf("support bundle: unsafe archive path %q", name)
	}
	normalized := strings.ReplaceAll(name, `\`, "/")
	if strings.HasPrefix(normalized, "/") {
		return "", fmt.Errorf("support bundle: unsafe archive path %q", name)
	}
	clean := path.Clean(normalized)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || clean != normalized {
		return "", fmt.Errorf("support bundle: unsafe archive path %q", name)
	}
	return clean, nil
}

func safeJoin(root, name string) (string, error) {
	clean, err := cleanPath(name)
	if err != nil {
		return "", err
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("support bundle: resolve archive root: %w", err)
	}
	joined := filepath.Join(absRoot, filepath.FromSlash(clean))
	if joined == absRoot || !strings.HasPrefix(joined, absRoot+string(os.PathSeparator)) {
		return "", fmt.Errorf("support bundle: archive path %q escapes its root", name)
	}
	return joined, nil
}

func pathAllowed(name string) bool {
	switch name {
	case "config/effective.txt", "status/status.json", "logs/engine.log",
		"manifests/schema.json", "secrets/inventory.txt":
		return true
	}
	if strings.HasPrefix(name, "manifests/dr-") && strings.HasSuffix(name, ".json") {
		return true
	}
	return strings.HasPrefix(name, "verify/") && strings.HasSuffix(name, ".json") && path.Base(name) != ".json"
}

// ReadInput reads at most limit bytes from one diagnostic source.
func ReadInput(r io.Reader, limit int64) ([]byte, error) {
	if r == nil {
		return nil, fmt.Errorf("support bundle: nil input")
	}
	limited := io.LimitReader(r, limit+1)
	b, err := io.ReadAll(limited)
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("support bundle: diagnostic input exceeds %d bytes", limit)
	}
	return b, nil
}
