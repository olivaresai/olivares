// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// businessDockerfiles are the images the Business overlay adds at the repository root
// (deployment-overlay/Dockerfile.fips, .stig). The Community tree does not carry them
// since the fleet move; the assembled Business tree does, and holds them to the same
// contract as the Community images.
var businessDockerfiles = []string{"Dockerfile.fips", "Dockerfile.stig"}

// containerDockerfiles returns the Dockerfiles this tree ships: the Community ones, which
// must exist (a missing one fails its subtest on the read), and the Business ones. A
// Community tree does not carry the Business ones; any other edition must, or the
// contract would go unchecked without a failure.
func containerDockerfiles(t *testing.T, community ...string) []string {
	t.Helper()
	names := append([]string(nil), community...)
	for _, name := range businessDockerfiles {
		_, err := os.Stat(filepath.Join("..", "..", name))
		switch {
		case err == nil:
			names = append(names, name)
		case !errors.Is(err, fs.ErrNotExist):
			t.Fatal(err)
		case thisEdition.name != "community":
			t.Errorf("the %s build must carry %s from the Business overlay", thisEdition.name, name)
		}
	}
	return names
}

// Container entrypoints must leave listener policy to serve's shared defaults.
// The artifact qualification probes both address families on the running image.
func TestContainerUsesDefaultListeners(t *testing.T) {
	for _, name := range containerDockerfiles(t, "Dockerfile", "Dockerfile.release") {
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
