// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package base

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/appliance/answers/carriers"
)

// appliance-firstboot hands these adapters carriers.ExecRunner on a Host rooted at "/", so a
// program they run that is missing from its closed list would be refused on every appliance.
// Here they run on a scratch root with a recording runner; a program they name under that
// root is the one they name under "/".
func TestAdapters_RunOnlyProgramsExecRunnerRuns(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{filepath.Dir(productBinary), filepath.Dir(productDropIn), ProductDataDir} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o750); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, productBinary), nil, 0o700); err != nil {
		t.Fatal(err)
	}
	var names []string
	record := func(_ context.Context, name string, args ...string) ([]byte, error) {
		names = append(names, name)
		switch {
		case name == "cloud-init":
			return []byte(`{"status": "running", "errors": []}`), nil
		case name == "id":
			return []byte(strconv.Itoa(os.Getuid()) + "\n"), nil
		case name == "systemctl" && len(args) > 0 && args[0] == "is-enabled":
			return []byte("enabled\n"), nil
		case name == "systemctl" && len(args) > 0 && args[0] == "is-active":
			return []byte("inactive\n"), nil
		}
		return nil, nil
	}
	host := Host{Root: root, Run: record}
	in := Input{Answers: Answers{StorageProfile: "single-node-prod", PublicConsoleURL: "https://olivares.example.test"}}
	ctx := context.Background()
	_, _ = CloudInitHost{Host: host}.Apply(ctx, in)
	_, _ = ProductConfig{Host: host}.Apply(ctx, in)
	_, _ = Storage{Host: host}.Apply(ctx, in)
	_, _ = ProductService{Host: host}.Apply(ctx, in)
	_ = ProductService{Host: host}.Verify(ctx, in, "")
	_, _ = ProductReadiness{Host: host}.Measure(ctx, in)

	want := "cloud-init " + filepath.Join(root, productBinary) + " systemctl id systemctl systemctl systemctl systemctl"
	if got := strings.Join(names, " "); got != want {
		t.Fatalf("the adapters ran %q, want %q", got, want)
	}
	done, cancel := context.WithCancel(ctx)
	cancel()
	for _, name := range names {
		if rel, ok := strings.CutPrefix(name, root); ok {
			name = rel
		}
		if _, err := carriers.ExecRunner(done, name); errors.Is(err, carriers.ErrUnlistedProgram) {
			t.Errorf("the adapters run %s, which carriers.ExecRunner refuses: %v", name, err)
		}
	}
}
