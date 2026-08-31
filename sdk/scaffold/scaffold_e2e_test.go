// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

// This file holds the heavyweight WithPlugin compile test. Unlike the normal
// gate's TestGeneratedRepoCompiles (zero-dep SDK only — no go.sum at all), the
// plugin variant pulls the hashicorp/go-plugin + gRPC + protobuf dependency
// tree, which NEEDS go.sum entries the generated repo does not ship. The test
// stays offline-deterministic by resolving them from the warm local module
// cache: GOPROXY=file://$(go env GOMODCACHE)/cache/download serves exactly the
// modules this workspace already downloaded to build sdk/plugin, and
// GOSUMDB=off skips the (unreachable) checksum-db round trip — the cache's
// own hashes vouch for the bits. Gated behind `-tags e2e` so the default
// module test stays hermetic and fast (mirrors core/runtime/plugin_e2e_test.go).
// Run it with:
//
//	go test -tags e2e -run TestGeneratedPluginRepo ./...
package scaffold_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/sdk/scaffold"
)

func TestGeneratedPluginRepoBuilds(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skipf("go toolchain not on PATH: %v", err)
	}
	modcacheRaw, err := exec.Command("go", "env", "GOMODCACHE").Output()
	if err != nil {
		t.Fatalf("go env GOMODCACHE: %v", err)
	}
	modcache := strings.TrimSpace(string(modcacheRaw))
	if modcache == "" {
		t.Fatal("go env GOMODCACHE is empty")
	}

	dir := filepath.Join(t.TempDir(), "repo")
	if err := scaffold.Generate(scaffold.Options{
		Dir:        dir,
		Name:       "acme.widget-audit",
		Module:     "example.com/acme/widget-audit",
		Kind:       scaffold.KindSource,
		WithPlugin: true,
		SDKPath:    sdkDir(t),
	}); err != nil {
		t.Fatalf("Generate: %v", err)
	}

	env := append(os.Environ(),
		"GOWORK=off",       // the generated repo must never resolve through this repo's go.work
		"GOFLAGS=-mod=mod", // go mod tidy may rewrite go.mod/go.sum of the generated module
		"GOSUMDB=off",      // offline: no checksum-db round trip; the local cache vouches
		"GOPROXY=file://"+filepath.ToSlash(filepath.Join(modcache, "cache", "download")),
	)
	// tidy first: it materializes the transitive plugin requires + go.sum from
	// the file:// cache proxy; then the build proves the whole tree compiles,
	// plugin main included.
	for _, args := range [][]string{
		{"mod", "tidy"},
		{"build", "./..."},
	} {
		cmd := exec.Command("go", args...)
		cmd.Dir = dir
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("go %s in the generated plugin repo failed: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
}
