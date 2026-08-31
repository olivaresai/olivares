// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"fmt"
	"log/slog"
	"math"
	"regexp"
	"strconv"
)

// This file is the CONSOLE-BOUND redaction seam for engine logs.
//
// WHERE IT SITS, AND WHY THERE. LogBroker.Handle is the single point where a
// slog.Record becomes a LogEntry; both console consumers — the SSE stream
// (/v1/console/logs/stream) and the buffer the viewer seeds itself from
// (/v1/console/logs/buffer), plus the support bundle's ring snapshot — read what
// Handle produced. Putting the scrub there means a NEW log call site inherits it
// by existing, and a new CONSUMER of the ring inherits it too. The alternatives
// were measured and rejected:
//
//   - per call site: 287 today by the narrow shape (…("…", "err", err)), 563 by a
//     wider one, and neither number is the class — no regex can enumerate "a log
//     whose value may carry upstream text". Site 288 would be born unprotected.
//   - the module logger: `debugf` is defined 18 SEPARATE times, once per module,
//     and covers neither core/api's own 18 sites nor cmd/olivares's 112.
//   - the HTTP handlers: two of them today, and the ring would still HOLD the
//     secret at rest.
//
// WHAT IT DOES NOT DO. It does not touch the wrapped handler: b.inner.Handle
// still receives the ORIGINAL record, so the terminal/file sink keeps full
// fidelity. That is deliberate and it is the whole level answer — the split is
// not by log LEVEL but by SURFACE. A reader of the terminal already has host
// access and can read the DSN out of the config file; a reader of the console has
// a bearer token and a browser. Redaction applies to every level the console
// captures, and to nothing the console cannot see.
//
// TWO LAYERS, ON PURPOSE. logRedactFloor below is core-owned and always runs.
// The composition root additionally injects the canonical catalog
// (modules/security.RedactCredentials) via WithLogRedactor, because core must not
// import /modules (scripts/check-boundary.sh) and the detector catalog is
// single-owner in that module. The floor recognizes a secret by its
// STRUCTURAL FRAME — userinfo in a URL authority, key=value, Bearer, PEM, JWT —
// and deliberately owns no vendor prefixes (AKIA…, ghp_…, sk-ant-…); those stay
// with the catalog. The split is not cosmetic: it is what makes each layer
// separately observable, since a vendor-shaped witness survives the floor alone
// and dies once the seam is wired.

// maxLogValueLen bounds one rendered attribute. An upstream error can carry a
// whole HTTP response body, and the ring holds 10 000 entries. Truncation runs
// AFTER redaction, never before: clamping first could cut a secret in half and
// leave the first half in the buffer.
const maxLogValueLen = 4096

// redactionMarker names the RULE that fired and never the value, so a reader —
// and a test — can see that redaction happened and which shape matched.
func redactionMarker(rule string) string { return "[REDACTED:" + rule + "]" }

var (
	// logURLUserinfoRe matches the userinfo of a URL authority
	// (scheme://user:secret@host) and keeps everything around it. This is the
	// shape a database DSN leaks through and the canonical catalog did not cover
	// before cmd/olivares/supportbundle.go:262 had already had to detect it
	// by hand for the config projection.
	logURLUserinfoRe = regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.\-]*://)([^\s/?#@"']+)@`)

	// logKeyValueSecretRe matches a sensitive key and its value, keeping the key
	// (submatch 1) so the reader still sees the STRUCTURE: `password=[REDACTED:…]`
	// says more than a hole. The vocabulary mirrors the canonical
	// modules/security.keyValueSecretRe; that one stays authoritative and runs
	// after this when wired.
	logKeyValueSecretRe = regexp.MustCompile(
		`(?i)((?:api[_-]?key|secret|token|password|passwd|pwd|access[_-]?key|auth|` +
			`client[_-]?secret|cookie|hmac|credential|passphrase|private[_-]?key|` +
			`session[_-]?key|signing[_-]?key)["']?\s*[:=]\s*["']?)([^\s"'&;,)]{4,})`)

	// logBearerRe matches an Authorization bearer value in free text.
	logBearerRe = regexp.MustCompile(`(?i)bearer\s+[0-9A-Za-z._~+/=-]{12,}`)

	// logPEMRe matches a whole PEM private-key block, BEGIN to END.
	logPEMRe = regexp.MustCompile(`(?s)-----BEGIN (?:[A-Z ]+ )?PRIVATE KEY-----.*?-----END (?:[A-Z ]+ )?PRIVATE KEY-----`)

	// logJWTRe matches a three-segment JWT.
	logJWTRe = regexp.MustCompile(`eyJ[0-9A-Za-z_-]{8,}\.[0-9A-Za-z_-]{8,}\.[0-9A-Za-z_-]{8,}`)

	// logCleanMarkerRe matches a value that is EXACTLY a marker, so a later rule
	// can tell "already handled" from "handled, with residue".
	logCleanMarkerRe = regexp.MustCompile(`^\[REDACTED:[a-z0-9-]+\]$`)
)

// logRedactFloorMaxPasses bounds the fixed-point loop. The reason it iterates at
// all is the one modules/security/detect.go paid for and wrote down: the rules
// run in a fixed order and one rule's match can be BLOCKED by a character another
// rule would have removed, so a single pass can leave part of a secret behind.
// Whatever one rule leaves is offered to every rule again.
const logRedactFloorMaxPasses = 4

// logRedactFloor is the core-owned structural credential scrub. It returns the
// scrubbed text and how many markers it emitted.
func logRedactFloor(s string) (string, int) {
	if s == "" {
		return "", 0
	}
	out, n := s, 0
	for i := 0; i < logRedactFloorMaxPasses; i++ {
		next, added := logRedactFloorOnce(out)
		n += added
		if next == out {
			break
		}
		out = next
	}
	return out, n
}

func logRedactFloorOnce(s string) (string, int) {
	out, n := s, 0
	whole := func(re *regexp.Regexp, rule string) {
		out = re.ReplaceAllStringFunc(out, func(string) string {
			n++
			return redactionMarker(rule)
		})
	}

	// PEM first: its body spans newlines and holds characters every other rule
	// would happily chew a prefix off, which would leave the key body in place.
	whole(logPEMRe, "private-key")
	whole(logJWTRe, "jwt")
	whole(logBearerRe, "bearer-token")

	// URL userinfo: keep the scheme and the host. The host is WHERE the failure
	// happened and the operator needs it; the userinfo is the credential.
	out = logURLUserinfoRe.ReplaceAllStringFunc(out, func(m string) string {
		sub := logURLUserinfoRe.FindStringSubmatch(m)
		if len(sub) != 3 || logCleanMarkerRe.MatchString(sub[2]) {
			return m
		}
		n++
		return sub[1] + redactionMarker("url-userinfo") + "@"
	})

	// key=value last of the shapes, and it skips a value that is ALREADY exactly a
	// marker: re-redacting one would relabel a precisely identified secret as a
	// generic one and count it twice.
	out = logKeyValueSecretRe.ReplaceAllStringFunc(out, func(m string) string {
		sub := logKeyValueSecretRe.FindStringSubmatch(m)
		if len(sub) != 3 || logCleanMarkerRe.MatchString(sub[2]) {
			return m
		}
		n++
		return sub[1] + redactionMarker("key-value-secret")
	})

	return out, n
}

// redactLogText runs the core floor and then, when the composition root wired it,
// the canonical catalog. Composition rather than selection is deliberate: a
// missing wire can only ever REDUCE coverage to the floor, never to nothing.
func (b *LogBroker) redactLogText(s string) string {
	out, _ := logRedactFloor(s)
	if b.redactor != nil {
		out, _ = b.redactor(out)
	}
	return out
}

// scrubLogValue turns one slog attribute value into something that is (a) JSON
// safe, (b) readable by a human, and (c) redacted.
//
// (b) is not a nicety, it is half the defect. Handle used to store the live Go
// value and let encoding/json decide, and json.Marshal drops UNEXPORTED fields:
// errors.New and fmt.Errorf produce exactly such types, so the operator was
// handed `"err":{}` — a log line with the diagnosis removed. Meanwhile types with
// exported fields (*url.Error, *net.OpError, *os.PathError) marshaled in full,
// secrets included. Rendering to text fixes the mute half; on its own it would
// have made the leaking half universal, which is why the redaction is in the same
// function and not in a follow-up.
//
// Scalars keep their JSON type — they carry no free text and the console can
// format them. Everything else becomes text, because a value whose shape we
// cannot predict is a value we cannot scrub any other way.
func (b *LogBroker) scrubLogValue(v slog.Value) any {
	v = v.Resolve() // a LogValuer decides what it wants logged; ask it first.
	switch v.Kind() {
	case slog.KindString:
		return b.clampRedacted(v.String())
	case slog.KindBool:
		return v.Bool()
	case slog.KindInt64:
		return v.Int64()
	case slog.KindUint64:
		return v.Uint64()
	case slog.KindFloat64:
		// NaN and ±Inf are legitimate telemetry and encoding/json REFUSES them
		// (encode.go: "unsupported value"). The two console consumers fail
		// differently and both badly: the SSE handler drops the frame on a marshal
		// error (log_handler.go), so the entry silently never arrives, and the
		// buffer handler has already written 200 by then. A log surface must not
		// lose a record because a float was not finite, so a non-finite float
		// becomes its text.
		f := v.Float64()
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return strconv.FormatFloat(f, 'g', -1, 64)
		}
		return f
	case slog.KindTime, slog.KindDuration:
		// Both already marshal to a sane JSON scalar and neither can carry text.
		return v.Any()
	case slog.KindGroup:
		out := make(map[string]any, len(v.Group()))
		for _, a := range v.Group() {
			out[b.scrubLogKey(a.Key)] = b.scrubLogValue(a.Value)
		}
		return out
	default: // slog.KindAny
		return b.clampRedacted(renderAny(v.Any()))
	}
}

// scrubLogKey scrubs an ATTRIBUTE NAME. Keys are usually literals, which is
// exactly why they are easy to forget: nothing in slog stops a caller from
// building one, and an unscrubbed key reaches the console rendered next to its
// value (web/src/features/logs/log-stream.tsx) with neither redaction nor the
// length bound the values get. A seam that scrubs "everything console-bound"
// cannot have an exception it never states.
//
// A collision after redaction (two distinct keys scrubbing to the same marker)
// keeps the last value, which is the same thing slog's own map-shaped handlers do
// with duplicate keys — and losing one attribute of a line is a smaller harm than
// publishing the secret that made them collide.
func (b *LogBroker) scrubLogKey(key string) string {
	return b.clampRedacted(key)
}

// scrubLogModule scrubs the MODULE label. WithGroup turns its argument into the
// module name verbatim, so this is not a hypothetical field: it is a caller-chosen
// string that the console prints on every row.
func (b *LogBroker) scrubLogModule(module string) string {
	if module == "" {
		return ""
	}
	return b.clampRedacted(module)
}

// renderAny produces the text of an arbitrary logged value, preferring the value's
// own account of itself. An error is asked for Error() BEFORE anything else: it is
// the case this whole seam exists for, and it is the one encoding/json got wrong.
// It runs the value's own method behind a recover. That guard is not defensive
// decoration: before nothing here CALLED Error() or String(), so a typed-nil
// pointer with a value receiver — the classic `var e *MyErr; log("x", "err", e)` —
// or any method that panics on a half-built value would now take down the
// goroutine that logged, from inside a log handler. A logging call must not be
// able to kill its caller, and a panic message is still a better diagnosis than a
// dead process.
func renderAny(a any) (out string) {
	defer func() {
		if r := recover(); r != nil {
			out = fmt.Sprintf("<unrenderable %T: panicked in its own String/Error method: %v>", a, r)
		}
	}()
	switch t := a.(type) {
	case nil:
		return "<nil>"
	case error:
		return t.Error()
	case string:
		return t
	case fmt.Stringer:
		return t.String()
	case []byte:
		return string(t)
	default:
		return fmt.Sprintf("%+v", a)
	}
}

// clampRedacted redacts and THEN bounds the length. The order is the point:
// clamping first can cut a secret in half and leave the head of it in the ring.
func (b *LogBroker) clampRedacted(s string) string {
	out := b.redactLogText(s)
	if len(out) <= maxLogValueLen {
		return out
	}
	dropped := len(out) - maxLogValueLen
	return out[:maxLogValueLen] + "…[truncated " + strconv.Itoa(dropped) + " bytes]"
}

// redactLogMessage scrubs the record's message. A message is usually a literal,
// but not always — a fmt.Sprintf'd message carries exactly the same upstream text
// an attribute would, and leaving it out would be a hole shaped like a habit.
//
// There is no "cheap prefilter" here on purpose. The obvious one — skip a message
// with no `:`, `=` or `@` — is WRONG: a PEM block, a JWT, a `bearer …` value and
// every vendor-prefixed key in the canonical catalog (AKIA…, ghp_…, sk-ant-…)
// contain none of those bytes. A prefilter that admits a hole is worse than the
// scan it saves, because nothing turns red when it is wrong.
func (b *LogBroker) redactLogMessage(msg string) string {
	return b.clampRedacted(msg)
}
