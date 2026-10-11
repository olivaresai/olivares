// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only

//go:build !enterprise

package main

import (
	"crypto/sha256"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

func TestCommunityAuditRecoverArchiveUnavailableBeforeBoot(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run(map[bool]string{false: "fresh", true: "existing"}[existing], func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "not-created")
			tenant := "22222222-2222-7222-8222-222222222222"
			pubSpec := strings.Repeat("A", 43) + "="
			if existing {
				f := newRecoveryCLIFixture(t, true, false)
				dir, tenant, pubSpec = f.dataDir, f.tenant.String(), f.pubSpec
			}
			archiveDir := filepath.Join(t.TempDir(), "archive")
			before := recoveryDirectorySnapshot(t, dir)
			archiveBefore := recoveryDirectorySnapshot(t, archiveDir)
			cmd := newAuditCmd()
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			cmd.SetArgs([]string{"recover", "--tenant", tenant, "--pubkey", pubSpec, "--data-dir", dir, "--archive-dir", archiveDir})
			err := cmd.Execute()
			if err == nil || !strings.Contains(err.Error(), "Business") || exitcode.From(err) != exitcode.Edition {
				t.Errorf("archive recovery = %v (exit %d), want Business refusal, exit 9", err, exitcode.From(err))
			}
			if after := recoveryDirectorySnapshot(t, dir); !reflect.DeepEqual(before, after) {
				t.Errorf("archive recovery changed data directory %s", dir)
			}
			if after := recoveryDirectorySnapshot(t, archiveDir); !reflect.DeepEqual(archiveBefore, after) {
				t.Errorf("archive recovery changed archive directory %s", archiveDir)
			}
		})
	}
}

func recoveryDirectorySnapshot(t *testing.T, dir string) map[string][32]byte {
	t.Helper()
	files := make(map[string][32]byte)
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if os.IsNotExist(err) && path == dir {
			return nil
		}
		if err != nil {
			return err
		}
		if info.IsDir() {
			files[path] = [32]byte{}
			return nil
		}
		data, err := os.ReadFile(path)
		if err == nil {
			files[path] = sha256.Sum256(data)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func TestCommunityAuditExportCommandsUnavailable(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "not-created")
	for _, args := range [][]string{
		{"export", "--tenant", "22222222-2222-7222-8222-222222222222", "--data-dir", dir},
		{"archive", "export", "--tenant", "22222222-2222-7222-8222-222222222222", "--data-dir", dir, "--out", dir},
		{"archive", "verify", "--dir", dir, "--strict"},
	} {
		cmd := newAuditCmd()
		cmd.SetOut(io.Discard)
		cmd.SetErr(io.Discard)
		cmd.SetArgs(args)
		err := cmd.Execute()
		if err == nil || !strings.Contains(err.Error(), "Business") || exitcode.From(err) != exitcode.Edition {
			t.Errorf("%v = %v (exit %d), want Business refusal, exit 9", args, err, exitcode.From(err))
		}
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("unavailable command touched data directory: %v", err)
	}
}

func TestCommunityDirectoryArchiveRefusesStartup(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "not-created")
	loop, err := newAuditArchiveLoop(auditArchiveConfig{sink: "dir", dir: dir}, nil, nil, nil, discardLog())
	if loop != nil || err == nil || !strings.Contains(err.Error(), "Business") || !strings.Contains(err.Error(), auditArchiveSinkEnv) {
		t.Fatalf("loop = %v %v", loop, err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("refusal created archive: %v", err)
	}
}
