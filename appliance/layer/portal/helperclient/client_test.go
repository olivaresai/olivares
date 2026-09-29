// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package helperclient

import (
	"context"
	"encoding/json"
	"errors"
	"go/parser"
	"go/token"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/olivaresai/olivares/appliance/layer/helpers/helperschema"
)

// shortDir is a directory whose socket paths fit a sockaddr_un.
func shortDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "hc")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// fakeHelper listens on dir/<name>.sock and answers every connection with answer, keeping the
// documents it received.
type fakeHelper struct {
	mu       sync.Mutex
	received []string
	listener net.Listener
	done     chan struct{}
}

func listen(t *testing.T, dir, name string, answer string) *fakeHelper {
	t.Helper()
	l, err := net.Listen("unix", filepath.Join(dir, name+".sock"))
	if err != nil {
		t.Fatal(err)
	}
	h := &fakeHelper{listener: l, done: make(chan struct{})}
	go func() {
		defer close(h.done)
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			document, _ := io.ReadAll(conn)
			h.mu.Lock()
			h.received = append(h.received, string(document))
			h.mu.Unlock()
			_, _ = io.WriteString(conn, answer)
			_ = conn.Close()
		}
	}()
	t.Cleanup(func() {
		_ = l.Close()
		<-h.done
	})
	return h
}

func (h *fakeHelper) documents() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.received...)
}

// operationID is an operation id the caller minted.
const operationID = "0123456789abcdef0123456789abcdef"

func reboot() helperschema.Request {
	return &helperschema.PowerRequest{Verb: helperschema.PowerReboot, OperationID: operationID}
}

// assertUnavailable asserts that err is 503 consumer_unavailable and that nothing was answered.
func assertUnavailable(t *testing.T, response helperschema.Response, err error) {
	t.Helper()
	var e *Error
	if !errors.As(err, &e) || e.Code != helperschema.CodeConsumerUnavailable || StatusOf(err) != http.StatusServiceUnavailable {
		t.Fatalf("got %v (status %d), want 503 %s", err, StatusOf(err), helperschema.CodeConsumerUnavailable)
	}
	if response.Result != "" || response.Code != "" || response.Nonce != "" || response.Bundle != nil {
		t.Fatalf("an unavailable helper answered %+v", response)
	}
}

func TestHelperClient_AbsentHelperIs503AndNeverDegrades(t *testing.T) {
	me := uint32(os.Getuid())
	ctx := context.Background()

	t.Run("no socket", func(t *testing.T) {
		dir := shortDir(t)
		// Another helper of the seam answers in the same directory: the client must not turn
		// to it, or to anything else, when the one it was asked for is absent.
		other := listen(t, dir, helperschema.HelperSupportBundle, `{"result": "performed"}`)
		response, err := Client{Dir: dir, Owner: me}.Call(ctx, helperschema.HelperPower, reboot())
		assertUnavailable(t, response, err)
		if got := other.documents(); len(got) != 0 {
			t.Fatalf("an absent power helper fell back to another helper, which received %q", got)
		}
	})

	t.Run("a socket nobody listens on", func(t *testing.T) {
		dir := shortDir(t)
		stale := listen(t, dir, helperschema.HelperPower, `{"result": "performed"}`)
		stale.listener.(*net.UnixListener).SetUnlinkOnClose(false)
		_ = stale.listener.Close()
		<-stale.done
		response, err := Client{Dir: dir, Owner: me}.Call(ctx, helperschema.HelperPower, reboot())
		assertUnavailable(t, response, err)
	})

	t.Run("a regular file where the socket belongs", func(t *testing.T) {
		dir := shortDir(t)
		path := filepath.Join(dir, "power.sock")
		if err := os.WriteFile(path, []byte("not a socket\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		response, err := Client{Dir: dir, Owner: me}.Call(ctx, helperschema.HelperPower, reboot())
		assertUnavailable(t, response, err)
		if data, _ := os.ReadFile(path); string(data) != "not a socket\n" {
			t.Fatalf("the file was changed: %q", data)
		}
	})

	t.Run("a symbolic link to a live socket", func(t *testing.T) {
		dir := shortDir(t)
		elsewhere := shortDir(t)
		live := listen(t, elsewhere, helperschema.HelperPower, `{"result": "performed"}`)
		if err := os.Symlink(filepath.Join(elsewhere, "power.sock"), filepath.Join(dir, "power.sock")); err != nil {
			t.Fatal(err)
		}
		response, err := Client{Dir: dir, Owner: me}.Call(ctx, helperschema.HelperPower, reboot())
		assertUnavailable(t, response, err)
		if got := live.documents(); len(got) != 0 {
			t.Fatalf("the request followed a link to %q", got)
		}
	})

	t.Run("a socket the service manager does not own", func(t *testing.T) {
		dir := shortDir(t)
		squatter := listen(t, dir, helperschema.HelperPower, `{"result": "performed"}`)
		response, err := Client{Dir: dir, Owner: me + 1}.Call(ctx, helperschema.HelperPower, reboot())
		assertUnavailable(t, response, err)
		if got := squatter.documents(); len(got) != 0 {
			t.Fatalf("the request was sent to a socket of another owner: %q", got)
		}
	})

	t.Run("the installed client's sockets are root's in the helpers' directory", func(t *testing.T) {
		var c Client
		if c.Owner != 0 || c.Dir != "" || helperschema.SocketDir != "/run/olivares-helpers" {
			t.Fatalf("the zero client trusts uid %d in %q", c.Owner, c.Dir)
		}
	})

	t.Run("a sent request whose answer is lost is unknown, not failed and not performed", func(t *testing.T) {
		dir := shortDir(t)
		listen(t, dir, helperschema.HelperPower, "")
		response, err := Client{Dir: dir, Owner: me}.Call(ctx, helperschema.HelperPower, reboot())
		var e *Error
		if !errors.As(err, &e) || e.Code != CodeOutcomeUnknown || StatusOf(err) != http.StatusBadGateway || response.Result != "" {
			t.Fatalf("a lost answer is %+v %v", response, err)
		}
	})

	t.Run("control: a present helper is asked once and answers", func(t *testing.T) {
		dir := shortDir(t)
		helper := listen(t, dir, helperschema.HelperPower, `{"result": "performed", "detail": "accepted"}`+"\n")
		response, err := Client{Dir: dir, Owner: me}.Call(ctx, helperschema.HelperPower, reboot())
		if err != nil || StatusOf(err) != http.StatusOK || response.Result != helperschema.ResultPerformed {
			t.Fatalf("a present helper: %+v %v", response, err)
		}
		got := helper.documents()
		if len(got) != 1 {
			t.Fatalf("the helper received %d documents", len(got))
		}
		var sent map[string]any
		if err := json.Unmarshal([]byte(got[0]), &sent); err != nil || len(sent) != 2 || sent["verb"] != "reboot" ||
			sent["operation_id"] != operationID {
			t.Fatalf("the helper received %q: the document and nothing about the caller", got[0])
		}
	})

	t.Run("a request outside the seam asks nothing", func(t *testing.T) {
		dir := shortDir(t)
		helper := listen(t, dir, helperschema.HelperPower, `{"result": "performed"}`)
		for name, call := range map[string]func() error{
			"an unknown helper": func() error {
				_, err := Client{Dir: dir, Owner: me}.Call(ctx, "../power", reboot())
				return err
			},
			"an invalid document": func() error {
				_, err := Client{Dir: dir, Owner: me}.Call(ctx, helperschema.HelperPower, &helperschema.PowerRequest{Verb: "halt", OperationID: operationID})
				return err
			},
		} {
			if err := call(); StatusOf(err) != http.StatusUnprocessableEntity {
				t.Errorf("%s: %v (status %d), want 422", name, err, StatusOf(err))
			}
		}
		if got := helper.documents(); len(got) != 0 {
			t.Fatalf("a refused request reached the helper: %q", got)
		}
	})

	t.Run("the client has no act of its own", func(t *testing.T) {
		entries, err := os.ReadDir(".")
		if err != nil {
			t.Fatal(err)
		}
		sources := 0
		for _, entry := range entries {
			name := entry.Name()
			if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				continue
			}
			sources++
			syntax, err := parser.ParseFile(token.NewFileSet(), name, nil, parser.ImportsOnly)
			if err != nil {
				t.Fatal(err)
			}
			for _, spec := range syntax.Imports {
				path, _ := strconv.Unquote(spec.Path.Value)
				if path == "os/exec" || path == "os/signal" {
					t.Errorf("%s imports %s: an absent helper must not become a local act", name, path)
				}
			}
		}
		if sources == 0 {
			t.Fatal("no source was read")
		}
	})
}
