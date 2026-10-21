// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

const observationPathShape = "/v1/m/inventory/entities/{}/{}/observations"

// inventoryObservationsSource is the real Inventory history wrapper shape: a local
// observationPath helper, a generic .get, options, and a chained .then. between is
// only the text between the http receiver and the member-access dot.
func inventoryObservationsSource(between string) string {
	return "function observationPath(kind: string, id: string): string {\n" +
		"  return `/v1/m/inventory/entities/${encodeURIComponent(kind)}/${encodeURIComponent(id)}/observations`\n" +
		"}\n" +
		"\n" +
		"export const inventoryApi = {\n" +
		"  observations: (\n" +
		"    kind: string,\n" +
		"    id: string,\n" +
		"    opts: { tenant?: string; signal?: AbortSignal; cursor?: string },\n" +
		"  ) => {\n" +
		"    const query: { limit: number; cursor?: string } = {\n" +
		"      limit: 25,\n" +
		"    }\n" +
		"    return http" + between + ".get<ObservationPage>(observationPath(kind, id), {\n" +
		"        tenant: opts.tenant,\n" +
		"        signal: opts.signal,\n" +
		"        query,\n" +
		"      })\n" +
		"      .then((page) => page)\n" +
		"  },\n" +
		"}\n"
}

func parseTS(t *testing.T, source string) (calls, unresolved, raw []clientCall) {
	t.Helper()
	abs := t.TempDir()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	root, err := filepath.Rel(cwd, abs)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "api.ts"), []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	return parseConsoleClientCalls(t, root)
}

func httpTokenLine(src string) int {
	idx := strings.Index(src, "http")
	if idx < 0 {
		return -1
	}
	return 1 + strings.Count(src[:idx], "\n")
}

func callsNamed(calls []clientCall, method, path string) []clientCall {
	var out []clientCall
	for _, c := range calls {
		if c.method == method && c.path == path {
			out = append(out, c)
		}
	}
	return out
}

func dumpClientCalls(t *testing.T, calls, unresolved, raw []clientCall) {
	t.Helper()
	t.Logf("calls=%d unresolved=%d raw=%d", len(calls), len(unresolved), len(raw))
	for _, c := range calls {
		t.Logf("call %s %s %s:%d", c.method, c.path, c.file, c.line)
	}
	for _, c := range unresolved {
		t.Logf("unresolved %s %s %s:%d", c.method, c.path, c.file, c.line)
	}
	for _, c := range raw {
		t.Logf("raw %s %s %s:%d", c.method, c.path, c.file, c.line)
	}
}

func requireObservationOnce(t *testing.T, src string, calls, unresolved, raw []clientCall) clientCall {
	t.Helper()
	hits := callsNamed(calls, "GET", observationPathShape)
	if len(hits) != 1 || len(calls) != 1 || len(unresolved) != 0 || len(raw) != 0 {
		dumpClientCalls(t, calls, unresolved, raw)
		t.Fatalf("want exactly one resolved GET %s and no unresolved/raw, got calls=%d hits=%d unresolved=%d raw=%d",
			observationPathShape, len(calls), len(hits), len(unresolved), len(raw))
	}
	got := hits[0]
	wantLine := httpTokenLine(src)
	if got.line != wantLine {
		t.Fatalf("attributed to line %d, want original receiver line %d", got.line, wantLine)
	}
	if !strings.HasSuffix(got.file, "api.ts") {
		t.Fatalf("file attribution lost: %q", got.file)
	}
	return got
}

func TestParseConsoleClientCallsDiscoversWhitespaceBeforeDot(t *testing.T) {
	t.Run("inventory helper newline and single-line are the same method and path", func(t *testing.T) {
		wrapped := inventoryObservationsSource("\n      ")
		single := inventoryObservationsSource("")
		wCalls, wU, wR := parseTS(t, wrapped)
		sCalls, sU, sR := parseTS(t, single)
		w := requireObservationOnce(t, wrapped, wCalls, wU, wR)
		s := requireObservationOnce(t, single, sCalls, sU, sR)
		if w.method != s.method || w.path != s.path {
			t.Fatalf("method/path diverged: wrapped %s %s vs single-line %s %s",
				w.method, w.path, s.method, s.path)
		}
	})

	t.Run("crlf at the receiver-dot boundary", func(t *testing.T) {
		src := inventoryObservationsSource("\r\n      ")
		calls, unresolved, raw := parseTS(t, src)
		requireObservationOnce(t, src, calls, unresolved, raw)
	})

	t.Run("tab at the receiver-dot boundary", func(t *testing.T) {
		src := inventoryObservationsSource("\t")
		calls, unresolved, raw := parseTS(t, src)
		requireObservationOnce(t, src, calls, unresolved, raw)
	})

	t.Run("spaces at the receiver-dot boundary", func(t *testing.T) {
		src := inventoryObservationsSource("  ")
		calls, unresolved, raw := parseTS(t, src)
		requireObservationOnce(t, src, calls, unresolved, raw)
	})

	t.Run("WithMeta generic keeps verb and normalized path", func(t *testing.T) {
		src := "export const createHold = (id: string, body: unknown) =>\n" +
			"  http.postWithMeta<LegalHold>(`/v1/m/compliance/holds/${encodeURIComponent(id)}`, body)\n"
		calls, unresolved, raw := parseTS(t, src)
		hits := callsNamed(calls, "POST", "/v1/m/compliance/holds/{}")
		if len(hits) != 1 || len(calls) != 1 || len(unresolved) != 0 || len(raw) != 0 {
			dumpClientCalls(t, calls, unresolved, raw)
			t.Fatal("WithMeta generic lost its verb or normalized path")
		}
	})

	t.Run("wrapped Raw generic keeps verb and normalized path", func(t *testing.T) {
		src := "export const exportEntity = (id: string) =>\n" +
			"  http\n" +
			"    .getRaw<Blob>(`/v1/m/inventory/entities/${encodeURIComponent(id)}/export`)\n"
		calls, unresolved, raw := parseTS(t, src)
		hits := callsNamed(calls, "GET", "/v1/m/inventory/entities/{}/export")
		if len(hits) != 1 || len(calls) != 1 || len(unresolved) != 0 || len(raw) != 0 {
			dumpClientCalls(t, calls, unresolved, raw)
			t.Fatal("wrapped Raw generic lost its verb or normalized path")
		}
		if hits[0].line != httpTokenLine(src) {
			t.Fatalf("Raw call attributed to line %d, want receiver line %d",
				hits[0].line, httpTokenLine(src))
		}
	})

	t.Run("non-transport receiver loses typed coverage", func(t *testing.T) {
		src := strings.Replace(inventoryObservationsSource("\n      "), "return http", "return client", 1)
		calls, unresolved, raw := parseTS(t, src)
		hits := callsNamed(calls, "GET", observationPathShape)
		if len(hits) != 0 || len(calls) != 0 {
			dumpClientCalls(t, calls, unresolved, raw)
			t.Fatal("a non-transport receiver still counted as typed coverage")
		}
	})

	t.Run("comment-only example creates no coverage", func(t *testing.T) {
		src := inventoryObservationsSource("\n      ")
		src = strings.Replace(src, "    return http", "    // return http", 1)
		src = strings.Replace(src, "      .get<", "      // .get<", 1)
		calls, unresolved, raw := parseTS(t, src)
		hits := callsNamed(calls, "GET", observationPathShape)
		if len(hits) != 0 || len(calls) != 0 {
			dumpClientCalls(t, calls, unresolved, raw)
			t.Fatal("a comment-only example created typed coverage")
		}
	})

	t.Run("wrapped unknown helper stays unresolved on the receiver line", func(t *testing.T) {
		src := strings.Replace(inventoryObservationsSource("\n      "),
			"observationPath(kind, id)", "unknownHelper(kind, id)", 1)
		calls, unresolved, raw := parseTS(t, src)
		hits := callsNamed(calls, "GET", observationPathShape)
		if len(hits) != 0 {
			dumpClientCalls(t, calls, unresolved, raw)
			t.Fatal("unknown helper invented a valid route")
		}
		if len(unresolved) != 1 || len(calls) != 0 || len(raw) != 0 {
			dumpClientCalls(t, calls, unresolved, raw)
			t.Fatalf("unknown wrapped helper must remain unresolved once, got unresolved=%d calls=%d raw=%d",
				len(unresolved), len(calls), len(raw))
		}
		u := unresolved[0]
		if u.method != "GET" {
			t.Fatalf("unresolved method %q, want GET", u.method)
		}
		if !strings.Contains(u.path, "unknownHelper") {
			t.Fatalf("unresolved path %q does not name the helper", u.path)
		}
		if !strings.Contains(u.path, "identifier is not a literal path constant") {
			t.Fatalf("unresolved path %q lost the unknown-helper classification", u.path)
		}
		if u.line != httpTokenLine(src) {
			t.Fatalf("unresolved attributed to line %d, want receiver line %d", u.line, httpTokenLine(src))
		}
		if !strings.HasSuffix(u.file, "api.ts") {
			t.Fatalf("unresolved file attribution lost: %q", u.file)
		}
	})
}

// The Business overlay is not formatted by web/.prettierrc.json (singleQuote), so its
// client calls spell the route as a double-quoted literal. The census must read them
// in every form it reads a single-quoted one: an unread call counts a surface the
// console has as missing (dev#2263, GET /v1/system/residency).
func TestDoubleQuotedPathLiteralsAreConsoleSurface(t *testing.T) {
	const source = "import { http } from '@/lib/api/client'\n" +
		"const BASE = \"/v1/m/exports\"\n" +
		"export const residencyApi = {\n" +
		"  getRegistry: () => http.get<Registry>(\"/v1/system/residency\"),\n" +
		"  setRegion: (id: string) => http.put<Org>(`/v1/system/orgs/${id}/region`, {}),\n" +
		"}\n" +
		"export const list = () => http.get<Rows>(`${BASE}/list`)\n" +
		"export const importRows = (raw: string) => apiFetch(\"/v1/m/imports\", { method: 'POST', rawBody: raw })\n" +
		"export const exportArchive = () => fetch(\"/v1/m/exports/archive\")\n" +
		"export const watch = () => subscribeStream({ path: \"/v1/m/exports/stream\", token })\n" +
		"// http.get(\"/v1/m/ghost\")\n" +
		"/* fetch(\"/v1/m/ghost-raw\") */\n" +
		"export const signIn = () => <a href=\"/v1/auth/federation/start\">sign in</a>\n" +
		"export const lone = \"/v1/m/not/a/call\"\n"
	calls, unresolved, raw := parseTS(t, source)
	var gotCalls, gotRaw []string
	for _, c := range calls {
		gotCalls = append(gotCalls, c.method+" "+c.path)
	}
	for _, r := range raw {
		gotRaw = append(gotRaw, r.path)
	}
	sort.Strings(gotCalls)
	sort.Strings(gotRaw)
	wantCalls := []string{
		"GET /v1/m/exports/list", "GET /v1/system/residency",
		"POST /v1/m/imports", "PUT /v1/system/orgs/{}/region",
	}
	wantRaw := []string{"/v1/m/exports/archive", "/v1/m/exports/stream"}
	if !mismasRutas(gotCalls, wantCalls) || !mismasRutas(gotRaw, wantRaw) || len(unresolved) != 0 {
		dumpClientCalls(t, calls, unresolved, raw)
		t.Fatalf("typed calls %v, raw calls %v, unresolved %d; want %v, %v and none",
			gotCalls, gotRaw, len(unresolved), wantCalls, wantRaw)
	}
}

// The imported-helper reader is the second place the census reads console source.
func TestDoubleQuotedImportedPathConstIsConsoleSurface(t *testing.T) {
	root := t.TempDir()
	for name, content := range map[string]string{
		"api.ts": "const BASE = \"/v1/m/dq\"\n" +
			"export function streamPath(ref: string): string {\n" +
			"  return `${BASE}/runs/${encodeURIComponent(ref)}/stream`\n}\n",
		"stream.ts": "import { streamPath } from './api'\n" +
			"subscribeStream({ path: streamPath(ref), token })\n",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	_, unresolved, raw := parseConsoleClientCalls(t, root)
	if len(unresolved) != 0 || len(raw) != 1 || raw[0].path != "/v1/m/dq/runs/{}/stream" {
		t.Fatalf("imported double-quoted constant not resolved: raw=%v unresolved=%v", raw, unresolved)
	}
}
