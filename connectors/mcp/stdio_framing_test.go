// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

type stdioPadding struct{}

func (stdioPadding) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = ' '
	}
	return len(p), nil
}

type stdioCountReader struct {
	io.Reader
	read int
}

func (r *stdioCountReader) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	r.read += n
	return n, err
}

func TestStdioFramingRejectsExcessWithoutDiscardOrRecovery(t *testing.T) {
	const ceiling = 32 << 20
	r := &stdioCountReader{Reader: io.MultiReader(strings.NewReader("stray"), io.LimitReader(stdioPadding{}, ceiling+(1<<20)), strings.NewReader("\n{\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{}}\n"))}
	tr := &stdioTransport{out: bufio.NewReaderSize(r, 4096)}
	result, err := tr.readResponse(1)
	if err == nil || len(result) != 0 {
		t.Fatalf("oversized stray output returned a successful response (%d bytes)", len(result))
	}
	if r.read > ceiling+2+4096 {
		t.Fatalf("read %d bytes before refusing excess", r.read)
	}
	result, nextErr := tr.readResponse(1)
	if nextErr == nil || len(result) != 0 {
		t.Fatal("refused framing later returned a poisoned success")
	}
}

func TestStdioFramingCeilingAndMatching(t *testing.T) {
	const ceiling = 32 << 20
	const prefix = `{"jsonrpc":"2.0","id":1,"result":{"ok":true}}`
	for _, ending := range []string{"\n", "\r\n", ""} {
		tr := &stdioTransport{out: bufio.NewReader(io.MultiReader(strings.NewReader(prefix), io.LimitReader(stdioPadding{}, ceiling-int64(len(prefix))), strings.NewReader(ending)))}
		result, err := tr.readResponse(1)
		if err != nil || string(result) != `{"ok":true}` {
			t.Fatalf("ceiling frame ending %q: result %q, error %v", ending, result, err)
		}
	}
	tr := &stdioTransport{out: bufio.NewReader(strings.NewReader("stray\n{\"jsonrpc\":\"2.0\",\"method\":\"notifications/initialized\"}\n{\"jsonrpc\":\"2.0\",\"id\":2,\"result\":{}}\n" + prefix + "\n"))}
	result, err := tr.readResponse(1)
	if err != nil || string(result) != `{"ok":true}` {
		t.Fatalf("matching response = %q, %v", result, err)
	}
}

type stdioFailedReader struct{ err error }

func (r stdioFailedReader) Read([]byte) (int, error) { return 0, r.err }
func TestStdioFramingSurfacesEOFAndReaderFailure(t *testing.T) {
	for _, failure := range []error{io.EOF, errors.New("fixture read failure")} {
		tr := &stdioTransport{out: bufio.NewReader(stdioFailedReader{failure})}
		if result, err := tr.readResponse(1); len(result) != 0 || !errors.Is(err, failure) {
			t.Fatalf("result %q, error %v, want %v", result, err, failure)
		}
	}
}

type stdioDataFailure struct{ err error }

func (r stdioDataFailure) Read(p []byte) (int, error) {
	return copy(p, `{"jsonrpc":"2.0","id":1,"result":{}}`), r.err
}
func TestStdioFramingNeverAcceptsAPartialFailedRead(t *testing.T) {
	failure := errors.New("fixture interrupted record")
	tr := &stdioTransport{out: bufio.NewReader(stdioDataFailure{failure})}
	if result, err := tr.readResponse(1); len(result) != 0 || !errors.Is(err, failure) {
		t.Fatalf("partial read returned %q, %v", result, err)
	}
}

func TestStdioFramingCancellationDuringUnterminatedOutput(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	tr, err := newStdioTransport(ctx, serverSpec{Command: os.Args[0], Args: []string{"-test.run=^TestStdioFramingSlowChild$"}, Env: map[string]string{"OLIVARES_MCP_FRAMING_HELPER": "1"}})
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()
	if _, err := tr.readResponse(1); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := tr.readResponse(2); done <- err }()
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancellation returned success")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("canceled framing did not unblock")
	}
}
func TestStdioFramingSlowChild(t *testing.T) {
	if os.Getenv("OLIVARES_MCP_FRAMING_HELPER") != "1" {
		return
	}
	fmt.Fprintln(os.Stdout, `{"jsonrpc":"2.0","id":1,"result":{}}`)
	fmt.Fprint(os.Stdout, strings.Repeat(" ", 512<<10))
	time.Sleep(30 * time.Second)
	os.Exit(0)
}
