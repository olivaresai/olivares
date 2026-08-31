// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mcpb

import (
	"archive/zip"
	"bytes"
	"crypto/x509"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/connectors/internal/textscan"
	"github.com/olivaresai/olivares/sdk/model"
)

// maxManifestBytes caps a manifest.json read (a real manifest is small).
const maxManifestBytes = 1 << 20 // 1 MiB

// maxBundleBytes caps how much of a .mcpb is loaded into memory to verify it. A
// desktop extension is small (the reference signer node-forge reads the whole
// file at once); a bundle beyond this is inventoried honestly as not-verified
// rather than loaded wholesale.
const maxBundleBytes = 64 << 20 // 64 MiB

// Signature block markers (modelcontextprotocol/mcpb src/node/sign.ts: the bundle
// is `[zip] MCPB_SIG_V1 [4-byte LE length] [DER PKCS#7] MCPB_SIG_END`).
const (
	sigMarkerStart = "MCPB_SIG_V1"
	sigMarkerEnd   = "MCPB_SIG_END"
)

// signatureState is the honest state of a bundle's PKCS#7 signature check.
type signatureState int

const (
	// sigNotApplicable: an unpacked install — the bundle signature is no longer
	// attached, so nothing can be checked post-install.
	sigNotApplicable signatureState = iota
	// sigAbsent: a bundle file without the MCPB signature block.
	sigAbsent
	// sigUnverified: a bundle carries an MCPB signature block, but it was NOT
	// cryptographically verified — no trusted_roots are configured to anchor the
	// signer. Deny-closed: a present signature is never reported valid unverified
	// (any self-signed cert would otherwise forge publisher identity).
	sigUnverified
	// sigValid: the detached PKCS#7 SignedData verified AND the signer chains to a
	// configured trusted root — the content is bound and the publisher anchored.
	sigValid
	// sigInvalid: a signature block is present but verification FAILED (corrupt
	// DER, a signature that does not verify, content modified after signing, or a
	// signer that does not chain to a trusted root). Distinct from absent.
	sigInvalid
)

// manifest is the parsed-and-minimized view of a manifest.json: only the
// governance-relevant fields. The spec's version key has churned across the
// format's life (dxt_version → mcpb_version → manifest_version); a robust
// inventory accepts all three (VERIFIED 2026-06-10).
type manifest struct {
	ManifestVersion string `json:"manifest_version"`
	MCPBVersion     string `json:"mcpb_version"`
	DXTVersion      string `json:"dxt_version"`
	Name            string `json:"name"`
	DisplayName     string `json:"display_name"`
	Version         string `json:"version"`
	Description     string `json:"description"`
	LongDescription string `json:"long_description"`
	Server          struct {
		Type       string `json:"type"`
		EntryPoint string `json:"entry_point"`
	} `json:"server"`
	Tools []struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	} `json:"tools"`
	ToolsGenerated bool `json:"tools_generated"`
	Prompts        []struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Text        string `json:"text"`
	} `json:"prompts"`
	// raw keeps the full manifest bytes for the whole-document secret-shape scan
	// (a token can hide in mcp_config.env or user_config defaults). Never emitted.
	raw []byte
}

// name returns the manifest's allowlist identity, trimmed.
func (m manifest) name() string { return strings.TrimSpace(m.Name) }

// specVersion returns the first present version-key value and which key carried
// it ("" when none of the three spellings is present).
func (m manifest) specVersion() (value, key string) {
	switch {
	case strings.TrimSpace(m.ManifestVersion) != "":
		return m.ManifestVersion, "manifest_version"
	case strings.TrimSpace(m.MCPBVersion) != "":
		return m.MCPBVersion, "mcpb_version"
	case strings.TrimSpace(m.DXTVersion) != "":
		return m.DXTVersion, "dxt_version"
	default:
		return "", ""
	}
}

// observedExtension is one extension seen on a configured surface.
type observedExtension struct {
	manifest manifest
	// dirName/fileName locate the observation for findings when the manifest
	// itself is unusable (sanitized before display).
	dirName  string
	fileName string
	// bundle is true for a .mcpb/.dxt FILE (signature presence is checkable);
	// false for an unpacked install.
	bundle bool
	// legacyDXT is true for a .dxt file (the pre-rename format; still installs).
	legacyDXT bool
	sig       signatureState
	// sigSigner is the signer certificate Common Name for a sigValid/sigInvalid
	// bundle (sanitized before display); sigReason explains a non-valid state.
	sigSigner string
	sigReason string
	// parseErr is true when a manifest.json existed but could not be parsed.
	parseErr bool
}

// ref is the extension's best display identity for findings: the manifest name,
// else the directory/file it was observed at.
func (e observedExtension) ref() string {
	if n := e.manifest.name(); n != "" {
		return n
	}
	if e.dirName != "" {
		return e.dirName
	}
	return e.fileName
}

// observeInstalled reads an unpacked-extensions directory: every subdirectory
// carrying a manifest.json is one installed extension. A missing directory is
// benign (nothing installed); a read fault is returned for the engine to retry.
func observeInstalled(dir string) ([]observedExtension, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []observedExtension
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		path := filepath.Join(dir, e.Name(), "manifest.json")
		raw, rerr := readFileCapped(path, maxManifestBytes)
		if rerr != nil {
			continue // a subdirectory without a readable manifest is not an extension
		}
		ext := observedExtension{dirName: e.Name(), sig: sigNotApplicable}
		var m manifest
		if json.Unmarshal(raw, &m) == nil {
			m.raw = raw
			ext.manifest = m
		} else {
			ext.parseErr = true
		}
		out = append(out, ext)
	}
	return out, nil
}

// observeBundles reads a directory of .mcpb/.dxt bundle files: each is opened
// as a zip (tolerating the appended signature block) and its root
// manifest.json is read. A missing directory is benign. roots, when non-nil,
// turns on cryptographic PKCS#7 verification of each signed bundle.
func observeBundles(dir string, roots *x509.CertPool) ([]observedExtension, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []observedExtension
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		ext := strings.ToLower(filepath.Ext(e.Name()))
		if ext != ".mcpb" && ext != ".dxt" {
			continue
		}
		path := filepath.Join(dir, e.Name())
		obs := observedExtension{fileName: e.Name(), bundle: true, legacyDXT: ext == ".dxt"}
		obs.sig, obs.sigSigner, obs.sigReason, obs.manifest, obs.parseErr = readBundle(path, roots)
		out = append(out, obs)
	}
	return out, nil
}

// readBundle reads a bundle into memory (bounded by maxBundleBytes), classifies
// its signature (absent / present-but-unverified / valid / invalid) — verifying
// the PKCS#7 chain against roots when configured — and reads its root
// manifest.json. A read fault returns parseErr=true; an oversized bundle is
// inventoried honestly as not-verified.
func readBundle(path string, roots *x509.CertPool) (signatureState, string, string, manifest, bool) {
	fh, err := os.Open(path) //nolint:gosec // operator-provided bundle path, read-only
	if err != nil {
		return sigAbsent, "", "", manifest{}, true
	}
	defer func() { _ = fh.Close() }()
	info, err := fh.Stat()
	if err != nil || info.Size() == 0 {
		return sigAbsent, "", "", manifest{}, true
	}
	if info.Size() > maxBundleBytes {
		return sigUnverified, "", fmt.Sprintf("bundle exceeds the %d-byte inspection cap; not loaded for verification", int64(maxBundleBytes)), manifest{}, true
	}
	data := make([]byte, info.Size())
	if _, err := io.ReadFull(fh, data); err != nil {
		return sigAbsent, "", "", manifest{}, true
	}
	return classifyBundle(data, roots)
}

// classifyBundle is readBundle's pure core over the in-memory bundle bytes.
func classifyBundle(data []byte, roots *x509.CertPool) (signatureState, string, string, manifest, bool) {
	markerIdx := bytes.LastIndex(data, []byte(sigMarkerStart))
	hasSig := markerIdx >= 0 && bytes.HasSuffix(bytes.TrimRight(data, "\x00"), []byte(sigMarkerEnd))
	m, parseErr := readManifestFromZip(data, markerIdx, hasSig)
	if !hasSig {
		return sigAbsent, "", "", m, parseErr
	}
	der, ok := extractSignatureDER(data, markerIdx)
	if !ok {
		return sigInvalid, "", "malformed signature block framing (the length prefix overruns the file)", m, parseErr
	}
	v := verifyMCPBSignature(der, data[:markerIdx], len(data)-markerIdx, roots)
	return v.state, v.signer, v.reason, m, parseErr
}

// extractSignatureDER returns the DER PKCS#7 bytes framed after the marker, bound
// by the uint32-LE length prefix (NOT the footer — a signer may pad before it).
func extractSignatureDER(data []byte, markerIdx int) ([]byte, bool) {
	off := markerIdx + len(sigMarkerStart)
	if off+4 > len(data) {
		return nil, false
	}
	n := binary.LittleEndian.Uint32(data[off : off+4])
	off += 4
	if n == 0 || uint64(off)+uint64(n) > uint64(len(data)) {
		return nil, false
	}
	return data[off : off+int(n)], true
}

// readManifestFromZip reads the root manifest.json. The canonical mcpb signer
// bumps the ZIP EOCD comment_length so the WHOLE signed file is a valid zip
// (comment = the signature block); an unsigned bundle or a stage-then-sign layout
// is a valid zip in its bytes-before-marker. Try both and take whichever parses.
func readManifestFromZip(data []byte, markerIdx int, hasSig bool) (manifest, bool) {
	candidates := [][]byte{data}
	if hasSig && markerIdx >= 0 {
		candidates = [][]byte{data[:markerIdx], data}
	}
	for _, c := range candidates {
		if m, ok := readManifestZip(c); ok {
			return m, false
		}
	}
	return manifest{}, true
}

// readManifestZip opens content as a zip and parses its root manifest.json.
func readManifestZip(content []byte) (manifest, bool) {
	zr, err := zip.NewReader(bytes.NewReader(content), int64(len(content)))
	if err != nil {
		return manifest{}, false
	}
	for _, zf := range zr.File {
		if zf.Name != "manifest.json" {
			continue
		}
		rc, oerr := zf.Open()
		if oerr != nil {
			return manifest{}, false
		}
		raw, rerr := io.ReadAll(io.LimitReader(rc, maxManifestBytes))
		_ = rc.Close()
		if rerr != nil {
			return manifest{}, false
		}
		var m manifest
		if json.Unmarshal(raw, &m) != nil {
			return manifest{}, false
		}
		m.raw = raw
		return m, true
	}
	return manifest{}, false
}

// --- posture ------------------------------------------------------------------

// manifestPostureFindings scans one observed extension's manifest text surface —
// the same poisoning vectors as an MCP server catalog: the description fields
// and declared tool/prompt metadata reach the agent, and the raw manifest can
// embed secret-shaped config values.
func manifestPostureFindings(scope string, ext observedExtension, at time.Time) []model.FindingReport {
	ref := textscan.SanitizeDisplay(ext.ref())
	var out []model.FindingReport
	add := func(sev model.Severity, title, key string, asi, llm []string) {
		out = append(out, model.FindingReport{
			Kind:        findingPosture,
			Severity:    sev,
			SubjectKind: subjectExtension,
			SubjectRef:  ref,
			Title:       "desktop extension " + strconv.Quote(ref) + ": " + title,
			DetailHash:  redact.Hash("mcpb-posture scope=" + scope + " ext=" + ref + " " + key),
			OccurredAt:  at,
			OWASPASI:    asi,
			OWASPLLM:    llm,
		})
	}

	if ext.parseErr {
		add(model.SeverityMedium,
			"manifest.json is missing or unparseable — the extension cannot be identified for allowlisting",
			"manifest-unparseable", nil, nil)
		return out
	}
	m := ext.manifest

	if m.name() == "" {
		add(model.SeverityMedium,
			"manifest declares no `name` — the extension cannot be identified for allowlisting",
			"name-missing", nil, nil)
	} else {
		if classes, n := textscan.ScanInvisible(m.Name); n > 0 {
			add(model.SeverityHigh,
				"manifest name contains "+strconv.Itoa(n)+" hidden character(s) ["+strings.Join(classes, ",")+"] — allowlist-identity spoofing",
				"invisible-name classes="+strings.Join(classes, ","), nil, nil)
		}
		if scripts, confusable := textscan.MixedScript(m.Name); confusable {
			add(model.SeverityHigh,
				"manifest name mixes scripts ["+strings.Join(scripts, ",")+"] — homoglyph impersonation of an allowlisted name",
				"homoglyph-name scripts="+strings.Join(scripts, ","), nil, nil)
		}
	}
	if _, key := m.specVersion(); key == "" {
		add(model.SeverityLow,
			"manifest declares no spec version (none of manifest_version/mcpb_version/dxt_version)",
			"no-spec-version", nil, nil)
	} else if key == "dxt_version" {
		add(model.SeverityLow,
			"manifest uses the legacy dxt_version key (pre-rename format)",
			"legacy-dxt-version", nil, nil)
	}

	// Agent-facing free text: descriptions + declared tool/prompt metadata.
	scanText := func(field, text string) {
		text = strings.TrimSpace(text)
		if text == "" {
			return
		}
		for _, id := range textscan.ScanInjection(text) {
			add(textscan.MarkerSeverity(id),
				field+" contains an instruction-injection marker ["+id+"]",
				"injection field="+field+" rule="+id, []string{"ASI01"}, []string{"LLM01:2025"})
		}
		if classes, n := textscan.ScanInvisible(text); n > 0 {
			add(model.SeverityHigh,
				field+" hides "+strconv.Itoa(n)+" invisible character(s) ["+strings.Join(classes, ",")+"] — concealed instruction",
				"invisible field="+field+" classes="+strings.Join(classes, ","), nil, nil)
		}
	}
	scanText("description", m.Description)
	scanText("long_description", m.LongDescription)
	for _, t := range m.Tools {
		scanText("tool "+textscan.SanitizeDisplay(t.Name)+" description", t.Description)
	}
	for _, p := range m.Prompts {
		scanText("prompt "+textscan.SanitizeDisplay(p.Name)+" description", p.Description)
		scanText("prompt "+textscan.SanitizeDisplay(p.Name)+" text", p.Text)
	}

	// Whole-document secret scan: a token hides as easily in mcp_config.env or a
	// user_config default as in a description.
	if redact.ContainsSecret(string(m.raw)) {
		add(model.SeverityHigh,
			"manifest embeds a credential/secret shape — secret exposure",
			"secret-in-manifest", nil, nil)
	}

	if m.ToolsGenerated {
		add(model.SeverityLow,
			"declares tools_generated=true — the tool surface is produced at runtime, not reviewable from the manifest",
			"tools-generated", nil, nil)
	}
	if m.Server.Type == "binary" {
		add(model.SeverityLow,
			"ships a binary server — opaque executable surface (unauditable)",
			"binary-server", nil, nil)
	}
	return out
}

// --- drift (PERMITTED vs OBSERVED) ---------------------------------------------

// driftFindings diffs one observed extension against the authored policy. nil
// policy = observe-only (no drift). Only asserted expectations are checked.
func driftFindings(scope string, expected *Policy, ext observedExtension, at time.Time) []model.FindingReport {
	if expected == nil {
		return nil
	}
	ref := textscan.SanitizeDisplay(ext.ref())
	var out []model.FindingReport
	add := func(sev model.Severity, title, key string) {
		out = append(out, model.FindingReport{
			Kind:        findingDrift,
			Severity:    sev,
			SubjectKind: subjectExtension,
			SubjectRef:  ref,
			Title:       "desktop extension " + strconv.Quote(ref) + ": " + title,
			DetailHash:  redact.Hash("mcpb-drift scope=" + scope + " ext=" + ref + " " + key),
			OccurredAt:  at,
		})
	}

	name := ext.manifest.name()
	if expected.AllowlistEnabled {
		switch entry, ok := allowedEntry(expected.Allowed, name); {
		case name == "":
			// An unidentifiable extension can never match the allowlist —
			// deny-closed: it drifts (the posture finding explains why).
			add(model.SeverityHigh,
				"observed extension has no allowlist identity (unidentifiable manifest) while the org allowlist is enabled",
				"unidentifiable")
		case !ok:
			add(model.SeverityHigh,
				"installed/distributed but NOT on the org Desktop allowlist (the org console would force-remove it; this surface still carries it)",
				"not-allowlisted name="+name)
		case entry.Version != "" && entry.Version != strings.TrimSpace(ext.manifest.Version):
			add(model.SeverityMedium,
				"version "+strconv.Quote(textscan.SanitizeDisplay(ext.manifest.Version))+" drifts from the allowlisted version "+strconv.Quote(entry.Version),
				"version-drift want="+entry.Version+" got="+ext.manifest.Version)
		}
	}

	if ext.legacyDXT {
		add(model.SeverityLow,
			"distributed as a legacy .dxt bundle (pre-rename format; still installable)",
			"legacy-dxt")
	}
	return out
}

// --- signature posture (presence vs validity) ----------------------------------

// signatureFindings reports a bundle's PKCS#7 signature posture along two
// orthogonal axes:
//
//   - PRESENCE is a policy assertion: require_signed mirrors
//     isDesktopExtensionSignatureRequired, so an ABSENT signature under the policy
//     is drift, and a present-but-UNVERIFIED one (no trusted_roots configured) is
//     a flagged config gap — never a silent pass.
//   - VALIDITY is intrinsic to the artifact: a signature that cryptographically
//     verifies AND chains to a configured trusted root is positive evidence
//     (Info); one that does NOT is a HIGH integrity finding — a verifying client
//     would reject it — reported whether or not the policy requires signing.
//
// Unpacked installs carry no attached signature; under require_signed that is
// reported honestly as not-verifiable post-install (the client enforces it at
// install time).
func signatureFindings(scope string, expected *Policy, ext observedExtension, at time.Time) []model.FindingReport {
	requireSigned := expected != nil && expected.RequireSigned
	ref := textscan.SanitizeDisplay(ext.ref())
	var out []model.FindingReport
	add := func(kind string, sev model.Severity, title, key string) {
		out = append(out, model.FindingReport{
			Kind:        kind,
			Severity:    sev,
			SubjectKind: subjectExtension,
			SubjectRef:  ref,
			Title:       "desktop extension " + strconv.Quote(ref) + ": " + title,
			DetailHash:  redact.Hash("mcpb-signature scope=" + scope + " ext=" + ref + " " + key),
			OccurredAt:  at,
		})
	}

	switch ext.sig {
	case sigValid:
		title := "PKCS#7 signature cryptographically verified; the signer chains to a configured trusted root"
		if s := textscan.SanitizeDisplay(ext.sigSigner); s != "" {
			title += " (signer " + strconv.Quote(s) + ")"
		}
		add(findingPosture, model.SeverityInfo, title, "valid signer="+ext.sigSigner)
	case sigInvalid:
		add(findingPosture, model.SeverityHigh,
			"signature block present but PKCS#7 verification FAILED — "+ext.sigReason+" (a verifying client would reject it)",
			"invalid "+ext.sigReason)
	case sigUnverified:
		if requireSigned {
			reason := ext.sigReason
			if reason == "" {
				reason = "no trusted_roots configured"
			}
			add(findingDrift, model.SeverityMedium,
				"policy requires signatures but the PKCS#7 chain is NOT verified ("+reason+"); presence is satisfied, authenticity is not",
				"unverified "+reason)
		}
	case sigAbsent:
		if requireSigned {
			add(findingDrift, model.SeverityHigh,
				"bundle carries NO MCPB signature block while org policy requires signatures (isDesktopExtensionSignatureRequired)",
				"unsigned")
		}
	case sigNotApplicable:
		if requireSigned {
			add(findingDrift, model.SeverityInfo,
				"unpacked install — the bundle signature is not attached post-install, so the require-signed policy is not verifiable here (enforced by the client at install time)",
				"signature-not-verifiable")
		}
	}
	return out
}

// allowedEntry finds the allowlist entry for a manifest name.
func allowedEntry(allowed []AllowedExtension, name string) (AllowedExtension, bool) {
	for _, a := range allowed {
		if a.Name == name {
			return a, true
		}
	}
	return AllowedExtension{}, false
}

// readFileCapped reads up to limit bytes of a file.
func readFileCapped(path string, limit int64) ([]byte, error) {
	fh, err := os.Open(path) //nolint:gosec // operator-provided path, read-only
	if err != nil {
		return nil, err
	}
	defer func() { _ = fh.Close() }()
	return io.ReadAll(io.LimitReader(fh, limit))
}
