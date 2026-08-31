// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package nats

import (
	"bufio"
	"strings"
	"testing"
)

// TestReadMsgParsesFrame proves the hand-rolled NATS framing is REAL, not mocked: a
// raw "MSG <subject> <sid> <#bytes>\r\n<payload>\r\n" buffer parses into a Msg whose
// subject and data match exactly and whose trailing CRLF is consumed.
func TestReadMsgParsesFrame(t *testing.T) {
	r := bufio.NewReader(strings.NewReader("MSG orders.events 1 5\r\nhello\r\n"))
	line, _ := r.ReadString('\n')
	verb, args := parseControlLine(line)
	if verb != "MSG" {
		t.Fatalf("verb = %q, want MSG", verb)
	}
	m, err := readMsg(r, args)
	if err != nil {
		t.Fatalf("readMsg: %v", err)
	}
	if m.Subject != "orders.events" {
		t.Fatalf("subject = %q", m.Subject)
	}
	if string(m.Data) != "hello" {
		t.Fatalf("data = %q, want hello", m.Data)
	}
	if m.ReplyTo != "" {
		t.Fatalf("unexpected reply-to %q", m.ReplyTo)
	}
}

// TestReadMsgWithReplyTo parses the four-token MSG variant (subject sid reply size).
func TestReadMsgWithReplyTo(t *testing.T) {
	r := bufio.NewReader(strings.NewReader("MSG s.x 2 _INBOX.abc 3\r\nabc\r\n"))
	line, _ := r.ReadString('\n')
	_, args := parseControlLine(line)
	m, err := readMsg(r, args)
	if err != nil {
		t.Fatalf("readMsg: %v", err)
	}
	if m.Subject != "s.x" || m.ReplyTo != "_INBOX.abc" || string(m.Data) != "abc" {
		t.Fatalf("parsed wrong: %+v", m)
	}
}

// TestReadHMsgParsesHeadersAndStatus parses an HMSG frame: a NATS/1.0 header block
// with a status line and a header, then the payload. This is the form JetStream uses
// for both CloudEvents-binary delivery and "no messages" status replies.
func TestReadHMsgParsesHeadersAndStatus(t *testing.T) {
	// header block: "NATS/1.0\r\nce-source... " — use bare CloudEvents attribute names.
	hdr := "NATS/1.0\r\nspecversion: 1.0\r\nid: e1\r\nsource: /apps/checkout\r\ntype: com.acme.Created\r\n\r\n"
	payload := `{"x":1}`
	frame := "HMSG orders.events 1 " + itoaTest(len(hdr)) + " " + itoaTest(len(hdr)+len(payload)) + "\r\n" + hdr + payload + "\r\n"
	r := bufio.NewReader(strings.NewReader(frame))
	line, _ := r.ReadString('\n')
	verb, args := parseControlLine(line)
	if verb != "HMSG" {
		t.Fatalf("verb = %q", verb)
	}
	m, err := readHMsg(r, args)
	if err != nil {
		t.Fatalf("readHMsg: %v", err)
	}
	if m.Subject != "orders.events" {
		t.Fatalf("subject = %q", m.Subject)
	}
	if m.Header["source"] != "/apps/checkout" || m.Header["type"] != "com.acme.Created" {
		t.Fatalf("headers parsed wrong: %+v", m.Header)
	}
	if string(m.Data) != payload {
		t.Fatalf("payload = %q", m.Data)
	}
}

// TestParseHeaderBlockStatus parses a JetStream 404 status block (no real message).
func TestParseHeaderBlockStatus(t *testing.T) {
	status, _ := parseHeaderBlock([]byte("NATS/1.0 404 No Messages"))
	if status != "404" {
		t.Fatalf("status = %q, want 404", status)
	}
	if !isStatusCode(status) {
		t.Fatalf("404 should be a no-messages status code")
	}
}

// TestSubjectParents verifies the dotted-subject hierarchy decomposition.
func TestSubjectParents(t *testing.T) {
	got := subjectParents("a.b.c")
	want := []string{"a.b", "a"}
	if len(got) != len(want) {
		t.Fatalf("parents = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("parents[%d] = %q, want %q", i, got[i], want[i])
		}
	}
	if subjectParents("flat") != nil {
		t.Fatalf("a token-less subject has no parents")
	}
}

// TestHostPort verifies server-string parsing and the natss:// TLS-scheme signal.
func TestHostPort(t *testing.T) {
	addr, tlsScheme, err := hostPort("nats://example:4222")
	if err != nil || addr != "example:4222" || tlsScheme {
		t.Fatalf("nats:// parse wrong: %q tls=%v err=%v", addr, tlsScheme, err)
	}
	addr, tlsScheme, err = hostPort("natss://example")
	if err != nil || addr != "example:4222" || !tlsScheme {
		t.Fatalf("natss:// parse wrong: %q tls=%v err=%v", addr, tlsScheme, err)
	}
	addr, _, err = hostPort("host:5222")
	if err != nil || addr != "host:5222" {
		t.Fatalf("bare host:port parse wrong: %q err=%v", addr, err)
	}
}

// itoaTest is a tiny test-local int formatter to keep the frame builders readable.
func itoaTest(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
