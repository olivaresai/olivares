// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package toolinstall

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/cmd/olivares/internal/toolinstall/toolinstalltest"
)

// The defect these tests close: a 163 MB download reported nothing while it ran.

// TestFetchProgressIsTransparent is the property that matters most. The provider
// computes the release digest from the bytes it writes through this wrapper, so
// a wrapper that altered, dropped or duplicated a byte would break verification
// of a signed release — a far worse defect than the one it fixes.
func TestFetchProgressIsTransparent(t *testing.T) {
	payload := bytes.Repeat([]byte("olivares"), 5000)
	var sink, out bytes.Buffer
	p := newFetchProgress(&sink, &out, int64(len(payload)))
	for i := 0; i < len(payload); i += 997 { // deliberately ragged writes
		end := i + 997
		if end > len(payload) {
			end = len(payload)
		}
		n, err := p.Write(payload[i:end])
		if err != nil {
			t.Fatal(err)
		}
		if n != end-i {
			t.Fatalf("short write reported: %d, want %d", n, end-i)
		}
	}
	if !bytes.Equal(sink.Bytes(), payload) {
		t.Fatal("the bytes that reached the file are not the bytes that were written")
	}
}

// TestFetchProgressReturnsTheWriterVerbatim: reporting must never be able to
// change the outcome of a fetch.
func TestFetchProgressReturnsTheWriterVerbatim(t *testing.T) {
	boom := errors.New("disk full")
	var out bytes.Buffer
	p := newFetchProgress(writerFunc(func(b []byte) (int, error) { return 3, boom }), &out, 100)
	n, err := p.Write([]byte("0123456789"))
	if n != 3 || !errors.Is(err, boom) {
		t.Fatalf("got (%d, %v), want the underlying writer's (3, disk full)", n, err)
	}
	// And a failed write reports nothing: a progress line after an error would
	// read as if the download were still going.
	if out.Len() != 0 {
		t.Fatalf("a failed write printed progress: %q", out.String())
	}
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(b []byte) (int, error) { return f(b) }

// TestFetchProgressRateLimitsItsReporting: a download is not a frame buffer.
func TestFetchProgressRateLimitsItsReporting(t *testing.T) {
	var sink, out bytes.Buffer
	p := newFetchProgress(&sink, &out, 0)
	now := time.Unix(0, 0)
	p.now = func() time.Time { return now }

	for i := 0; i < 100; i++ { // 100 writes inside one interval
		if _, err := p.Write([]byte("x")); err != nil {
			t.Fatal(err)
		}
	}
	if got := strings.Count(out.String(), "\n"); got != 1 {
		t.Fatalf("%d lines inside one interval, want the first one only", got)
	}
	now = now.Add(600 * time.Millisecond)
	if _, err := p.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(out.String(), "\n"); got != 2 {
		t.Fatalf("%d lines after the interval elapsed, want 2", got)
	}
}

// TestFetchProgressEndsOnTheWholeFigure: the last line an operator sees must be
// the total, not whatever the 500 ms interval happened to land on.
func TestFetchProgressEndsOnTheWholeFigure(t *testing.T) {
	var sink, out bytes.Buffer
	p := newFetchProgress(&sink, &out, 0)
	now := time.Unix(0, 0)
	p.now = func() time.Time { return now }
	if _, err := p.Write(bytes.Repeat([]byte("x"), 3<<20)); err != nil {
		t.Fatal(err)
	}
	p.done()
	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	if last := lines[len(lines)-1]; !strings.Contains(last, "3.0 MiB") {
		t.Fatalf("the final line is %q, want the whole figure", last)
	}
}

// TestFetchProgressOnlyClaimsAPercentageItHas. Grok declares a MAXIMUM size
// (SizeStateBounded) and not an exact one, so the 163 MB download of the first
// hour has no denominator. Inventing one from MaxSize would show a percentage
// that never reaches 100.
func TestFetchProgressOnlyClaimsAPercentageItHas(t *testing.T) {
	var sink, out bytes.Buffer
	p := newFetchProgress(&sink, &out, 0)
	if _, err := p.Write(bytes.Repeat([]byte("x"), 2<<20)); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); strings.Contains(got, "%") {
		t.Fatalf("no size was declared, so no percentage may be shown: %q", got)
	}

	sink.Reset()
	out.Reset()
	q := newFetchProgress(&sink, &out, 4<<20)
	if _, err := q.Write(bytes.Repeat([]byte("x"), 1<<20)); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); !strings.Contains(got, "(25%)") {
		t.Fatalf("a declared size must give a percentage: %q", got)
	}
}

func TestHumanBytes(t *testing.T) {
	for in, want := range map[int64]string{
		0: "0 bytes", 8192: "8192 bytes", 1 << 20: "1.0 MiB", 163035648: "155.5 MiB",
	} {
		if got := humanBytes(in); got != want {
			t.Errorf("humanBytes(%d) = %q, want %q", in, got, want)
		}
	}
}

// TestInstallV2ReportsTheDownloadToTheCallersTerminal installs from a fake origin
// with a writer standing in for the operator's terminal and asserts the byte
// count reaches it. The tests above prove fetchProgress; this proves InstallV2
// hands it the caller's writer. Wired to nil, every install would still succeed,
// and the operator would be back to a terminal that looks hung for the whole
// download.
func TestInstallV2ReportsTheDownloadToTheCallersTerminal(t *testing.T) {
	s := newTLSMap(t)
	body := toolinstalltest.VersionReporter("Grok", grokFxVersion, "")
	s.put("/latest", []byte(grokFxVersion+"\n"))
	s.put("/grok-"+grokFxVersion+"-linux-x86_64", body)
	eng, _, root := grokEngine(t, s)
	req := RequestV2{Driver: DriverGrok, Version: ChannelLatest, Platform: PlatformV2{OS: "linux", Arch: "amd64"}, DestRoot: root, Source: s.url()}
	var terminal bytes.Buffer
	if _, _, err := eng.InstallV2(context.Background(), req, nil, &terminal); err != nil {
		t.Fatalf("install: %v\n%s", err, terminal.String())
	}
	out := terminal.String()
	want := "  downloaded " + humanBytes(int64(len(body)))
	if !strings.Contains(out, want) {
		t.Fatalf("the terminal never saw the byte count %q; the download reported nothing:\n%s", want, out)
	}
	// The count qualifies the "downloading from" line, so it follows it.
	if strings.Index(out, "downloading from ") > strings.Index(out, want) {
		t.Fatalf("the count was reported before the download was announced:\n%s", out)
	}
}
