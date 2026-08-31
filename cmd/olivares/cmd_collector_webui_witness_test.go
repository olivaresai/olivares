// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/webui"
)

func TestCollectorArgvWitnessEntersLoopAndHonorsCancellation(t *testing.T) {
	t.Setenv("OLIVARES_SOURCES_CONFIG", "")
	t.Setenv("OLIVARES_INGEST_TOKEN", "")

	var logs bytes.Buffer
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	defer slog.SetDefault(previousLogger)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	root := newRootCmd()
	root.SetContext(ctx)
	root.SetArgs([]string{"collector", "--core-addr", "127.0.0.1:1", "--insecure"})
	if _, err := root.ExecuteC(); err != nil {
		t.Fatalf("collector with canceled context: %v\nlogs:\n%s", err, logs.String())
	}
	for _, want := range []string{
		"collector: started; pushing observations to core",
		"collector: context canceled; draining",
	} {
		if !strings.Contains(logs.String(), want) {
			t.Fatalf("collector log does not contain %q:\n%s", want, logs.String())
		}
	}
}

func TestCollectorArgvWitnessMissingCoreAddressDoesNotStart(t *testing.T) {
	missingToken := filepath.Join(t.TempDir(), "missing-token")
	root := newRootCmd()
	root.SetArgs([]string{"collector", "--token-file", missingToken, "--insecure"})
	_, err := root.ExecuteC()
	if err == nil {
		t.Fatal("collector without --core-addr succeeded")
	}
	if !strings.Contains(err.Error(), `required flag(s) "core-addr" not set`) {
		t.Fatalf("collector without --core-addr error = %q, want the required-flag refusal", err)
	}
	if strings.Contains(err.Error(), "read token file") {
		t.Fatalf("collector entered RunE before validating --core-addr: %v", err)
	}
}

func TestWebUIFilesArgvWitnessListsEveryEmbeddedAssetAndExactCount(t *testing.T) {
	wantPaths := embeddedWebAssetPaths(t)
	root := newRootCmd()
	var stdout, stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs([]string{"webui-files", "--output", "text"})
	if _, err := root.ExecuteC(); err != nil {
		t.Fatalf("webui-files: %v\nstderr:\n%s", err, stderr.String())
	}

	lines := strings.Split(strings.TrimSuffix(stdout.String(), "\n"), "\n")
	if len(lines) != len(wantPaths)+1 {
		t.Fatalf("webui-files emitted %d line(s), want %d asset line(s) plus one count:\n%s",
			len(lines), len(wantPaths), stdout.String())
	}
	for i, want := range wantPaths {
		if lines[i] != want {
			t.Fatalf("webui-files asset %d = %q, want %q", i, lines[i], want)
		}
	}
	wantCount := fmt.Sprintf("%d embedded web asset(s)", len(wantPaths))
	if got := lines[len(lines)-1]; got != wantCount {
		t.Fatalf("webui-files count = %q, want %q", got, wantCount)
	}
}

func TestWebUIFilesArgvWitnessDoesNotFireForNeighborCommand(t *testing.T) {
	root := newRootCmd()
	var stdout, stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs([]string{"version", "--output", "text"})
	if _, err := root.ExecuteC(); err != nil {
		t.Fatalf("version neighbor: %v\nstderr:\n%s", err, stderr.String())
	}
	if strings.Contains(stdout.String()+stderr.String(), "embedded web asset(s)") {
		t.Fatalf("webui-files diagnostic fired for version argv:\n%s%s", stdout.String(), stderr.String())
	}
	if !strings.HasPrefix(stdout.String(), "olivares ") {
		t.Fatalf("version neighbor did not execute its own behavior: %q", stdout.String())
	}
}

func embeddedWebAssetPaths(t *testing.T) []string {
	t.Helper()
	var paths []string
	if err := fs.WalkDir(webui.FS(), ".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			paths = append(paths, path)
		}
		return nil
	}); err != nil {
		t.Fatalf("walk embedded web UI: %v", err)
	}
	if len(paths) == 0 {
		t.Fatal("embedded web UI contains no assets")
	}
	return paths
}
