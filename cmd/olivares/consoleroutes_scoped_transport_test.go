// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"testing"
)

const scopedTransportSource = `export async function scopedRequest<T>(
  scope: RequestScope,
  path: string,
  options: Pick<RequestOptions, 'method' | 'body' | 'query'> = {},
): Promise<T> {
  return apiFetch<T>(path, {
    method: options.method,
    body: options.body,
    tenant: scope.tenant,
    signal: scope.signal,
  })
}
`

func TestScopedTransportClientCalls(t *testing.T) {
	for _, method := range []string{"GET", "POST", "PUT", "PATCH", "DELETE"} {
		t.Run(method, func(t *testing.T) {
			options := ""
			if method != "GET" {
				options = fmt.Sprintf(", { method: '%s', body: { nested: { value: 'GET' } } }", method)
			}
			source := scopedTransportSource + "scopedRequest<{ items: string[] }>(scope, '/v1/widgets'" + options + ")\n"
			calls, unresolved, raw := parseTS(t, source)
			if len(calls) != 1 || calls[0].method != method || calls[0].path != "/v1/widgets" || len(unresolved) != 0 || len(raw) != 0 {
				t.Fatalf("scoped transport must preserve the call's verb and path: calls=%v unresolved=%v raw=%v", calls, unresolved, raw)
			}
			_, _, raw = parseTS(t, strings.ReplaceAll(source, "apiFetch<T>", "withoutTransport<T>"))
			calls, _, _ = parseTS(t, strings.ReplaceAll(source, "apiFetch<T>", "withoutTransport<T>"))
			if len(calls) != 0 || len(raw) != 0 {
				t.Fatal("removing the transport must remove surface coverage")
			}
		})
	}
}

func TestScopedTransportDoesNotGuessDynamicMethods(t *testing.T) {
	for _, options := range []string{"{ method: selectedMethod }", "options", "{ ...options }", "{ body: { method: 'POST' } }", "{ get method() { return 'DELETE' } }"} {
		source := scopedTransportSource + "scopedRequest(scope, '/v1/widgets', " + options + ")\n"
		calls, unresolved, _ := parseTS(t, source)
		if options == "{ body: { method: 'POST' } }" {
			if len(calls) != 1 || calls[0].method != "GET" {
				t.Fatalf("nested body method must not become the request method: %v", calls)
			}
		} else if len(calls) != 0 || len(unresolved) != 1 {
			t.Fatalf("unknown methods must stay unresolved: calls=%v unresolved=%v", calls, unresolved)
		}
	}
}

// Scoped wrappers put a request lifetime before the path. Resolve them by their
// transport and forwarded method, rather than by a feature-specific function name.
var scopedTransportFnRe = regexp.MustCompile(`(?ms)^\s*(?:export\s+)?(?:async\s+)?function\s+([A-Za-z_$][\w$]*)\s*(?:<[^(]*?>)?\s*\(\s*[A-Za-z_$][\w$]*\s*:[^,\n]+,\s*([A-Za-z_$][\w$]*)\s*:\s*string\s*,\s*([A-Za-z_$][\w$]*)\b(.*?)\n\}`)

// tsArguments splits only at the outer delimiter. Quoted literals and comments
// use the parser's existing lexer; nested payloads cannot supply a request verb.
func tsArguments(text string, start int, close byte) ([]string, int, bool) {
	spans := consoleNonCodeRe.FindAllStringIndex(text[start:], -1)
	span, depth, field := 0, 0, start
	var args []string
	for i := start; i < len(text); i++ {
		if span < len(spans) && i == start+spans[span][0] {
			i = start + spans[span][1] - 1
			span++
			continue
		}
		switch ch := text[i]; ch {
		case '(', '{', '[':
			depth++
		case ')', '}', ']':
			if depth == 0 {
				if ch != close {
					return nil, 0, false
				}
				if tail := strings.TrimSpace(text[field:i]); tail != "" {
					args = append(args, tail)
				}
				return args, i + 1, true
			}
			depth--
		case ',':
			if depth == 0 {
				args = append(args, strings.TrimSpace(text[field:i]))
				field = i + 1
			}
		}
	}
	return nil, 0, false
}

// Preserve byte offsets and line numbers while letting comments act as whitespace.
func scopedWithoutComments(text string) string {
	return consoleNonCodeRe.ReplaceAllStringFunc(text, func(token string) string {
		if !strings.HasPrefix(token, "//") && !strings.HasPrefix(token, "/*") {
			return token
		}
		masked := []byte(token)
		for i, ch := range masked {
			if ch != '\n' && ch != '\r' {
				masked[i] = ' '
			}
		}
		return string(masked)
	})
}

func scopedRequestMethod(options string) (string, bool) {
	options = scopedWithoutComments(options)
	options = strings.TrimSpace(options)
	if options == "" {
		return "get", true
	}
	if !strings.HasPrefix(options, "{") {
		return "", false
	}
	fields, end, ok := tsArguments(options, 1, '}')
	if !ok || strings.TrimSpace(options[end:]) != "" {
		return "", false
	}
	method := "get"
	for _, field := range fields {
		if strings.HasPrefix(field, "...") || strings.HasPrefix(field, "[") {
			return "", false
		}
		key, value, hasValue := strings.Cut(field, ":")
		key = strings.TrimSpace(key)
		if !hasValue || !scopedObjectKeyRe.MatchString(key) {
			return "", false
		}
		key = strings.Trim(key, "'\"")
		if key != "method" {
			continue
		}
		value = strings.TrimSpace(value)
		if len(value) < 2 || (value[0] != '\'' && value[0] != '"') || value[len(value)-1] != value[0] {
			return "", false
		}
		method = strings.ToLower(value[1 : len(value)-1])
		switch method {
		case "get", "post", "put", "patch", "delete":
		default:
			return "", false
		}
	}
	return method, true
}

// Resolve only a single unqualified shared transport with unchanged arguments.
// Unknown defaults, object overlays and writes must not manufacture route coverage.
var scopedTransportSignatureRe = regexp.MustCompile(`(?s)^\s*:\s*(?:Pick<[^>]*>|[A-Za-z_$][\w$]*)\s*=\s*\{\s*\}\s*,?\s*\)\s*(?::[^{}]*)?\{`)
var scopedAPIFetchRe = regexp.MustCompile(`\bapiFetch(?:WithMeta)?\s*(?:<[^(]*?>)?\s*\(`)
var scopedIdentifierTailRe = regexp.MustCompile(`[\pL\pN_$]$`)
var scopedDestructureRe = regexp.MustCompile(`(?m)(?:\b(?:const|let|var)\s*|[(,]\s*|(?:^|[;\n])\s*)([\[{])`)
var scopedObjectKeyRe = regexp.MustCompile(`^(?:[A-Za-z_$][\w$]*|'[^']*'|"[^"]*")$`)

const scopedFunctionDeclaration = `\bfunction(?:\s*\*\s*|\s+)`

var scopedFunctionPrefixRe = regexp.MustCompile(scopedFunctionDeclaration + `$`)

func scopedReferenceHasPrefix(text string, offset int) bool {
	return strings.HasSuffix(strings.TrimSpace(text[:offset]), ".") || scopedIdentifierTailRe.MatchString(text[:offset])
}

func scopedTransportSemantics(body, path, options string) bool {
	body = scopedWithoutComments(body)
	header := scopedTransportSignatureRe.FindStringIndex(body)
	if header == nil {
		return false
	}
	body = body[header[1]:]
	matches := scopedAPIFetchRe.FindAllStringIndex(body, -1)
	if len(matches) != 1 || scopedReferenceHasPrefix(body, matches[0][0]) {
		return false
	}
	args, _, ok := tsArguments(body, matches[0][1], ')')
	if !ok || len(args) != 2 || strings.TrimSpace(args[0]) != path {
		return false
	}
	init := strings.TrimSpace(args[1])
	if !strings.HasPrefix(init, "{") {
		return false
	}
	fields, end, ok := tsArguments(init, 1, '}')
	if !ok || strings.TrimSpace(init[end:]) != "" {
		return false
	}
	methods, forwardedOptions := 0, 0
	for _, field := range fields {
		key, value, ok := strings.Cut(field, ":")
		key = strings.TrimSpace(key)
		if !ok || !scopedObjectKeyRe.MatchString(key) {
			return false
		}
		switch strings.TrimSpace(value) {
		case options + ".method", options + ".body", options + ".query":
			forwardedOptions++
		}
		if strings.Trim(key, "'\"") == "method" {
			methods++
			if strings.TrimSpace(value) != options+".method" {
				return false
			}
		}
	}
	// Template interpolation is executable code. The shared lexer masks it as
	// a literal, so reject it before masking rather than hide argument writes.
	for _, token := range consoleNonCodeRe.FindAllString(body, -1) {
		if strings.HasPrefix(token, "`") && strings.Contains(token, "${") {
			return false
		}
	}
	// Mask literal/comment contents before checking identifiers and writes.
	code := consoleNonCodeRe.ReplaceAllStringFunc(body, func(token string) string {
		return strings.Repeat(" ", len(token))
	})
	pathRefs := regexp.MustCompile(`\b` + regexp.QuoteMeta(path) + `\b`)
	optionRefs := regexp.MustCompile(`\b` + regexp.QuoteMeta(options) + `\b`)
	// Accept extra references only in known read-only expressions. This keeps
	// cockpit outcome checks without guessing every possible assignment form.
	code = strings.NewReplacer("(", " ", ")", " ").Replace(code)
	optionRead := regexp.MustCompile(`^\s*\.\s*(?:method|body|query)\b`)
	readStart := regexp.MustCompile(`(?:\b(?:if|while|return)|&&|\|\||\?\?|[=?:,])\s*$`)
	readOnly := regexp.MustCompile(`^\s*(?:!==|===|!=|==|<=|>=|<(?:[^<=]|$)|>(?:[^>=]|$)|(?:&&|\|\||\?\?)(?:[^=]|$))`)
	refs := optionRefs.FindAllStringIndex(code, -1)
	readOnlyRefs := 0
	for _, ref := range refs {
		read := optionRead.FindStringIndex(code[ref[1]:])
		if read == nil {
			return false
		}
		if readStart.MatchString(code[:ref[0]]) && readOnly.MatchString(code[ref[1]+read[1]:]) {
			readOnlyRefs++
		}
	}
	return methods == 1 && len(pathRefs.FindAllStringIndex(code, -1)) == 1 &&
		len(refs) == forwardedOptions+readOnlyRefs
}

func scopedTransportsAsHTTP(text string) (string, []clientCall) {
	type replacement struct {
		start, end int
		text       string
	}
	var changes []replacement
	var unresolved []clientCall
	masked := scopedWithoutComments(text)
	seen := map[string]bool{}
	for _, fn := range scopedTransportFnRe.FindAllStringSubmatchIndex(masked, -1) {
		if consoleBindingInComment(text, fn[0]) {
			continue
		}
		name, path, options, body := masked[fn[2]:fn[3]], masked[fn[4]:fn[5]], masked[fn[6]:fn[7]], masked[fn[8]:fn[9]]
		if seen[name] || len(scopedAPIFetchRe.FindAllStringIndex(body, -1)) == 0 {
			continue
		}
		seen[name] = true
		// Any redeclaration or assignment makes the wrapper or transport binding
		// uncertain, including nested functions and commented declaration gaps.
		names := `(?:` + regexp.QuoteMeta(name) + `|apiFetch(?:WithMeta)?)`
		shadow := regexp.MustCompile(`(?:\b(?:const|let|var)\s+` + names + `\b|[(,]\s*` + names + `\s*(?:\?\s*)?[:=,)]|\b` + names + `[\s)]*(?:[+*/%&|?^<>-]*=[^=]|=>|\+\+|--|(?:as|satisfies|in|of)\b|!(?:[^=]|$))|(?:\bdelete|\+\+|--)[\s(]*(?:<(?s:.*?)>[\s(]*)?` + names + `\b|` + scopedFunctionDeclaration + `apiFetch(?:WithMeta)?\b)`)
		declarations := regexp.MustCompile(scopedFunctionDeclaration + regexp.QuoteMeta(name) + `\b`)
		ambiguous := !scopedTransportSemantics(body, path, options) || len(declarations.FindAllStringIndex(masked, -1)) != 1
		for _, declaration := range shadow.FindAllStringIndex(masked, -1) {
			if !consoleBindingInComment(text, declaration[0]) {
				ambiguous = true
			}
		}
		// Destructuring can rebind a wrapper or transport even when it has
		// nested/default fields. Reuse the delimiter lexer to inspect the pattern.
		binding := regexp.MustCompile(`(?:^|[^\pL\pN_$])(` + regexp.QuoteMeta(name) + `|apiFetch(?:WithMeta)?)(?:$|[^\pL\pN_$])`)
		for _, declaration := range scopedDestructureRe.FindAllStringSubmatchIndex(masked, -1) {
			close := byte('}')
			if masked[declaration[2]] == '[' {
				close = ']'
			}
			_, end, ok := tsArguments(masked, declaration[3], close)
			if !ok {
				ambiguous = true
				continue
			}
			pattern := masked[declaration[3] : end-1]
			for _, identifier := range binding.FindAllStringSubmatchIndex(pattern, -1) {
				// A call within an array/object literal is not a binding. This
				// includes Promise.all([wrapper<T>(...)]) in real console callers.
				tail := strings.TrimSpace(pattern[identifier[3]:])
				if !strings.HasPrefix(tail, "(") && !strings.HasPrefix(tail, "<") {
					ambiguous = true
				}
			}
		}
		calls := regexp.MustCompile(`\b` + regexp.QuoteMeta(name) + `\s*(?:<[^(]*?>)?\s*\(`)
		for _, call := range calls.FindAllStringIndex(masked, -1) {
			if (call[0] >= fn[0] && call[0] < fn[1]) || consoleBindingInComment(text, call[0]) || scopedFunctionPrefixRe.MatchString(masked[:call[0]]) {
				continue
			}
			args, end, ok := tsArguments(text, call[1], ')')
			if !ok || len(args) < 2 || len(args) > 3 {
				continue
			}
			init := ""
			if len(args) == 3 {
				init = args[2]
			}
			verb, known := scopedRequestMethod(init)
			if !known || ambiguous || scopedReferenceHasPrefix(masked, call[0]) {
				unresolved = append(unresolved, clientCall{path: args[1] + " (scoped transport: method or binding is not statically resolved)", line: 1 + strings.Count(text[:call[0]], "\n")})
				continue
			}
			var rewritten string
			for _, path := range scopedLiteralPaths(text[:call[0]], args[1]) {
				rewritten += "http." + verb + "(" + path + ") "
			}
			rewritten += strings.Repeat("\n", max(0, strings.Count(text[call[0]:end], "\n")-strings.Count(rewritten, "\n")))
			changes = append(changes, replacement{call[0], end, rewritten})
		}
	}
	sort.Slice(changes, func(i, j int) bool { return changes[i].start > changes[j].start })
	for _, change := range changes {
		text = text[:change.start] + change.text + text[change.end:]
	}
	return text, unresolved
}

func TestScopedTransportLiteralActionUnion(t *testing.T) {
	source := scopedTransportSource + "export const client = {\n  control: (scope: RequestScope, id: string, action: 'start' | 'stop') =>\n    scopedRequest(scope, `/v1/widgets/${id}/${action}`, { method: 'POST' }),\n}\n"
	calls, unresolved, _ := parseTS(t, source)
	if len(calls) != 2 || len(unresolved) != 0 || calls[0].path != "/v1/widgets/{}/start" || calls[1].path != "/v1/widgets/{}/stop" {
		t.Fatalf("literal action alternatives must enumerate real path segments: calls=%v unresolved=%v", calls, unresolved)
	}
}

// Only the arrow expression whose body is this call supplies literal alternatives.
// The same parameter name in another function cannot widen this call's paths.
var scopedArrowParamsRe = regexp.MustCompile(`(?s)\(([^()]*)\)\s*=>\s*$`)
var scopedLiteralParamRe = regexp.MustCompile(`(?:^|,)\s*([A-Za-z_$][\w$]*)\s*:\s*((?:'[^']*'\s*\|\s*)+'[^']*')\s*(?:,|$)`)
var scopedLiteralValueRe = regexp.MustCompile(`'([^']*)'`)

func scopedLiteralPaths(prefix, path string) []string {
	paths := []string{path}
	signature := scopedArrowParamsRe.FindStringSubmatch(prefix)
	if signature == nil {
		return paths
	}
	for _, parameter := range scopedLiteralParamRe.FindAllStringSubmatch(signature[1], -1) {
		marker := "${" + parameter[1] + "}"
		if !strings.Contains(path, marker) {
			continue
		}
		values := scopedLiteralValueRe.FindAllStringSubmatch(parameter[2], -1)
		if len(paths)*len(values) > 8 {
			return []string{path}
		}
		var next []string
		for _, candidate := range paths {
			for _, value := range values {
				next = append(next, strings.ReplaceAll(candidate, marker, value[1]))
			}
		}
		paths = next
	}
	return paths
}

func TestScopedTransportCommentedOptions(t *testing.T) {
	for _, options := range []string{"{ /* note */ method: 'POST' }", "{ /* note */ ...options }"} {
		calls, unresolved, _ := parseTS(t, scopedTransportSource+"scopedRequest(scope, '/v1/widgets', "+options+")\n")
		if strings.Contains(options, "...options") {
			if len(calls) != 0 || len(unresolved) != 1 {
				t.Errorf("commented spread must stay unresolved: calls=%v unresolved=%v", calls, unresolved)
			}
		} else if len(calls) != 1 || calls[0].method != "POST" {
			t.Errorf("comments must not hide the method: %v", calls)
		}
	}
}

func TestScopedTransportShadowedBinding(t *testing.T) {
	for _, declaration := range []string{
		"function useLocal(scopedRequest: Callback) {",
		"function useLocal(/* note */ scopedRequest: Callback) {",
		"function useLocal() { const /* note */ scopedRequest = callback;",
	} {
		t.Run(declaration, func(t *testing.T) {
			source := scopedTransportSource + declaration + "\n  return scopedRequest(scope, '/v1/widgets', { method: 'POST' })\n}\n"
			calls, unresolved, _ := parseTS(t, source)
			if len(calls) != 0 || len(unresolved) != 1 {
				t.Fatalf("shadowed binding must not invent transport coverage: calls=%v unresolved=%v", calls, unresolved)
			}
		})
	}
}

func TestScopedTransportUncertainSemantics(t *testing.T) {
	t.Run("caller accessor", func(t *testing.T) {
		calls, unresolved, _ := parseTS(t, scopedTransportSource+"scopedRequest(scope, '/v1/widgets', { get method() { return 'DELETE' } })\n")
		if len(calls) != 0 || len(unresolved) != 1 {
			t.Fatalf("accessor method must stay unresolved: calls=%v unresolved=%v", calls, unresolved)
		}
	})
	for name, source := range map[string]string{
		"spread overrides method":   strings.Replace(scopedTransportSource, "method: options.method,", "method: options.method, ...{ method: 'DELETE' },", 1),
		"assign method":             strings.Replace(scopedTransportSource, "  return apiFetch", "  options.method = 'DELETE'\n  return apiFetch", 1),
		"assign path":               strings.Replace(scopedTransportSource, "  return apiFetch", "  path = '/v1/other'\n  return apiFetch", 1),
		"default method":            strings.Replace(scopedTransportSource, "= {},", "= { method: 'DELETE' },", 1),
		"object transport":          strings.Replace(scopedTransportSource, "apiFetch<T>", "other.apiFetch<T>", 1),
		"object transport with gap": strings.Replace(scopedTransportSource, "apiFetch<T>", "other. /* note */ apiFetch<T>", 1),
		"duplicate method":          strings.Replace(scopedTransportSource, "method: options.method,", "method: options.method, method: 'DELETE',", 1),
		"computed method":           strings.Replace(scopedTransportSource, "method: options.method,", "method: options.method, ['method']: 'DELETE',", 1),
		"bracket method mutation":   strings.Replace(scopedTransportSource, "  return apiFetch", "  options['method'] = 'DELETE'\n  return apiFetch", 1),
		"default mutation":          strings.Replace(scopedTransportSource, "  return apiFetch", "  options.method ||= 'DELETE'\n  return apiFetch", 1),
		"shadowed transport":        strings.Replace(scopedTransportSource, "  return apiFetch", "  const apiFetch = otherTransport\n  return apiFetch", 1),
		"aliased options":           strings.Replace(scopedTransportSource, "  return apiFetch", "  mutate(options)\n  return apiFetch", 1),
		"rebind options":            strings.Replace(scopedTransportSource, "  return apiFetch", "  options = { method: 'DELETE' }\n  return apiFetch", 1),
	} {
		t.Run(name, func(t *testing.T) {
			calls, unresolved, _ := parseTS(t, source+"scopedRequest(scope, '/v1/widgets', { method: 'POST' })\n")
			if len(calls) != 0 || len(unresolved) != 1 {
				t.Fatalf("uncertain wrapper must remain unresolved: calls=%v unresolved=%v", calls, unresolved)
			}
		})
	}
}

func TestScopedTransportOmittedDefaultMethod(t *testing.T) {
	source := strings.Replace(scopedTransportSource, "= {},", "= { method: 'DELETE' },", 1)
	calls, unresolved, _ := parseTS(t, source+"scopedRequest(scope, '/v1/widgets')\n")
	if len(calls) != 0 || len(unresolved) != 1 {
		t.Fatalf("nonempty default options must not fabricate GET: calls=%v unresolved=%v", calls, unresolved)
	}
}

func TestScopedTransportMemberBinding(t *testing.T) {
	for _, gap := range []string{"", " ", "\n", " /* note */ ", " // note\n"} {
		t.Run(gap, func(t *testing.T) {
			calls, unresolved, _ := parseTS(t, scopedTransportSource+"other."+gap+"scopedRequest(scope, '/v1/widgets', { method: 'POST' })\n")
			if len(calls) != 0 || len(unresolved) != 1 {
				t.Fatalf("member call must not bind to local wrapper: calls=%v unresolved=%v", calls, unresolved)
			}
		})
	}
}

func TestScopedTransportNestedFunctionBinding(t *testing.T) {
	source := scopedTransportSource + "function useLocal() {\n  function /* note */ scopedRequest(scope: RequestScope, path: string, options: RequestOptions) { return other(path) }\n  return scopedRequest(scope, '/v1/widgets', { method: 'POST' })\n}\n"
	calls, unresolved, _ := parseTS(t, source)
	if len(calls) != 0 || len(unresolved) != 1 {
		t.Fatalf("nested function must shadow local wrapper: calls=%v unresolved=%v", calls, unresolved)
	}
}

func TestScopedTransportAdditionalBindingForms(t *testing.T) {
	call := "scopedRequest(scope, '/v1/widgets', { method: 'POST' })\n"
	for name, source := range map[string]string{
		"transport identifier prefix":      strings.Replace(scopedTransportSource, "apiFetch<T>", "$apiFetch<T>", 1) + call,
		"caller identifier prefix":         scopedTransportSource + "$" + call,
		"destructured transport":           strings.Replace(scopedTransportSource, "  return apiFetch", "  const { apiFetch } = other\n  return apiFetch", 1) + call,
		"destructured caller":              scopedTransportSource + "function useLocal() {\n  const { scopedRequest } = other\n  return " + call + "}\n",
		"destructured caller with default": scopedTransportSource + "function useLocal() {\n  const { unused = 0, scopedRequest } = other\n  return " + call + "}\n",
		"destructured caller parameter":    scopedTransportSource + "function useLocal({ scopedRequest }: { scopedRequest: Callback }) {\n  return " + call + "}\n",
		"destructured arrow parameter":     scopedTransportSource + "const useLocal = ({ scopedRequest }: Callbacks) => " + call,
		"destructured catch binding":       scopedTransportSource + "try { fail() } catch ({ scopedRequest }) {\n  " + call + "}\n",
		"destructured assignment":          scopedTransportSource + "({ scopedRequest } = other)\n" + call,
		"destructured array assignment":    scopedTransportSource + "[scopedRequest] = [other]\n" + call,
		"caller logical reassignment":      scopedTransportSource + "scopedRequest &&= otherTransport\n" + call,
		"transport logical reassignment":   strings.Replace(scopedTransportSource, "  return apiFetch", "  apiFetch &&= otherTransport\n  return apiFetch", 1) + call,
		"unparenthesized arrow binding":    scopedTransportSource + "const useLocal = scopedRequest => " + call,
		"transport reassignment":           strings.Replace(scopedTransportSource, "  return apiFetch", "  apiFetch = otherTransport\n  return apiFetch", 1) + call,
		"template method mutation":         strings.Replace(scopedTransportSource, "  return apiFetch", "  const diagnostic = `${(options.method = 'DELETE')}`\n  return apiFetch", 1) + call,
		"template path mutation":           strings.Replace(scopedTransportSource, "  return apiFetch", "  const diagnostic = `${(path = '/v1/other')}`\n  return apiFetch", 1) + call,
		"prefix mutation":                  strings.Replace(scopedTransportSource, "  return apiFetch", "  ++options.method\n  return apiFetch", 1) + call,
	} {
		t.Run(name, func(t *testing.T) {
			calls, unresolved, _ := parseTS(t, source)
			if len(calls) != 0 || len(unresolved) != 1 {
				t.Fatalf("additional binding form must stay unresolved: calls=%v unresolved=%v", calls, unresolved)
			}
		})
	}
}

func TestScopedTransportCallsInsideLiterals(t *testing.T) {
	call := "scopedRequest(scope, '/v1/widgets', { method: 'POST' })"
	for name, source := range map[string]string{
		"array of generic calls": scopedTransportSource + "Promise.all([" + strings.Replace(call, "scopedRequest(", "scopedRequest<{ value: string }>(", 1) + "])\n",
		"array of calls":         scopedTransportSource + "Promise.all([" + call + "])\n",
		"object call value":      scopedTransportSource + "consume({ result: " + call + " })\n",
	} {
		t.Run(name, func(t *testing.T) {
			calls, unresolved, _ := parseTS(t, source)
			if len(calls) != 1 || calls[0].method != "POST" || len(unresolved) != 0 {
				t.Fatalf("calls inside literals must retain coverage: calls=%v unresolved=%v", calls, unresolved)
			}
		})
	}
}

func TestScopedTransportParenthesizedWrites(t *testing.T) {
	for _, target := range []string{"options.method", "options.body", "options.query", "path", "scopedRequest", "apiFetch"} {
		for _, parens := range []int{0, 1, 2} {
			expr := strings.Repeat("(", parens) + target + strings.Repeat(")", parens)
			writes := []string{"delete " + expr, "++" + expr, "--" + expr, expr + "++", expr + "--"}
			for _, op := range []string{"=", "+=", "-=", "*=", "/=", "**=", "%=", "^=", "&=", "|=", "<<=", ">>=", ">>>=", "&&=", "||=", "??="} {
				writes = append(writes, expr+" "+op+" 'DELETE'")
			}
			for _, write := range writes {
				t.Run(write, func(t *testing.T) {
					source := strings.Replace(scopedTransportSource, "  return apiFetch", "  "+write+"\n  return apiFetch", 1)
					source += "scopedRequest(scope, '/v1/widgets', { method: 'POST' })\n"
					calls, unresolved, _ := parseTS(t, source)
					if len(calls) != 0 || len(unresolved) != 1 || !strings.HasPrefix(unresolved[0].path, "'/v1/widgets'") {
						t.Fatalf("write must keep the real caller unresolved: calls=%v unresolved=%v", calls, unresolved)
					}
				})
			}
		}
	}
}

func TestScopedTransportReadOnlyOptions(t *testing.T) {
	source := strings.Replace(scopedTransportSource, "  return apiFetch", "  if (options.method && options.method !== 'GET') reportUnknownOutcome()\n  return apiFetch", 1)
	source += "scopedRequest(scope, '/v1/widgets', { method: 'POST' })\n"
	calls, unresolved, _ := parseTS(t, source)
	if len(calls) != 1 || calls[0].method != "POST" || calls[0].path != "/v1/widgets" || len(unresolved) != 0 {
		t.Fatalf("read-only option checks must retain forwarding: calls=%v unresolved=%v", calls, unresolved)
	}
}

func TestScopedTransportTypeAssertedWrites(t *testing.T) {
	for _, target := range []string{"options.method", "options.body", "options.query", "path", "scopedRequest", "apiFetch", "apiFetchWithMeta"} {
		for _, assertion := range []string{" as any", " satisfies any", "!"} {
			expr := "(" + target + assertion + ")"
			for _, write := range []string{expr + " = 'DELETE'", expr + " ??= 'DELETE'", expr + "++", "++" + expr, "delete " + expr} {
				t.Run(write, func(t *testing.T) {
					source := strings.Replace(scopedTransportSource, "  return apiFetch", "  "+write+"\n  return apiFetch", 1)
					source += "scopedRequest(scope, '/v1/widgets', { method: 'POST' })\n"
					calls, unresolved, _ := parseTS(t, source)
					if len(calls) != 0 || len(unresolved) != 1 || !strings.HasPrefix(unresolved[0].path, "'/v1/widgets'") {
						t.Fatalf("asserted write must keep the real caller unresolved: calls=%v unresolved=%v", calls, unresolved)
					}
				})
			}
		}
	}
}

func TestScopedTransportLoopAndDestructuredWrites(t *testing.T) {
	for _, target := range []string{"options.method", "options.body", "options.query", "path", "scopedRequest", "apiFetch", "apiFetchWithMeta"} {
		for _, write := range []string{
			"for (" + target + " of ['DELETE']) {}",
			"for (" + target + " in { DELETE: 0 }) {}",
			"({ value: " + target + " } = { value: 'DELETE' })",
			"[" + target + "] = ['DELETE']",
		} {
			t.Run(write, func(t *testing.T) {
				source := strings.Replace(scopedTransportSource, "  return apiFetch", "  "+write+"\n  return apiFetch", 1)
				source += "scopedRequest(scope, '/v1/widgets', { method: 'POST' })\n"
				calls, unresolved, _ := parseTS(t, source)
				if len(calls) != 0 || len(unresolved) != 1 || !strings.HasPrefix(unresolved[0].path, "'/v1/widgets'") {
					t.Fatalf("assigned target must keep the real caller unresolved: calls=%v unresolved=%v", calls, unresolved)
				}
			})
		}
	}
}

func TestScopedTransportAssertedPrefixUpdates(t *testing.T) {
	for _, target := range []string{"options.method", "options.body", "options.query", "path", "scopedRequest", "apiFetch", "apiFetchWithMeta"} {
		for _, assertion := range []string{"any", "any & { x?: unknown; y?: unknown }", "any & Record<string, Array<unknown>>"} {
			for _, op := range []string{"++", "--"} {
				t.Run(op+"<"+assertion+">"+target, func(t *testing.T) {
					source := strings.Replace(scopedTransportSource, "  return apiFetch", "  if ("+op+"(<"+assertion+">"+target+") !== 'GET') reportUnknownOutcome()\n  return apiFetch", 1)
					source += "scopedRequest(scope, '/v1/widgets', { method: 'POST' })\n"
					calls, unresolved, _ := parseTS(t, source)
					if len(calls) != 0 || len(unresolved) != 1 || !strings.HasPrefix(unresolved[0].path, "'/v1/widgets'") {
						t.Fatalf("asserted prefix update must stay unresolved: calls=%v unresolved=%v", calls, unresolved)
					}
				})
			}
		}
	}
}

func TestScopedTransportOptionalAndGeneratorBindings(t *testing.T) {
	for _, name := range []string{"apiFetch", "apiFetchWithMeta"} {
		t.Run("optional transport inside wrapper "+name, func(t *testing.T) {
			source := strings.ReplaceAll(scopedTransportSource, "apiFetch", name)
			source = strings.Replace(source, "  return "+name, "  function dispatch("+name+"?: Callback) {\n    if (typeof "+name+" === 'function') return "+name, 1)
			source = strings.Replace(source, "  })\n}", "  })\n  }\n  return dispatch(otherTransport)\n}", 1)
			calls, unresolved, _ := parseTS(t, source+"scopedRequest(scope, '/v1/widgets', { method: 'POST' })\n")
			if len(calls) != 0 || len(unresolved) != 1 || !strings.HasPrefix(unresolved[0].path, "'/v1/widgets'") {
				t.Fatalf("callback transport must not credit the shared transport: calls=%v unresolved=%v", calls, unresolved)
			}
		})
	}
	for _, name := range []string{"scopedRequest", "apiFetch", "apiFetchWithMeta"} {
		for _, declaration := range []string{
			"function useLocal(" + name + "?: Callback) {",
			"const useLocal = (" + name + " /* note */ ? : Callback) => {",
			"function useLocal() { function* " + name + "(scope: RequestScope, path: string, options: RequestOptions) {}",
			"function useLocal() { function *" + name + "(scope: RequestScope, path: string, options: RequestOptions) {}",
			"function useLocal() { function/* note */*" + name + "(scope: RequestScope, path: string, options: RequestOptions) {}",
		} {
			t.Run(declaration, func(t *testing.T) {
				source := scopedTransportSource + declaration + "\n  if (typeof " + name + " === 'function') return scopedRequest(scope, '/v1/widgets', { method: 'POST' })\n}\n"
				calls, unresolved, _ := parseTS(t, source)
				if len(calls) != 0 || len(unresolved) != 1 || !strings.HasPrefix(unresolved[0].path, "'/v1/widgets'") || unresolved[0].line != 2+strings.Count(scopedTransportSource, "\n") {
					t.Fatalf("binding must keep only the actual caller unresolved: calls=%v unresolved=%v", calls, unresolved)
				}
			})
		}
	}
}
