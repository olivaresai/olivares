// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strings"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

// VAULT SECRETS AS A SESSION'S ENVIRONMENT.
//
// A launch (or its template) names secrets of the tenant vault by NAME, each with
// the environment variable the child reads it from. The values are opened at
// launch and at every resume, and they travel in the child's environment only:
// never in argv, the folder, the run row, the API, the ledger or a log line, and
// the output the plane stores and streams has them replaced by the secret's name.
//
// Who may: only a principal who may manage the tenant's secrets (tenant:admin,
// the permission the vault's tenant scope already requires) may give them to a
// session. The names are stored on the run row, so a resume uses exactly what the
// run was given; it re-reads the CURRENT value and asks the same question again.

// secretEnvPrefix is the vault namespace a session may read. The tenant scope also
// holds MCP credential handles (mcp/) and provider records (an internal design note (not shipped));
// neither can be named here.
const secretEnvPrefix = "env/"

// minSecretValue is the shortest value the output can be relied on to withhold:
// replacing every occurrence of a shorter one would also rewrite ordinary text.
const minSecretValue = 8

// maxSecretEnv bounds one launch; the child's whole explicit environment is
// bounded at 128 values (child_env.go validateExplicitEnv).
const maxSecretEnv = 32

// SecretEnvRef names one vault secret a session receives as an environment variable.
type SecretEnvRef struct {
	Env    string `json:"env"`
	Secret string `json:"secret"`
}

// validateSecretEnv checks the names (and only the names) of a launch's secrets.
func validateSecretEnv(refs []SecretEnvRef, envAllow []string) error {
	if len(refs) > maxSecretEnv {
		return badRequest(fmt.Sprintf("secret_env names more than %d secrets", maxSecretEnv))
	}
	allowed := make(map[string]bool, len(envAllow))
	for _, name := range envAllow {
		allowed[name] = true
	}
	seen := make(map[string]bool, len(refs))
	for i := range refs {
		env, secret := strings.TrimSpace(refs[i].Env), strings.TrimSpace(refs[i].Secret)
		if !validEnvName(env) || reservedSecretEnvName(env) {
			return badRequest(fmt.Sprintf("secret_env: %q is not a variable a secret may set", env))
		}
		if seen[env] {
			return badRequest(fmt.Sprintf("secret_env: %s is named twice", env))
		}
		if allowed[env] {
			return badRequest(fmt.Sprintf("secret_env: %s is also in env_allow; name it once", env))
		}
		if !strings.HasPrefix(secret, secretEnvPrefix) || len(secret) == len(secretEnvPrefix) ||
			auth.ValidateSecretName(secret) != "" {
			return badRequest(fmt.Sprintf("secret_env: %q is not a session secret (names begin with %s)", secret, secretEnvPrefix))
		}
		seen[env] = true
		refs[i] = SecretEnvRef{Env: env, Secret: secret}
	}
	return nil
}

// refuseSecretEnvFor answers a launch or a resume that names secrets by a caller
// who may not use them.
func refuseSecretEnvFor(refs []SecretEnvRef, mayUse bool) error {
	if len(refs) > 0 && !mayUse {
		return forbiddenErr("giving vault secrets to a session needs tenant administration")
	}
	return nil
}

// secretEnvWayOut ends each refusal of a named secret, at launch and on resume.
// Session secrets live in the tenant's store, which the console opens from the
// start form (the deployment-wide Secrets page is another store), and a stopped
// session's secrets cannot be changed, so the other way out is a new session.
const secretEnvWayOut = " in New session > More options > Manage session secrets, or start a new session without it"

// resolveSecretEnv opens every named secret for ONE launch. The values are held by
// the caller for the spawn and never persisted, logged or returned.
func (m *Module) resolveSecretEnv(ctx context.Context, tenant model.TenantID, refs []SecretEnvRef) ([]EnvVar, error) {
	if len(refs) == 0 {
		return nil, nil
	}
	if m.rt.ProviderVault == nil {
		return nil, &runErr{http.StatusServiceUnavailable,
			"this session names vault secrets, and no sealed vault is wired on this node to open them (the launch is denied)"}
	}
	out := make([]EnvVar, 0, len(refs))
	for _, ref := range refs {
		value, err := m.rt.ProviderVault.Open(ctx, tenant, ref.Secret)
		switch {
		case errors.Is(err, auth.ErrSecretNotFound):
			return nil, conflictErr(fmt.Sprintf(
				"the vault has no session secret named %s; add it"+secretEnvWayOut, ref.Secret))
		case err != nil:
			return nil, errors.Join(&runErr{http.StatusServiceUnavailable, fmt.Sprintf(
				"the vault could not open %s on this node; the launch is denied", ref.Secret)}, err)
		}
		if len(value) < minSecretValue {
			return nil, conflictErr(fmt.Sprintf(
				"%s is shorter than %d characters, too short to withhold from the session output; "+
					"store a longer value"+secretEnvWayOut, ref.Secret, minSecretValue))
		}
		if strings.ContainsAny(string(value), "\r\n") {
			lines := secretLines(string(value))
			if len(lines) == 0 || slices.ContainsFunc(lines, func(l string) bool { return len(l) < minSecretValue }) {
				return nil, conflictErr(fmt.Sprintf(
					"a line of the value of %s is shorter than %d characters; the session output is split into lines, "+
						"so it could not be withheld there; store it without line breaks"+secretEnvWayOut,
					ref.Env, minSecretValue))
			}
		}
		out = append(out, EnvVar{Name: ref.Env, Value: string(value)})
	}
	if env, ok := markerCollision(refs, out); ok {
		return nil, conflictErr(fmt.Sprintf(
			"the value of %s shares text with the mark that replaces a secret in the session output, "+
				"so it could not be withheld there; store a different value"+secretEnvWayOut, env))
	}
	if env, ok := cutMarkCollision(out); ok {
		return nil, conflictErr(fmt.Sprintf(
			"the value of %s shares text with the note that ends a cut line in the session output, "+
				"so it could not be withheld there; store a different value"+secretEnvWayOut, env))
	}
	return out, nil
}

// secretMark is the text the output carries in place of a secret's value.
func secretMark(secret string) string { return "[secret " + secret + "]" }

// secretForms is a value as the output can carry it: raw, and JSON-escaped since
// stream-json carries strings escaped, both as json.Marshal writes it and as the
// tools write it (toolJSONForm). Each distinct form once.
func secretForms(value string) []string {
	escaped, _ := json.Marshal(value)
	forms := []string{value}
	for _, form := range append([]string{string(escaped[1 : len(escaped)-1]), toolJSONForm(value)}, secretLines(value)...) {
		if !slices.Contains(forms, form) {
			forms = append(forms, form)
		}
	}
	return forms
}

// secretLines is a value with a line break as the output framer delivers it: line
// by line, each without its trailing CR (procrunner.go pump and pumpDiagnostics).
// Empty lines are not forms. A value without CR or LF has none.
func secretLines(value string) []string {
	if !strings.ContainsAny(value, "\r\n") {
		return nil
	}
	var lines []string
	for _, line := range strings.Split(value, "\n") {
		if line = strings.TrimRight(line, "\r"); line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

// toolJSONForm is value as JSON.stringify (Claude Code, OpenCode) and serde_json
// (Codex) write it inside a string: only the quote, the backslash and control
// characters are escaped, never <, >, &, U+2028 or U+2029 as json.Marshal does, so
// a value holding both kinds reaches the output in a form json.Marshal never makes.
func toolJSONForm(value string) string {
	var b strings.Builder
	for i := 0; i < len(value); i++ {
		switch c := value[i]; c {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if c < 0x20 {
				fmt.Fprintf(&b, `\u%04x`, c)
			} else {
				b.WriteByte(c)
			}
		}
	}
	return b.String()
}

// markerCollision names the first variable whose value, raw or JSON-escaped, shares
// text with a mark this run's redactor can write (secretMark of every bound name):
// inside it, containing it, or across either of its edges. The redactor writes each
// mark once and never scans it again, so such a mark could complete the value in
// the output. A launch or resume refuses the binding before spawn (resolveSecretEnv).
func markerCollision(refs []SecretEnvRef, values []EnvVar) (string, bool) {
	for _, v := range values {
		if v.Value == "" {
			continue
		}
		for _, form := range secretForms(v.Value) {
			for _, ref := range refs {
				if sharesText(form, secretMark(ref.Secret)) {
					return v.Name, true
				}
			}
		}
	}
	return "", false
}

// cutMarkCollision names the first variable whose value, in any form, shares text
// with the note the framer appends to a cut line (diagnosticTruncatedMark): that
// note is the engine's own text, so it must never complete or carry a value.
func cutMarkCollision(values []EnvVar) (string, bool) {
	for _, v := range values {
		if v.Value == "" {
			continue
		}
		for _, form := range secretForms(v.Value) {
			if sharesText(form, diagnosticTruncatedMark) {
				return v.Name, true
			}
		}
	}
	return "", false
}

// sharesText reports whether an occurrence of value could overlap a mark written
// inside, around or next to it: one contains the other, or a suffix of one is a
// prefix of the other.
func sharesText(value, mark string) bool {
	if strings.Contains(mark, value) || strings.Contains(value, mark) {
		return true
	}
	for k := 1; k < len(value) && k < len(mark); k++ {
		if strings.HasSuffix(value, mark[:k]) || strings.HasPrefix(value, mark[len(mark)-k:]) {
			return true
		}
	}
	return false
}

// encodeSecretEnv is the run row's record of the names: a JSON array, never values.
func encodeSecretEnv(refs []SecretEnvRef) string {
	if len(refs) == 0 {
		return ""
	}
	raw, _ := json.Marshal(refs)
	return string(raw)
}

func decodeSecretEnv(raw string) ([]SecretEnvRef, error) {
	if raw == "" {
		return nil, nil
	}
	var refs []SecretEnvRef
	if err := json.Unmarshal([]byte(raw), &refs); err != nil {
		return nil, fmt.Errorf("sessions: stored secret_env: %w", err)
	}
	return refs, nil
}

// storedSecretEnvNames reads a run row's names for the API; an unreadable value
// reads as none rather than failing a list.
func storedSecretEnvNames(rec model.Record) []SecretEnvRef {
	refs, _ := decodeSecretEnv(rec.String(colRunSecretEnv))
	return refs
}

// secretEnvDetail is the ledger's line: which variable came from which secret.
func secretEnvDetail(refs []SecretEnvRef) string {
	if len(refs) == 0 {
		return ""
	}
	parts := make([]string, 0, len(refs))
	for _, ref := range refs {
		parts = append(parts, ref.Env+"<-"+ref.Secret)
	}
	return "secret_env " + strings.Join(parts, ",")
}

// secretRedactor replaces the exact bytes of a session's secret values in its
// output before the plane stores or streams a frame. Each value is matched raw
// and in its JSON-escaped form, since stream-json carries strings escaped.
type secretRedactor struct {
	needles [][]byte
	marks   [][]byte
	// withholdAll is set for a binding a mark could complete (markerCollision). A
	// launch refuses such a binding before spawn; a redactor built from one anyway
	// returns nothing rather than a frame that could carry a value.
	withholdAll bool
}

func newSecretRedactor(refs []SecretEnvRef, values []EnvVar) *secretRedactor {
	secretOf := make(map[string]string, len(refs))
	for _, ref := range refs {
		secretOf[ref.Env] = ref.Secret
	}
	r := &secretRedactor{}
	add := func(needle, mark string) {
		if needle == "" {
			return
		}
		r.needles = append(r.needles, []byte(needle))
		r.marks = append(r.marks, []byte(mark))
	}
	for _, v := range values {
		for _, form := range secretForms(v.Value) {
			add(form, secretMark(secretOf[v.Name]))
		}
	}
	if len(r.needles) == 0 {
		return nil
	}
	if _, collides := markerCollision(refs, values); collides {
		return &secretRedactor{withholdAll: true}
	}
	if _, collides := cutMarkCollision(values); collides {
		return &secretRedactor{withholdAll: true}
	}
	// Longest first, so a value that contains another is withheld whole.
	order := make([]int, len(r.needles))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool { return len(r.needles[order[a]]) > len(r.needles[order[b]]) })
	needles, marks := make([][]byte, len(order)), make([][]byte, len(order))
	for i, at := range order {
		needles[i], marks[i] = r.needles[at], r.marks[at]
	}
	r.needles, r.marks = needles, marks
	return r
}

// apply returns data with every secret value replaced. A nil redactor (a session
// with no secrets) returns data unchanged.
func (r *secretRedactor) apply(data []byte) []byte {
	out, _ := r.applyWithSpans(data)
	return out
}

// SecretMaskSpan is the byte range [Start, End) of one "[secret <name>]" marker the
// redactor wrote into its output.
type SecretMaskSpan struct {
	Start, End int
}

// applyWithSpans replaces every secret value in ONE left-to-right pass and records
// each marker's span as it writes it, so a span is never found again by scanning the
// output: text that already said "[secret x]" gets no span, and a later value can
// never match inside a marker already written, so a binding a marker could complete
// is refused at launch (markerCollision). At each position the longest value wins
// (the needles are ordered longest first). Spans are ordered and non-overlapping.
func (r *secretRedactor) applyWithSpans(data []byte) ([]byte, []SecretMaskSpan) {
	if r == nil {
		return data, nil
	}
	if r.withholdAll {
		return []byte{}, nil
	}
	// A stderr line cut at the cap may end with the beginning of a value. The cut is
	// read on the ORIGINAL bytes, before any replacement: masking first could rewrite the mark or hide the cut's context.
	body, cut := data, bytes.HasSuffix(data, []byte(diagnosticTruncatedMark))
	clipAt, clipWhich := len(data), -1
	if cut {
		body = data[:len(data)-len(diagnosticTruncatedMark)]
		clipAt, clipWhich = r.cutValue(body)
	}
	head := body[:clipAt]
	present := false
	for _, needle := range r.needles {
		if bytes.Contains(head, needle) {
			present = true
			break
		}
	}
	if !present && clipWhich < 0 {
		return data, nil // the common frame: nothing to replace, nothing copied
	}
	out := make([]byte, 0, len(data))
	var spans []SecretMaskSpan
	for i := 0; i < len(head); {
		matched := false
		for n, needle := range r.needles {
			if bytes.HasPrefix(head[i:], needle) {
				start := len(out)
				out = append(out, r.marks[n]...)
				spans = append(spans, SecretMaskSpan{Start: start, End: len(out)})
				i += len(needle)
				matched = true
				break
			}
		}
		if !matched {
			out = append(out, head[i])
			i++
		}
	}
	if clipWhich >= 0 {
		start := len(out)
		out = append(out, r.marks[clipWhich]...)
		spans = append(spans, SecretMaskSpan{Start: start, End: len(out)})
	}
	if cut {
		out = append(out, diagnosticTruncatedMark...)
	}
	return out, spans
}

// cutValue finds, on the original bytes of a line the framer cut, where the part
// to withhold starts: the longest beginning of a value that ends the line, widened
// left over every value occurrence that reaches into it, so no part of either is
// left before the marker. which is the secret whose marker replaces that part, or
// -1 when the line ends with no beginning of a value.
func (r *secretRedactor) cutValue(body []byte) (start, which int) {
	best, which := 0, -1
	for n, needle := range r.needles {
		if k := longestPrefixAtEnd(needle, body); k > best {
			best, which = k, n
		}
	}
	if which < 0 {
		return len(body), -1
	}
	start = len(body) - best
	for widened := true; widened; {
		widened = false
		for _, needle := range r.needles {
			// An occurrence that starts before start and ends after it lies in
			// body[start-len+1 : start+len-1]; the first one found is the leftmost.
			lo, hi := max(0, start-len(needle)+1), min(len(body), start+len(needle)-1)
			if hi-lo < len(needle) {
				continue
			}
			if i := bytes.Index(body[lo:hi], needle); i >= 0 && lo+i < start {
				start, widened = lo+i, true
			}
		}
	}
	return start, which
}

// longestPrefixAtEnd is the length of the longest proper prefix of needle that
// ends text: Knuth-Morris-Pratt over at most len(needle)-1 bytes, linear in the
// value's length.
func longestPrefixAtEnd(needle, text []byte) int {
	m := len(needle)
	if m < 2 {
		return 0
	}
	if len(text) > m-1 {
		text = text[len(text)-(m-1):]
	}
	fail := make([]int, m)
	for i, k := 1, 0; i < m; i++ {
		for k > 0 && needle[i] != needle[k] {
			k = fail[k-1]
		}
		if needle[i] == needle[k] {
			k++
		}
		fail[i] = k
	}
	k := 0
	for _, c := range text {
		for k > 0 && c != needle[k] {
			k = fail[k-1]
		}
		if c == needle[k] {
			k++
		}
	}
	return k
}

// RedactSessionSecrets withholds a supervised run's vault secret values from data
// that leaves the session through another path: the Claude Code hook PEP and the
// approval it may open carry a tool's input, and a command the model wrote after
// reading GITHUB_TOKEN can hold the value. It applies the run's own per-launch
// redactor (exact values, raw and JSON-escaped, replaced by "[secret <name>]"), so
// there is no second vault read and no second list of names.
//
// ok is false when this node does not supervise runRef in tenant, so it cannot vouch
// for the data: a caller whose session credential names vault secrets withholds the
// data then rather than show it. A supervised run with no secrets returns data
// unchanged with ok true.
func (m *Module) RedactSessionSecrets(tenant model.TenantID, runRef string, data []byte) (redacted []byte, ok bool) {
	lr, live := m.rt.getLive(tenant, runRef)
	if !live {
		return data, false
	}
	return lr.redact.apply(data), true
}

// RedactSessionSecretsWithSpans is RedactSessionSecrets plus the byte span of each
// marker it wrote, in order and non-overlapping, recorded while replacing. A caller
// that cleans the text again by pattern (connectors/redact.CleanMasked) passes them
// so a later clean never splits "[secret <name>]". ok has the same meaning.
func (m *Module) RedactSessionSecretsWithSpans(tenant model.TenantID, runRef string, data []byte) (redacted []byte, spans []SecretMaskSpan, ok bool) {
	lr, live := m.rt.getLive(tenant, runRef)
	if !live {
		return data, nil, false
	}
	out, spans := lr.redact.applyWithSpans(data)
	return out, spans, true
}
