// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package tak

import (
	"context"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/sdk"
)

// The CoT bearers carry Cursor-on-Target in the CLEAR over TCP and UDP
// (listener.go: net.Listen / net.ListenPacket, no TLS anywhere on this path).
// A CoT event is a position report keyed by a device uid — it identifies a
// bearer and where they are — so a non-loopback bind hands that traffic to
// anything that can route to the host, and equally lets anyone inject forged
// position reports into the governed feed. The connector's own field docs
// advertised "0.0.0.0:6969" and "0.0.0.0:8087" as the examples to copy
// (finding H-03 of the the model contrast of PR #565).

func takSettings(kv ...string) map[string]string {
	m := map[string]string{}
	for i := 0; i+1 < len(kv); i += 2 {
		m[kv[i]] = kv[i+1]
	}
	return m
}

func TestTAKRefusesAPlaintextPublicCoTBind(t *testing.T) {
	cases := []struct{ key, addr string }{
		{cfgCoTTCPListen, "0.0.0.0:8087"},
		{cfgCoTTCPListen, ":8087"},
		{cfgCoTTCPListen, "[::]:8087"},
		{cfgCoTTCPListen, "192.0.2.7:8087"},
		{cfgCoTUDPListen, "0.0.0.0:6969"},
		{cfgCoTUDPListen, ":6969"},
		{cfgCoTUDPListen, "192.0.2.7:6969"},
	}
	for _, c := range cases {
		t.Run(c.key+"="+c.addr, func(t *testing.T) {
			s := New()
			err := s.Open(context.Background(), sdk.Config{Settings: takSettings(c.key, c.addr)})
			if err == nil {
				_ = s.Close(context.Background())
				t.Fatalf("Open accepted a plaintext non-loopback CoT bind %s=%q without allow_public_bind", c.key, c.addr)
			}
			if !strings.Contains(err.Error(), "allow_public_bind") {
				t.Errorf("refusal must name the opt-in that unblocks it; got: %v", err)
			}
		})
	}
}

func TestTAKAcceptsAPublicCoTBindWhenDeclared(t *testing.T) {
	s := New()
	err := s.Open(context.Background(), sdk.Config{Settings: takSettings(
		cfgCoTTCPListen, "0.0.0.0:8087",
		cfgCoTUDPListen, "0.0.0.0:6969",
		cfgAllowPublicBind, "true",
	)})
	if err != nil {
		t.Fatalf("a declared public bind must be accepted: %v", err)
	}
	_ = s.Close(context.Background())
}

func TestTAKAcceptsALoopbackCoTBind(t *testing.T) {
	for _, addr := range []string{"127.0.0.1:8087", "localhost:8087", "[::1]:8087"} {
		t.Run(addr, func(t *testing.T) {
			s := New()
			if err := s.Open(context.Background(), sdk.Config{Settings: takSettings(cfgCoTTCPListen, addr)}); err != nil {
				t.Fatalf("a loopback bind must need no opt-in: %v", err)
			}
			_ = s.Close(context.Background())
		})
	}
}

// A multicast join is inherently off-host: the group address is not loopback and
// the point of joining is to receive from the LAN. That is a legitimate TAK
// deployment, but it is exactly the risk the opt-in exists to make explicit, so
// it must be DECLARED rather than arrive as a side effect of setting a group.
func TestTAKMulticastStillRequiresTheDeclaration(t *testing.T) {
	s := New()
	err := s.Open(context.Background(), sdk.Config{Settings: takSettings(
		cfgCoTUDPListen, "0.0.0.0:6969",
		cfgCoTMulticast, "239.2.3.1",
	)})
	if err == nil {
		_ = s.Close(context.Background())
		t.Fatal("a multicast CoT join on a wildcard bind must still be declared")
	}
}

// The field documentation is the other half: an operator copies the example.
// While these read "0.0.0.0:…", the descriptor teaches the exposure the code
// now refuses — a contradiction that costs a support round at best.
func TestTAKFieldDocsDoNotAdvertiseAWildcardExample(t *testing.T) {
	for _, f := range New().Descriptor().ConfigFields {
		if f.Key != cfgCoTUDPListen && f.Key != cfgCoTTCPListen {
			continue
		}
		if strings.Contains(f.Description, "0.0.0.0") {
			t.Errorf("%s advertises a WILDCARD example operators will copy: %q", f.Key, f.Description)
		}
	}
}
