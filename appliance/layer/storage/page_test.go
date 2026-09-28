// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package storage_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/olivaresai/olivares/appliance/layer/storage"
)

const signIn = "Sign-in is refused on this appliance; use the repair console on tty1."

// served runs one GET and fails when the page does not answer within two seconds: a page answers
// from what it has and never waits on the storage helper.
func served(t *testing.T, page http.Handler) *httptest.ResponseRecorder {
	t.Helper()
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		rec := httptest.NewRecorder()
		page.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/storage", nil))
		done <- rec
	}()
	select {
	case rec := <-done:
		return rec
	case <-time.After(2 * time.Second):
		t.Fatal("the page waited on the storage helper instead of answering from its last inventory")
		return nil
	}
}

// settled serves GET until accept takes the answer, for at most two seconds: a read the page
// starts in the background reaches a later request.
func settled(t *testing.T, page http.Handler, accept func(*httptest.ResponseRecorder) bool) *httptest.ResponseRecorder {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		rec := served(t, page)
		if accept(rec) {
			return rec
		}
		if time.Now().After(deadline) {
			t.Fatalf("the page never showed the expected answer: %d %s", rec.Code, rec.Body.String())
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestPage_ShowsTheInventoryMarksAndOperationsAndChangesNothing(t *testing.T) {
	h := newHost()
	h.disk("sda", "SYS0001", 8<<8, 64<<30, "gpt")
	h.partition("sda", "sda1", 1, "c12a7328-f81f-11d2-ba4b-00a0c93ec93b")
	h.partition("sda", "sda2", 2, "4f68bce3-e8cd-4db1-96e7-fbcaf984b709")
	h.with("sda1", mounted("vfat", "/boot/efi"))
	h.with("sda2", mounted("ext4", "/"))
	h.with("sda2", func(b *storage.Block) { b.IDLabel = "<script>alert(1)</script>" })
	caps := map[string]storage.Capability{"resize/ext4": {Available: true, Modes: storage.ResizeOfflineGrow | storage.ResizeOnlineGrow}}
	inv := read(t, &fakeBus{objects: h.objects, caps: caps}, &fakeKernel{boot: bootA})
	page := storage.Page(signIn, func(context.Context) (storage.Inventory, error) { return inv, nil })

	rec := settled(t, page, func(rec *httptest.ResponseRecorder) bool { return rec.Code == http.StatusOK })
	body := rec.Body.String()
	if rec.Header().Get("Content-Type") != "text/html; charset=utf-8" {
		t.Fatalf("type %q", rec.Header().Get("Content-Type"))
	}
	for _, want := range []string{
		signIn, "/dev/sda", "System disk: yes (/, /boot/efi)", "mount /boot/efi", "grow ext4 (offline, online)",
		"drive:" + diskNamed(t, inv, "/dev/sda").Identity.Digest, "LVM: read", "Nothing on this page changes the appliance",
		"An LVM metadata backup or a snapshot is not a data backup", "olivares-appliance storage list --plan",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the page lacks %q", want)
		}
	}
	if strings.Contains(body, "<script>") || !strings.Contains(body, "&lt;script&gt;") {
		t.Fatal("a filesystem label reached the page unescaped")
	}
	if strings.Contains(body, "<form") || strings.Contains(body, "method=\"post\"") {
		t.Fatal("the read-only page offers a form")
	}

	var posted atomic.Int32
	refusing := storage.Page(signIn, func(context.Context) (storage.Inventory, error) { posted.Add(1); return inv, nil })
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		rec := httptest.NewRecorder()
		refusing.ServeHTTP(rec, httptest.NewRequest(method, "/storage", strings.NewReader("op=format")))
		if rec.Code != http.StatusMethodNotAllowed || posted.Load() != 0 {
			t.Fatalf("%s: status %d reads %d", method, rec.Code, posted.Load())
		}
	}

	for err, code := range map[error]string{
		&storage.Refusal{Code: "not_admitted", Reason: "fixed"}: "not_admitted",
		errors.New("dial unix: no such file"):                   "consumer_unavailable",
	} {
		failing := storage.Page(signIn, func(context.Context) (storage.Inventory, error) { return storage.Inventory{}, err })
		rec := settled(t, failing, func(rec *httptest.ResponseRecorder) bool { return strings.Contains(rec.Body.String(), code) })
		if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), signIn) || strings.Contains(rec.Body.String(), "no such file") {
			t.Fatalf("%v: status %d body %s", err, rec.Code, rec.Body.String())
		}
	}
}

// gatedReader is a storage helper whose reads each wait for one result from the test, whatever
// their context says, as a slow or stuck helper would.
type gatedReader struct {
	calls   atomic.Int32
	started chan struct{}
	results chan gatedResult
}

type gatedResult struct {
	inv storage.Inventory
	err error
}

func newGatedReader(t *testing.T) *gatedReader {
	g := &gatedReader{started: make(chan struct{}, 16), results: make(chan gatedResult)}
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(g.results) }) })
	return g
}

func (g *gatedReader) read(context.Context) (storage.Inventory, error) {
	g.calls.Add(1)
	g.started <- struct{}{}
	r := <-g.results
	return r.inv, r.err
}

func (g *gatedReader) waitStarted(t *testing.T) {
	t.Helper()
	select {
	case <-g.started:
	case <-time.After(2 * time.Second):
		t.Fatal("the page started no read of the storage helper")
	}
}

func TestPage_AnswersFromTheLastInventoryWithoutWaitingOnARead(t *testing.T) {
	inv := read(t, &fakeBus{objects: fixture(t, "virtio-blank-serial.json")},
		&fakeKernel{boot: bootA, firstMiB: map[string]string{"/dev/vda": firstMiBOfVda}})
	g := newGatedReader(t)
	page := storage.Page(signIn, g.read)

	// Before any read completes: an immediate answer that says so, and one read in flight.
	rec := served(t, page)
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "not completed yet") ||
		strings.Contains(rec.Body.String(), "/dev/vda") || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("first answer %d %q %s", rec.Code, rec.Header().Get("Retry-After"), rec.Body.String())
	}
	g.waitStarted(t)
	for range 3 {
		served(t, page)
	}
	if n := g.calls.Load(); n != 1 {
		t.Fatalf("%d reads in flight: the page refreshes one at a time", n)
	}

	// The read completes: a later request shows it.
	g.results <- gatedResult{inv: inv}
	settled(t, page, func(rec *httptest.ResponseRecorder) bool {
		return rec.Code == http.StatusOK && strings.Contains(rec.Body.String(), "/dev/vda")
	})

	// A refresh is running and stuck: the page still answers at once, from the last inventory.
	rec = served(t, page)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "/dev/vda") {
		t.Fatalf("while refreshing: %d %s", rec.Code, rec.Body.String())
	}
	g.waitStarted(t)
	for range 3 {
		if rec := served(t, page); rec.Code != http.StatusOK {
			t.Fatalf("while refreshing: %d", rec.Code)
		}
	}
	if n := g.calls.Load(); n != 2 {
		t.Fatalf("%d reads: one completed and one in flight", n)
	}

	// The refresh fails: the last inventory stays, and the page names the failure.
	g.results <- gatedResult{err: &storage.Refusal{Code: "not_admitted", Reason: "fixed"}}
	rec = settled(t, page, func(rec *httptest.ResponseRecorder) bool { return strings.Contains(rec.Body.String(), "not_admitted") })
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "/dev/vda") {
		t.Fatalf("after a failed refresh: %d %s", rec.Code, rec.Body.String())
	}
}
