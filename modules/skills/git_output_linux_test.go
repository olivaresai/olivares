// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build linux

package skills

import (
	"bytes"
	"context"
	"io"
	"os"
	"strconv"
	"testing"
	"time"
)

func TestSkillsGitOutputBoundedBeforeAllocation(t *testing.T) {
	if count := os.Getenv("SKILLSC_GIT_OUTPUT_BYTES"); count != "" {
		n, err := strconv.Atoi(count)
		if err != nil {
			os.Exit(2)
		}
		if _, err := os.Stdout.Write(bytes.Repeat([]byte{'x'}, n)); err != nil {
			os.Exit(3)
		}
		os.Exit(0)
	}
	const limit = 32 << 10
	for _, count := range []int{limit, limit * 4} {
		t.Run(strconv.Itoa(count), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			cmd, err := boundedGitCommand(ctx, os.Args[0], "-test.run=^TestSkillsGitOutputBoundedBeforeAllocation$")
			if err != nil {
				t.Fatal(err)
			}
			cmd.Env = []string{"GOMAXPROCS=2", "SKILLSC_GIT_OUTPUT_BYTES=" + strconv.Itoa(count)}
			out := limitedBuffer{max: limit}
			cmd.Stdout, cmd.Stderr = &out, io.Discard
			err = cmd.Run()
			stopGitGroup(cmd)
			if out.data.Len() > limit || out.data.Cap() > limit {
				t.Fatalf("child stdout exceeded its %d-byte limit: length=%d capacity=%d", limit, out.data.Len(), out.data.Cap())
			}
			if count == limit {
				if err != nil || out.exceeded || !bytes.Equal(out.data.Bytes(), bytes.Repeat([]byte{'x'}, limit)) {
					t.Fatalf("at-limit child output: bytes=%d exceeded=%v err=%v", out.data.Len(), out.exceeded, err)
				}
			} else if err == nil || !out.exceeded {
				t.Fatalf("oversize child output accepted: bytes=%d exceeded=%v err=%v", out.data.Len(), out.exceeded, err)
			}
		})
	}
}
