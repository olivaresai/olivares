// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"fmt"
	"io"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/olivaresai/olivares/core/secret"
	coresupport "github.com/olivaresai/olivares/core/supportbundle"
	securitymodule "github.com/olivaresai/olivares/modules/security"
)

// supportPublicConfigKeys is deliberately deny-by-default: a new key is not
// public until it is reviewed and added here, so unknown values are redacted
// instead of leaked. These exact names cover the non-secret installPlan/serve
// surface and its common env-file spellings; credential-bearing keys, DSNs,
// license material and the free-form OLIVARES_EXTRA_ARGS escape hatch are
// intentionally absent.
var supportPublicConfigKeys = map[string]struct{}{
	"PROFILE":                           {},
	"OLIVARES_PROFILE":                  {},
	"LOG_LEVEL":                         {},
	"OLIVARES_LOG_LEVEL":                {},
	"LISTEN":                            {},
	"OLIVARES_LISTEN":                   {},
	"GRPC_LISTEN":                       {},
	"OLIVARES_GRPC_LISTEN":              {},
	"HOST":                              {},
	"OLIVARES_HOST":                     {},
	"HTTP_HOST":                         {},
	"OLIVARES_HTTP_HOST":                {},
	"GRPC_HOST":                         {},
	"OLIVARES_GRPC_HOST":                {},
	"BIND_ADDR":                         {},
	"OLIVARES_BIND_ADDR":                {},
	"HTTP_PORT":                         {},
	"OLIVARES_HTTP_PORT":                {},
	"GRPC_PORT":                         {},
	"OLIVARES_GRPC_PORT":                {},
	"DATA_DIR":                          {},
	"OLIVARES_DATA_DIR":                 {},
	"ENGINE":                            {},
	"OLIVARES_ENGINE":                   {},
	"GRPC_CLIENT_CA":                    {},
	"OLIVARES_GRPC_CLIENT_CA":           {},
	"TIMEOUT":                           {},
	"OLIVARES_TIMEOUT":                  {},
	"CHECKPOINT_INTERVAL":               {},
	"OLIVARES_CHECKPOINT_INTERVAL":      {},
	"INSECURE":                          {},
	"OLIVARES_INSECURE":                 {},
	"ALLOW_PRIVILEGED_DB_ROLE":          {},
	"OLIVARES_ALLOW_PRIVILEGED_DB_ROLE": {},
	"REGION":                            {},
	"OLIVARES_REGION":                   {},
	"KNOWN_REGIONS":                     {},
	"OLIVARES_KNOWN_REGIONS":            {},
	"EDITION":                           {},
	"OLIVARES_EDITION":                  {},
	"MODEL_ID":                          {},
	"OLIVARES_MODEL_ID":                 {},
	"EMBEDDINGS_MODEL":                  {},
	"OLIVARES_EMBEDDINGS_MODEL":         {},
	"TENANT":                            {},
	"OLIVARES_TENANT":                   {},
	"TENANT_NAME":                       {},
	"OLIVARES_TENANT_NAME":              {},
	"INSTANCE_NAME":                     {},
	"OLIVARES_INSTANCE_NAME":            {},
	"BASE_URL":                          {},
	"OLIVARES_BASE_URL":                 {},
	"SERVER_URL":                        {},
	"OLIVARES_SERVER_URL":               {},
	"DB_MAX_CONNS":                      {},
	"OLIVARES_DB_MAX_CONNS":             {},
	"MAX_CONNS":                         {},
	"OLIVARES_MAX_CONNS":                {},
	"MAX_CONNECTIONS":                   {},
	"OLIVARES_MAX_CONNECTIONS":          {},
	"CONTEXT_MAX_TOKENS":                {},
	"OLIVARES_CONTEXT_MAX_TOKENS":       {},
	"VECTOR_DIM":                        {},
	"OLIVARES_VECTOR_DIM":               {},
}

type supportBundleManifest = coresupport.Manifest

// supportBundleEntry carries what supportBundleGuardText needs, and only that: the two
// fields it does not read (source, redactions) were left behind when the assembler moved
// to core/support, which owns them now.
type supportBundleEntry struct {
	path string
	data []byte
}

// supportBundleAssembler accepts only the diagnostic paths defined by the
// support-bundle contract. It never walks a directory or copies an arbitrary
// source file into the archive.
type supportBundleAssembler struct {
	shared *coresupport.Assembler
}

func newSupportBundleAssembler() *supportBundleAssembler {
	return &supportBundleAssembler{shared: coresupport.NewAssembler()}
}

func (a *supportBundleAssembler) add(name, source string, data []byte, redactions int) error {
	if a == nil {
		return fmt.Errorf("support bundle: nil assembler")
	}
	return a.shared.Add(name, source, data, redactions)
}

// redactEffectiveConfig is fail-closed: it always preserves a parsed key name,
// but preserves its value only for an exact secret reference or an explicitly
// public key. Unknown values and inline credentials are replaced structurally.
// Comments and non-assignment text still pass through the shared free-text
// catalog so known secret and PII shapes cannot escape via those channels.
func redactEffectiveConfig(in string) (string, int) {
	canonical, total := redactConfigTextPreservingReferences(in)
	var out strings.Builder
	for len(canonical) > 0 {
		line := canonical
		rest := ""
		if i := strings.IndexByte(canonical, '\n'); i >= 0 {
			line, rest = canonical[:i+1], canonical[i+1:]
		}
		canonical = rest

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
		if found && !strings.HasPrefix(strings.TrimSpace(key), "#") {
			if supportConfigValueMayBeShown(key, value) {
				out.WriteString(body)
				out.WriteString(ending)
				continue
			}
			out.WriteString(body[:strings.IndexByte(body, '=')+1])
			out.WriteString("[REDACTED]")
			out.WriteString(ending)
			if !supportValueAlreadyCounted(value) {
				total++
			}
			continue
		}

		out.WriteString(body)
		out.WriteString(ending)
	}
	return out.String(), total
}

// redactConfigTextPreservingReferences applies the canonical redactor to the
// whole config so a PEM block can span lines. Assignment keys are temporarily
// masked because key names are diagnostic structure and must always survive;
// reviewed public values and exact references are masked too because the
// contract records them verbatim. redactEffectiveConfig then structurally
// redacts every other assignment, including opaque values the catalog cannot
// recognize.
func redactConfigTextPreservingReferences(in string) (string, int) {
	type restoration struct {
		placeholder string
		original    string
	}
	prefix := "\x00OLIVARES_SUPPORT_CONFIG\x1f"
	for strings.Contains(in, prefix) {
		prefix += "\x1f"
	}
	placeholder := func(kind byte, index int) string {
		return fmt.Sprintf("%s%c%d%s", prefix, kind, index, prefix)
	}

	var masked strings.Builder
	restores := make([]restoration, 0)
	remaining := in
	assignment := 0

	for len(remaining) > 0 {
		line := remaining
		rest := ""
		if i := strings.IndexByte(remaining, '\n'); i >= 0 {
			line, rest = remaining[:i+1], remaining[i+1:]
		}
		remaining = rest

		body := strings.TrimSuffix(line, "\n")
		body = strings.TrimSuffix(body, "\r")
		key, value, found := strings.Cut(body, "=")
		if !found || strings.HasPrefix(strings.TrimSpace(key), "#") {
			masked.WriteString(line)
			continue
		}

		keyPlaceholder := placeholder('K', assignment)
		restores = append(restores, restoration{placeholder: keyPlaceholder, original: key})
		masked.WriteString(keyPlaceholder)
		masked.WriteByte('=')
		if supportConfigValueMayBeShown(key, value) {
			valuePlaceholder := placeholder('V', assignment)
			restores = append(restores, restoration{placeholder: valuePlaceholder, original: value})
			masked.WriteString(valuePlaceholder)
		} else {
			masked.WriteString(value)
		}
		masked.WriteString(line[len(body):])
		assignment++
	}

	redacted, total := securitymodule.RedactText(masked.String())
	for _, restore := range restores {
		redacted = strings.ReplaceAll(redacted, restore.placeholder, restore.original)
	}
	return redacted, total
}

func supportConfigValueMayBeShown(key, value string) bool {
	if isExactSecretReference(value) {
		return true
	}
	configKey := supportConfigKey(key)
	if secret.IsCredentialBearingConfigKey(configKey) || secret.ContainsInlineCredential(value) {
		return false
	}
	// A public-allowlisted key gates STRUCTURE, not content. An operator can paste a
	// catalog-recognized secret (e.g. an AWS/Google key) into MODEL_ID/BASE_URL, or a
	// URL userinfo credential (https://<token>@host) under a public URL key — neither
	// is a safe literal. Never show a value that carries a catalog shape or a URL
	// authority userinfo, even under a public key.
	if securitymodule.ContainsSecretOrPII(value) || supportValueHasAuthorityUserinfo(value) {
		return false
	}
	_, public := supportPublicConfigKeys[strings.ToUpper(configKey)]
	return public
}

// supportValueHasAuthorityUserinfo reports a non-empty userinfo before '@' in a
// URL authority (scheme://userinfo@host/...). Any userinfo — with or without a
// password colon — is treated as credential material for redaction purposes; a
// bare token@host is a credential too.
func supportValueHasAuthorityUserinfo(value string) bool {
	v := strings.TrimSpace(value)
	i := strings.Index(v, "://")
	if i < 0 {
		return false
	}
	authority := v[i+3:]
	if end := strings.IndexAny(authority, "/?#"); end >= 0 {
		authority = authority[:end]
	}
	return strings.IndexByte(authority, '@') > 0
}

// supportValueAlreadyCounted reports whether the canonical pass over the whole
// config already counted this value, so the structural replacement below must not
// count it a SECOND time. The tally is one redaction event per config value; it is
// what the support bundle prints as "N redactions applied", and a number that
// double-counts one secret is a number nobody can reconcile.
//
// The guard used to be isSupportRedactionMarker alone, which only recognizes a
// value that is EXACTLY a marker. That misses the common case by construction: the
// canonical redactor is submatch-preserving, so it leaves `password=[redacted]`
// and `db://svc:[redacted:url-userinfo]@host/db` — marker PLUS surrounding
// structure, never a bare marker. Both were then counted twice.
//
// Known and accepted asymmetry: a value carrying a recognized secret AND further
// opaque material the structural pass also removes is counted once, not twice.
// One event per value is the rule; over-counting one secret is the error that
// misleads, and under-counting a compound value cannot hide anything, because the
// value is gone either way.
func supportValueAlreadyCounted(value string) bool {
	if isSupportRedactionMarker(value) {
		return true
	}
	lower := strings.ToLower(value)
	return strings.Contains(lower, "[redacted]") || strings.Contains(lower, "[redacted:")
}

func isSupportRedactionMarker(value string) bool {
	v := strings.TrimSpace(value)
	if len(v) >= 2 && ((v[0] == '"' && v[len(v)-1] == '"') || (v[0] == '\'' && v[len(v)-1] == '\'')) {
		v = strings.TrimSpace(v[1 : len(v)-1])
	}
	v = strings.ToLower(v)
	return v == "[redacted]" || (strings.HasPrefix(v, "[redacted:") && strings.HasSuffix(v, "]"))
}

func supportConfigKey(key string) string {
	k := strings.TrimSpace(key)
	rest, found := strings.CutPrefix(k, "export")
	if !found || rest == "" {
		return k
	}
	// Only the first rune after "export" decides: a space means this is the
	// `export FOO=...` form and the name is what follows it; anything else means
	// "export" was a prefix of a longer word and the key is untouched. This used to
	// be a `for range` that returned on its first iteration, which read as a scan.
	first, _ := utf8.DecodeRuneInString(rest)
	if !unicode.IsSpace(first) {
		return k
	}
	return strings.TrimLeftFunc(rest, unicode.IsSpace)
}

func isExactSecretReference(value string) bool {
	return coresupport.IsExactSecretReference(value)
}

func redactSupportText(in []byte) ([]byte, int) {
	out, redactions := securitymodule.RedactText(string(in))
	return []byte(out), redactions
}

// redactSupportLines first applies the canonical redactor to the whole log so
// multi-line credential shapes are collapsed, then preserves the existing
// per-line pass for every remaining shape and the original newline form.
func redactSupportLines(in []byte) ([]byte, int) {
	text, _ := securitymodule.RedactText(string(in))
	var out strings.Builder
	total := 0
	for len(text) > 0 {
		line := text
		rest := ""
		if i := strings.IndexByte(text, '\n'); i >= 0 {
			line, rest = text[:i+1], text[i+1:]
		}
		text = rest
		redacted, count := securitymodule.RedactText(line)
		out.WriteString(redacted)
		total += count
	}
	return []byte(out.String()), total
}

// writeSupportBundle writes an atomic 0600 tar.gz with deterministic entry
// ordering and metadata. The returned digest covers the exact manifest.json
// bytes stored in the archive.
func writeSupportBundle(outPath, olivaresVersion string, createdAt time.Time, assembler *supportBundleAssembler) (string, error) {
	if assembler == nil {
		return "", fmt.Errorf("support bundle: nil assembler")
	}
	return coresupport.Write(
		outPath,
		olivaresVersion,
		createdAt,
		assembler.shared,
		securitymodule.ContainsSecretOrPII,
	)
}

// supportBundleGuardText leaves content unchanged except for effective-config
// values that the fail-closed policy deliberately records verbatim: exact secret
// references and reviewed public settings. Replacing those values with a short
// non-matching token lets the shared catalog inspect every other byte without
// rejecting an intentional public host, URL or identifier as generic PII.
func supportBundleGuardText(entry supportBundleEntry) string {
	return coresupport.GuardText(entry.path, entry.data)
}

func readSupportInput(r io.Reader, limit int64) ([]byte, error) {
	return coresupport.ReadInput(r, limit)
}
