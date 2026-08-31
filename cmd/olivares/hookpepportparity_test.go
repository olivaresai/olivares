// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"os"
	"strings"
	"testing"
)

// Each engine's hook PEP listens on its OWN loopback port on purpose: the three
// response dialects differ, so one socket answering for another is not a smaller
// deployment, it is the wrong verdict shape reaching an agent that is deny-closed.
// Both files say so in their own comments.
//
// The help text is the ONLY place an operator learns which port to point at, so a
// help text naming another engine's port defeats exactly what the separate sockets
// exist to prevent. Measured on 2026-08-24: cmd_grokhook.go named 8448, which is
// Codex's PEP (codexhookpepserver.go) and also the inference proxy's default
// (inferenceproxy.go), while Grok's PEP listens on 8449.
//
// Nothing tied the two together, so the drift was silent. This does.
func TestEachHookHelpNamesItsOwnPEPPort(t *testing.T) {
	for _, tc := range []struct {
		name       string
		helpFile   string
		serverFile string
		envVar     string
		listenVar  string
	}{
		{"claude", "cmd_claudehook.go", "claudehookpep.go", "OLIVARES_HOOK_PEP_URL", "defaultHookPEPListen"},
		{"codex", "cmd_codexhook.go", "codexhookpepserver.go", "OLIVARES_CODEX_HOOK_URL", "defaultCodexHookPEPListen"},
		{"grok", "cmd_grokhook.go", "grokhookpepserver.go", "OLIVARES_GROK_HOOK_URL", "defaultGrokHookPEPListen"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			port := portFromListenConst(t, tc.serverFile, tc.listenVar)
			help, err := os.ReadFile(tc.helpFile)
			if err != nil {
				t.Fatalf("NO HE PODIDO MIRAR: read %s: %v", tc.helpFile, err)
			}
			line := lineContaining(string(help), tc.envVar)
			if line == "" {
				t.Fatalf("NO HE PODIDO MIRAR: %s does not document %s, so this test has no subject",
					tc.helpFile, tc.envVar)
			}
			if !strings.Contains(line, port) {
				t.Fatalf("%s help points the operator at the wrong PEP: the line for %s is %q "+
					"but %s.%s listens on :%s. A hook aimed at another engine's socket gets a "+
					"verdict in a dialect it cannot read, and it is deny-closed",
					tc.name, tc.envVar, strings.TrimSpace(line), tc.serverFile, tc.listenVar, port)
			}
		})
	}
}

func portFromListenConst(t *testing.T, file, constName string) string {
	t.Helper()
	src, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("NO HE PODIDO MIRAR: read %s: %v", file, err)
	}
	line := lineContaining(string(src), constName+" = ")
	if line == "" {
		t.Fatalf("NO HE PODIDO MIRAR: %s does not declare %s", file, constName)
	}
	_, after, ok := strings.Cut(line, "127.0.0.1:")
	if !ok {
		t.Fatalf("NO HE PODIDO MIRAR: %s is not a 127.0.0.1:<port> literal: %q", constName, line)
	}
	port, _, _ := strings.Cut(after, `"`)
	if port == "" {
		t.Fatalf("NO HE PODIDO MIRAR: empty port in %s", constName)
	}
	return port
}

func lineContaining(src, needle string) string {
	for _, line := range strings.Split(src, "\n") {
		if strings.Contains(line, needle) {
			return line
		}
	}
	return ""
}
