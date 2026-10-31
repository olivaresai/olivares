// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConsoleCensusResolvesExportedPaths(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "api.ts")
	source := "export const BASE = '/v1/settings'\nhttp.get(BASE)\n"
	if err := os.WriteFile(file, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	calls, _, _ := parseConsoleClientCalls(t, root)
	if len(calls) != 1 || calls[0].path != "/v1/settings" {
		t.Fatalf("exported constant: %v", calls)
	}
}

func TestConsoleCensusResolvesImportedStreamPaths(t *testing.T) {
	for _, tc := range []struct {
		name, declaration, suffix, importLine string
		want                                  bool
	}{
		{"exported", "export const", "", "import { STREAM as PATH } from './api'", true},
		{"unexported", "const", "", "import { STREAM as PATH } from './api'", false},
		{"line comment", "// export const", "", "import { STREAM as PATH } from './api'", false},
		{"block comment", "/* export const", "", "import { STREAM as PATH } from './api'", false},
		{"compound", "export const", " + dynamic", "import { STREAM as PATH } from './api'", false},
		{"type import", "export const", "", "import type { STREAM as PATH } from './api'", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, "api.ts"), []byte(tc.declaration+" STREAM = '/v1/events'"+tc.suffix+"\n"), 0600); err != nil {
				t.Fatal(err)
			}
			file := filepath.Join(root, "stream.ts")
			source := tc.importLine + "\nsubscribeStream({ path: PATH, token })\n"
			if err := os.WriteFile(file, []byte(source), 0600); err != nil {
				t.Fatal(err)
			}
			_, _, streams := parseConsoleClientCalls(t, root)
			if !tc.want {
				if len(streams) != 0 {
					t.Fatalf("non-exported binding supplied coverage: %v", streams)
				}
				return
			}
			if len(streams) != 1 || streams[0].path != "/v1/events" {
				t.Fatalf("imported stream path: %v", streams)
			}
			if err := os.WriteFile(file, []byte(strings.ReplaceAll(source, "subscribeStream", "withoutTransport")), 0600); err != nil {
				t.Fatal(err)
			}
			_, _, streams = parseConsoleClientCalls(t, root)
			if len(streams) != 0 {
				t.Fatalf("import without transport supplied coverage: %v", streams)
			}
		})
	}
}

func TestConsoleTransportWrapperNamedAfterHTTPMethod(t *testing.T) {
	root := t.TempDir()
	source := "post('/v1/login', input)\nasync function post(path: string, input: object) {\n return http.post(path, input)\n}\n"
	if err := os.WriteFile(filepath.Join(root, "api.ts"), []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	calls, unresolved, _ := parseConsoleClientCalls(t, root)
	if len(unresolved) != 0 || len(calls) != 1 || calls[0].method != "POST" || calls[0].path != "/v1/login" {
		t.Fatalf("calls=%v unresolved=%v", calls, unresolved)
	}
}
