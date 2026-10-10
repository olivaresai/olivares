// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build linux

package main

import (
	"archive/zip"
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptrace"
	"os"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

func TestSkillsCLIArchiveRefusesFIFOWithoutWaiting(t *testing.T) {
	prepareDatalaneCLITest(t)
	rec := newDatalaneRecorder(t, http.StatusCreated, `{"state":"catalog_published"}`)
	name := filepath.Join(t.TempDir(), "withheld.zip")
	if err := syscall.Mkfifo(name, 0600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, _, err := execDatalane(t, "", datalaneArgs(rec, "skills", "install", "--archive", name)...)
		done <- err
	}()
	select {
	case err := <-done:
		if exitcode.From(err) != exitcode.Usage || rec.count() != 0 {
			t.Fatalf("nonregular archive refusal: %v; requests=%d", err, rec.count())
		}
	case <-time.After(time.Second):
		// Release the controlled FIFO if the old preparation path opened it.
		f, err := os.OpenFile(name, os.O_RDWR|syscall.O_NONBLOCK, 0)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = f.Write([]byte("not an archive"))
		_ = f.Close()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Fatal("archive reader did not drain after releasing the fixture")
		}
		t.Fatal("CLI blocked opening a nonregular archive with no writer")
	}
}

func TestSkillsCLIImportCarriesDeadlineToRequest(t *testing.T) {
	prepareDatalaneCLITest(t)
	rec := newDatalaneRecorder(t, http.StatusCreated, `{"state":"catalog_published"}`)
	var archive bytes.Buffer
	z := zip.NewWriter(&archive)
	f, err := z.Create("research/SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(f, "---\nname: research\ndescription: A fixture\n---\nReviewed instruction.\n")
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	name := filepath.Join(t.TempDir(), "research.zip")
	if err := os.WriteFile(name, archive.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	root := newRootCmd()
	args := datalaneArgs(rec, "skills", "install", "--archive", name)
	cmd, _, err := root.Find(args)
	if err != nil {
		t.Fatal(err)
	}
	root.SetArgs(args)
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	var observed atomic.Bool
	ctx := httptrace.WithClientTrace(context.Background(), &httptrace.ClientTrace{WroteHeaders: func() {
		deadline, ok := cmd.Context().Deadline()
		observed.Store(ok && time.Until(deadline) > 0 && time.Until(deadline) <= time.Minute)
	}})
	if err := root.ExecuteContext(ctx); err != nil {
		t.Fatal(err)
	}
	if !observed.Load() || rec.count() != 1 {
		t.Fatalf("bounded import context did not reach the actual HTTP request: observed=%v requests=%d", observed.Load(), rec.count())
	}
}
