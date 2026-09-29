// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package services

import (
	"context"
	"errors"
	"slices"
	"testing"
)

func TestExecJournal_RefusesForeignVectorsBeforeSpawning(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	valid := LogArgv("olivares.service", 20)
	replace := func(index int, value string) []string { argv := slices.Clone(valid); argv[index] = value; return argv }
	for name, argv := range map[string][]string{
		"empty":                nil,
		"short":                valid[:4],
		"extra flag":           append(slices.Clone(valid), "--directory=/tmp"),
		"foreign executable":   replace(0, "/bin/sh"),
		"relative executable":  replace(0, "journalctl"),
		"foreign unit option":  replace(1, "--file=/tmp/journal"),
		"unit path":            replace(1, "--unit=/etc/passwd"),
		"unit glob":            replace(1, "--unit=*.service"),
		"unit flag":            replace(1, "--unit=--output=cat"),
		"unit newline":         replace(1, "--unit=ssh.service\n"),
		"foreign format":       replace(2, "--output=cat"),
		"foreign lines option": replace(3, "--since=1970"),
		"zero lines":           replace(3, "--lines=0"),
		"negative lines":       replace(3, "--lines=-1"),
		"too many lines":       replace(3, "--lines=501"),
		"all lines":            replace(3, "--lines=all"),
		"noncanonical lines":   replace(3, "--lines=020"),
		"signed lines":         replace(3, "--lines=+20"),
		"overflow lines":       replace(3, "--lines=999999999999999999999999999999"),
		"pager":                replace(4, "--pager-end"),
	} {
		if _, err := ExecJournal(ctx, argv, MaxJournalBytes); err == nil || err.Error() != "not the journal reader's argument vector" {
			t.Errorf("%s: got %v, want refusal before spawning", name, err)
		}
	}
	for _, limit := range []int64{-1, 0, MaxJournalBytes + 1} {
		if _, err := ExecJournal(ctx, valid, limit); err == nil || err.Error() != "the journal reader's byte limit is out of range" {
			t.Errorf("limit %d: got %v, want refusal before spawning", limit, err)
		}
	}
	// Guard success reaches only the canceled context, never a host journal process.
	for _, unit := range []string{"ssh.service", "olivares.service", "worker@one.service", "systemd-journald.service"} {
		for _, lines := range []int{1, DefaultLogLines, MaxLogLines} {
			for _, limit := range []int64{1, MaxJournalBytes} {
				argv := LogArgv(unit, lines)
				before := slices.Clone(argv)
				if _, err := ExecJournal(ctx, argv, limit); !errors.Is(err, context.Canceled) {
					t.Errorf("valid %q limit %d must reach the canceled context: %v", argv, limit, err)
				}
				if !slices.Equal(argv, before) {
					t.Errorf("adapter changed caller argv: %q", argv)
				}
			}
		}
	}
}
