// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build e2e

// This file holds the heavyweight out-of-process plugin test. It builds the
// reference source connector into a real plugin binary, launches it as a
// separate process and verifies the full go-plugin path: handshake, streaming
// Gather over gRPC, and process teardown on Stop. It is gated behind `-tags e2e`
// so the default `task test` stays hermetic and fast; the fast contract test for
// the gRPC adapters lives in sdk/plugin (TestPluginGRPCConn). Run it with:
//
//	go test -tags e2e -run TestOutOfProcess ./core/runtime/...
package runtime_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/runtime"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/event"
)

func TestOutOfProcessSourcePlugin(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not on PATH; skipping out-of-process plugin build")
	}

	// Build the example-source connector into a plugin binary.
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	repoRoot := filepath.Clean(filepath.Join(wd, "..", ".."))
	// execCapableDir y no t.TempDir(): este caso COMPILA un binario y lo lanza fuera de proceso,
	// asi que necesita un directorio donde de verdad se pueda ejecutar. Con t.TempDir() en un
	// montaje noexec el fallo sale como «el loader no arranco el plugin» — acusando al codigo por
	// una propiedad de la maquina. Es el mismo defecto que loader_test.go tenia, medido el
	// 2026-08-19 en ci-runner-8.
	bin := filepath.Join(execCapableDir(t), "example-source")
	build := exec.Command("go", "build", "-o", bin, "github.com/olivaresai/olivares/connectors/example/cmd/example-source")
	build.Dir = repoRoot
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build plugin: %v\n%s", err, out)
	}

	rt := runtime.New(runtime.Options{Logger: quiet()})
	mod := &fakeModule{name: "counter", got: make(chan event.Event, 16)}
	if err := rt.AddModule(mod, sdk.Config{}); err != nil {
		t.Fatal(err)
	}
	// Load the connector out-of-process; it emits 4 edges over gRPC.
	if err := rt.LoadSourcePlugin(bin, sdk.Config{Settings: map[string]string{"count": "4"}}, "tenant-oop"); err != nil {
		t.Fatalf("load plugin: %v", err)
	}

	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = rt.Stop(ctx) // also kills the plugin process
	})

	for i := 0; i < 4; i++ {
		select {
		case e := <-mod.got:
			if e.Source != "olivares.example-source" || e.Tenant != "tenant-oop" {
				t.Errorf("unexpected event from plugin: %+v", e)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("did not receive edge %d from out-of-process plugin", i)
		}
	}
}
