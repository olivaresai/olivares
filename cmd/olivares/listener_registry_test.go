// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"net"
	"net/http"
	"strconv"
	"strings"
	"testing"
)

func TestServeListenerPublishedDefaults(t *testing.T) {
	for name, addr := range map[string]string{"codex-hook-pep": defaultCodexHookPEPListen, "inference-proxy": defaultInferenceProxyListen} {
		if addr != "127.0.0.1:8448" {
			t.Errorf("%s moved its published 26.10.1 default to %s", name, addr)
		}
	}
}

func TestServeListenerCollisionAddresses(t *testing.T) {
	for _, tc := range []struct {
		name, first, second string
		collision           bool
	}{
		{"identical", "127.0.0.1:8448", "127.0.0.1:8448", true},
		{"wildcard", ":8448", "127.0.0.1:8448", true},
		{"IPv4 wildcard", "0.0.0.0:8448", "127.0.0.1:8448", true},
		{"IPv6 wildcard", "[::]:8448", "[::1]:8448", true},
		{"dual stack", "[::]:8448", "127.0.0.1:8448", true},
		{"IPv6 loopback scope", "[::1]:8448", "[::1%lo]:8448", true},
		{"IPv6 spelling", "[::1]:8448", "[0:0:0:0:0:0:0:1]:8448", true},
		{"mapped IPv4", "[::ffff:127.0.0.1]:8448", "127.0.0.1:8448", true},
		{"named port", "127.0.0.1:http", "127.0.0.1:80", true},
		{"hostname", "localhost:8448", "localhost:8448", true},
		{"hostname and IP", "localhost:8448", "127.0.0.1:8448", true},
		{"IPv4 wildcard and IPv6", "0.0.0.0:8448", "[::1]:8448", true},
		{"ephemeral", "127.0.0.1:0", "127.0.0.1:0", false},
		{"different ports", "127.0.0.1:8448", "127.0.0.1:8449", false},
		{"different addresses", "127.0.0.1:8448", "127.0.0.2:8448", false},
		{"different families", "127.0.0.1:8448", "[::1]:8448", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			binds := 0
			bind := func(context.Context, string, bool) (net.Listener, error) {
				binds++
				return &fakeServeListener{}, nil
			}
			owned, err := acquireServeListeners(context.Background(), bind,
				[]serveListenerSpec{{addr: tc.first}, {addr: tc.second}}, true, serveListenersLogger())
			if owned != nil {
				_ = owned.closeAll()
			}
			if tc.collision {
				if err == nil || !strings.Contains(err.Error(), "collision") || binds != 0 {
					t.Fatalf("err=%v binds=%d; want a collision refusal before binding", err, binds)
				}
			} else if err != nil || binds != 2 {
				t.Fatalf("err=%v binds=%d; want both independent listeners", err, binds)
			}
		})
	}
}

func TestServeListenerRegistryRefusesCollisionBeforeBinding(t *testing.T) {
	for _, reuse := range []bool{false, true} {
		t.Run(map[bool]string{false: "ordinary", true: "reuse-port"}[reuse], func(t *testing.T) {
			dataDir := t.TempDir()
			addr := "127.0.0.1:18443"
			t.Setenv("OLIVARES_HITL_CONFIG", writeBindAnnounceHITLConfig(t, addr))
			opts := bindAnnounceInsecureOpts(dataDir, addr, "127.0.0.1:0")
			opts.reusePort = reuse
			binds := 0
			opts.bindListener = func(context.Context, string, bool) (net.Listener, error) {
				binds++
				return nil, errBindAnnounceFixture
			}
			out := &bindAnnounceBuffer{}
			res := runBindAnnounce(t, opts, out, serveAnnounce(true), nil)
			if res.err == nil {
				t.Fatal("boot accepted two listeners on one address")
			}
			for _, want := range []string{"collision", "http", "hitl", addr, "OLIVARES_HITL_CONFIG"} {
				if !strings.Contains(res.err.Error(), want) {
					t.Errorf("boot error %q does not name %q", res.err, want)
				}
			}
			if binds != 0 {
				t.Errorf("boot attempted %d binds before refusing the collision", binds)
			}
			requireNoAnnouncementEffects(t, "listener collision", res, out, dataDir)
		})
	}
}

func TestServeListenerRegistryInvalidAddressRefusesBeforeBinding(t *testing.T) {
	binds := 0
	bind := func(context.Context, string, bool) (net.Listener, error) {
		binds++
		return &fakeServeListener{}, nil
	}
	owned, err := acquireServeListeners(context.Background(), bind,
		[]serveListenerSpec{{addr: "127.0.0.1:0"}, {addr: "127.0.0.1:invalid-port"}}, false, serveListenersLogger())
	if owned != nil {
		_ = owned.closeAll()
	}
	if err == nil || binds != 0 {
		t.Fatalf("err=%v binds=%d; want invalid address refusal before any bind", err, binds)
	}
}

func TestServeListenerRegistryDefaultAddresses(t *testing.T) {
	server := func() *http.Server { return &http.Server{Addr: "127.0.0.1:0"} }
	specs := serveListenerRegistry(serveOptions{grpcListen: "127.0.0.1:0"},
		server(), server(), server(), server(), server(), server(), server(), server())
	if len(specs) != 9 {
		t.Fatalf("registry has %d listeners, want HTTP, gRPC and seven side listeners", len(specs))
	}
	for i := range specs {
		if specs[i].name == "" || specs[i].configKey == "" || specs[i].admission == "" || specs[i].defaultAddr == "" {
			t.Fatalf("listener %d lacks its name, default, config key or admission explanation", i)
		}
		specs[i].addr = specs[i].defaultAddr
	}
	// Both optional surfaces published 8448 before collision checks existed.
	// Preserve standalone installs; co-enabling them requires an explicit address.
	if _, err := resolveServeListenerAddresses(context.Background(), specs); err == nil ||
		!strings.Contains(err.Error(), "codex-hook-pep") || !strings.Contains(err.Error(), "inference-proxy") {
		t.Fatalf("co-enabled legacy defaults must name the collision: %v", err)
	}
	for i := range specs {
		if specs[i].name == "codex-hook-pep" {
			specs[i].addr = "127.0.0.1:8450"
		}
	}
	if _, err := resolveServeListenerAddresses(context.Background(), specs); err != nil {
		t.Fatalf("explicitly separated listener registry collides: %v", err)
	}
	active := serveListenerRegistry(serveOptions{grpcListen: "127.0.0.1:0"},
		server(), nil, nil, nil, nil, nil, nil, nil)
	if len(active) != 2 || active[0].name != "http" || active[1].name != "grpc" {
		t.Fatalf("unconfigured optional listeners must not enter acquisition: %#v", active)
	}
}

func TestServeListenerRegistryPinsResolvedAddresses(t *testing.T) {
	bind := func(_ context.Context, addr string, _ bool) (net.Listener, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil || net.ParseIP(host) == nil || port != "443" {
			t.Errorf("bind address %q can resolve differently from preflight", addr)
		}
		return &fakeServeListener{}, nil
	}
	owned, err := acquireServeListeners(context.Background(), bind,
		[]serveListenerSpec{{addr: "localhost:https"}}, true, serveListenersLogger())
	if err != nil {
		t.Fatal(err)
	}
	_ = owned.closeAll()
}

func TestServeListenerRegistryZoneAliases(t *testing.T) {
	interfaces, err := net.Interfaces()
	if err != nil {
		t.Fatal(err)
	}
	if len(interfaces) == 0 {
		t.Skip("no network interfaces for scope alias test")
	}
	iface := interfaces[0]
	binds := 0
	bind := func(context.Context, string, bool) (net.Listener, error) {
		binds++
		return &fakeServeListener{}, nil
	}
	owned, err := acquireServeListeners(context.Background(), bind, []serveListenerSpec{
		{addr: "[fe80::1%" + iface.Name + "]:8448"},
		{addr: "[fe80::1%" + strconv.Itoa(iface.Index) + "]:8448"},
	}, true, serveListenersLogger())
	if owned != nil {
		_ = owned.closeAll()
	}
	if err == nil || !strings.Contains(err.Error(), "collision") || binds != 0 {
		t.Fatalf("err=%v binds=%d; want same-interface scope alias refusal before binding", err, binds)
	}
}

// Model a kernel-assigned ephemeral port matching a later configured listener.
type registryAssignedPortListener struct{ fakeServeListener }

func (*registryAssignedPortListener) Addr() net.Addr {
	return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 18443}
}

func TestServeListenerRegistryEphemeralCollisionUnwinds(t *testing.T) {
	first := &registryAssignedPortListener{}
	binds := 0
	bind := func(context.Context, string, bool) (net.Listener, error) {
		binds++
		return first, nil
	}
	owned, err := acquireServeListeners(context.Background(), bind, []serveListenerSpec{
		{name: "ephemeral", addr: "127.0.0.1:0"},
		{name: "fixed", addr: "127.0.0.1:18443"},
	}, true, serveListenersLogger())
	if owned != nil {
		_ = owned.closeAll()
	}
	if err == nil || !strings.Contains(err.Error(), "collision") || !strings.Contains(err.Error(), "ephemeral") || !strings.Contains(err.Error(), "fixed") {
		t.Fatalf("want both listener names in an assigned-port collision, got %v", err)
	}
	if binds != 1 || first.closes.Load() != 1 {
		t.Fatalf("binds=%d closes=%d; want first listener closed before the second bind", binds, first.closes.Load())
	}
}
