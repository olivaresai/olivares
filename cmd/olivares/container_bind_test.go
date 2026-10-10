// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Container entrypoints must leave listener policy to serve's shared defaults.
// The artifact qualification probes both address families on the running image.
func TestContainerUsesDefaultListeners(t *testing.T) {
	for _, name := range []string{"Dockerfile", "Dockerfile.release", "Dockerfile.fips", "Dockerfile.stig"} {
		t.Run(name, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join("..", "..", name))
			if err != nil {
				t.Fatal(err)
			}
			var args []string
			for _, line := range strings.Split(string(data), "\n") {
				if rest, ok := strings.CutPrefix(line, "CMD "); ok {
					if err := json.Unmarshal([]byte(rest), &args); err != nil {
						t.Fatal(err)
					}
				}
			}
			if len(args) == 0 || args[0] != "serve" {
				t.Fatalf("expected serve CMD, got %q", args)
			}
			for _, arg := range args[1:] {
				flag, _, _ := strings.Cut(arg, "=")
				if flag == "--listen" || flag == "--grpc-listen" {
					t.Errorf("CMD overrides %s; inherit serve's dual-stack default", flag)
				}
			}
		})
	}
}
