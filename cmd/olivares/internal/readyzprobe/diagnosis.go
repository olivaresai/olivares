// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package readyzprobe

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
)

// maxDiagnosticBody bounds the readiness body this probe will read. The answers
// it recognizes are small fixed JSON objects; anything larger is not that
// contract, so it is neither buffered further nor interpreted. The extra byte
// read past the bound is how an oversized body is detected rather than silently
// truncated into something that parses.
const maxDiagnosticBody = 8 << 10

// readinessFacts are the only two fields this probe reads out of a readiness
// body. They are compared against the table below and never printed, so a
// response cannot choose a word the operator reads.
type readinessFacts struct {
	status string
	code   string
}

// diagnostic is the fixed local sentence pair for one recognized state.
type diagnostic struct {
	diagnosis string
	remedy    string
}

// readinessDiagnostics maps the <status, code> pairs core/api's /readyz handler
// emits for a first-boot problem onto sentences that live in this binary. Only
// these three are recognized: an installation blocked on cross-tenant admin
// enumeration, a failed setup-capability probe, and an unreadable setup state.
// Every other answer — including a future one — keeps the bare status verdict,
// because inventing a remedy for a state this build does not understand is how
// an operator is sent to repair the wrong thing.
var readinessDiagnostics = map[readinessFacts]diagnostic{
	{status: "setup_blocked", code: "cross_tenant_admin_pool_not_configured"}: {
		diagnosis: "the engine is running but first-boot setup is blocked: this deployment has no " +
			"cross-tenant admin database pool, so it cannot enumerate organizations authoritatively.",
		remedy: "Provision the NOSUPERUSER BYPASSRLS admin role described in deploy/postgres/README.md " +
			"and restart the engine with --admin-dsn pointing at that role. Give db init the database, " +
			"app role and owner role this deployment already uses: it runs ALTER DATABASE ... OWNER TO, " +
			"so a short form can hand ownership to the wrong role.",
	},
	{status: "setup_unavailable", code: "setup_probe_unavailable"}: {
		diagnosis: "the engine is running and first-boot setup is still pending, but its probe of " +
			"the capability that setup needs failed, so readiness is withheld.",
		remedy: "Read the engine log for the failed capability probe and confirm the store is reachable " +
			"with the configured credentials. Readiness recovers on its own once the probe succeeds.",
	},
	{status: "setup_unavailable", code: "setup_state_unavailable"}: {
		diagnosis: "the engine is running and holds the writer lease but cannot read whether first-boot " +
			"setup is complete, so it withholds readiness rather than guessing.",
		remedy: "Read the engine log for the failed setup-state read and confirm the store is reachable. " +
			"Readiness recovers on its own once the state can be read.",
	},
}

// diagnose returns the fixed local sentence pair for a recognized not-ready
// readiness body, or two empty strings. It is the only place a response body is
// touched, and it copies nothing out of it: an unrecognized, duplicated,
// malformed, trailing, oversized or truncated body is simply not a match.
func diagnose(body io.Reader) (diagnosis, remedy string) {
	raw, ok := readBounded(body)
	if !ok {
		return "", ""
	}
	facts, ok := parseReadiness(raw)
	if !ok {
		return "", ""
	}
	hint, ok := readinessDiagnostics[facts]
	if !ok {
		return "", ""
	}
	return hint.diagnosis, hint.remedy
}

// readBounded reads at most maxDiagnosticBody bytes. It reports failure for a
// body that exceeds the bound and for one that ends early or errors mid-read,
// so a truncated answer is never interpreted as a complete one.
func readBounded(body io.Reader) ([]byte, bool) {
	raw, err := io.ReadAll(io.LimitReader(body, maxDiagnosticBody+1))
	if err != nil || len(raw) > maxDiagnosticBody {
		return nil, false
	}
	return raw, true
}

// parseReadiness reads the two recognized fields out of one JSON object using
// the standard library only, and refuses anything ambiguous. It walks tokens
// rather than decoding into a struct because encoding/json resolves a duplicate
// key by last-one-wins: a body carrying two "code" fields would otherwise be
// read as whichever one came last, which is a decision no sender made.
// Unrelated fields are skipped so a future field cannot break a live install.
func parseReadiness(raw []byte) (readinessFacts, bool) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return readinessFacts{}, false
	}
	var facts readinessFacts
	seen := make(map[string]struct{}, 4)
	for dec.More() {
		keyToken, err := dec.Token()
		if err != nil {
			return readinessFacts{}, false
		}
		key, isString := keyToken.(string)
		if !isString {
			return readinessFacts{}, false
		}
		if _, duplicate := seen[key]; duplicate {
			return readinessFacts{}, false
		}
		seen[key] = struct{}{}
		switch key {
		case "status", "code":
			valueToken, verr := dec.Token()
			if verr != nil {
				return readinessFacts{}, false
			}
			// A non-string here is not just the wrong type. Token() returns an
			// opening brace or bracket WITHOUT consuming the value behind it, so
			// accepting one would leave the walk reading a nested document's keys
			// as if they were this object's. Refusing keeps the two apart.
			value, isString := valueToken.(string)
			if !isString {
				return readinessFacts{}, false
			}
			if key == "status" {
				facts.status = value
			} else {
				facts.code = value
			}
		default:
			var ignored json.RawMessage
			if derr := dec.Decode(&ignored); derr != nil {
				return readinessFacts{}, false
			}
		}
	}
	if tok, err := dec.Token(); err != nil || tok != json.Delim('}') {
		return readinessFacts{}, false
	}
	// A second document, or any trailing byte that is not whitespace, means this
	// is not the single object the contract describes.
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return readinessFacts{}, false
	}
	return facts, true
}
