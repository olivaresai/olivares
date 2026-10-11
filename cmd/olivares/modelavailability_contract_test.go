// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/cmd/olivares/internal/toolinstall"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/sessions"
)

// This opt-in check measures native output with an empty owned home. It never
// opens host authentication and never sends a prompt or starts an inference turn.
func TestAutomaticToolCatalogRealGrokEmptyHome(t *testing.T) {
	if os.Getenv("OLIVARES_MODEL_CATALOG_CONTRACT") != "1" {
		t.Skip("native CLI catalog measurement is opt-in")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	catalog, err := toolinstall.NewCapabilityCatalog(toolinstall.NewCatalog(), toolinstall.NewGrok(toolinstall.GrokOptions{}))
	if err != nil {
		t.Fatal(err)
	}
	installer := toolinstall.NewEngineWithCapabilities(catalog, toolinstall.EngineOptions{InstallerVersion: "model-catalog-contract"})
	platform, _, err := toolinstall.PlatformV2For("grok", toolinstall.Platform{OS: runtime.GOOS, Arch: runtime.GOARCH})
	if err != nil {
		t.Fatal(err)
	}
	receipt, _, err := installer.InstallV2(ctx, toolinstall.RequestV2{Driver: "grok", Version: "1.0.46", DestRoot: t.TempDir(), Platform: platform}, nil, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	userHome := t.TempDir()
	configHome := filepath.Join(userHome, ".grok")
	if err := os.Mkdir(configHome, 0700); err != nil {
		t.Fatal(err)
	}
	callCtx, stop := context.WithTimeout(ctx, 20*time.Second)
	defer stop()
	cmd := exec.CommandContext(callCtx, receipt.Destination.Executable, "models")
	cmd.Dir = userHome
	cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + userHome, "GROK_HOME=" + configHome, "GROK_DISABLE_AUTOUPDATER=1"}
	raw, err := cmd.CombinedOutput()
	if len(raw) > 1<<20 {
		t.Fatal("native catalog exceeded its response bound")
	}
	if err != nil {
		t.Fatalf("empty-home models command: %v: %s", err, raw)
	}
	if !strings.Contains(string(raw), "You are not authenticated.") || !strings.Contains(string(raw), "Available models:") {
		t.Fatalf("native catalog output changed: %s", raw)
	}
	t.Logf("Grok %s empty-home model catalog (not signed-in availability):\n%s", receipt.Version, raw)

	// Exercise the product adapter against the same real executable in another
	// empty tenant home: the reference list must not become signed-in availability.
	m := sessions.New(sessions.WithRunner(sessions.NewProcRunner()), sessions.WithProviderDriver(sessions.NewGrokDriver()), sessions.WithDriverProgram("grok", receipt.Destination.Executable))
	m.UseToolLoginsRoot(t.TempDir())
	tenant := model.TenantID(model.NewID())
	_, ownConfigHome, ok := m.OwnToolLoginHomes(tenant, "grok")
	if !ok {
		t.Fatal("no product login home")
	}
	if err := os.MkdirAll(ownConfigHome, 0700); err != nil {
		t.Fatal(err)
	}
	if ids, err := m.ReadOwnToolLoginModels(ctx, tenant, "grok"); err == nil || len(ids) != 0 {
		t.Fatalf("signed-out native reference list advertised by the product: %v %v", ids, err)
	}
}
