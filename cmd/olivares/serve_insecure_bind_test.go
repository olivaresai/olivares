// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// (release gate S-5, U2): --insecure is a legitimate development mode, and
// it stays one. What it must NOT be is a mode a production-shaped listener can
// enter on a single word.
//
// Measured on this tree before the guard existed
// (bin/olivares serve --insecure --listen 0.0.0.0:19443 --grpc-listen 0.0.0.0:19444):
// the engine STARTED, printed the single-use setup token, and answered
// `curl http://172.20.0.2:19443/v1/server-info` with 200 over plain HTTP from a
// non-loopback address. The only thing standing between that and the wire was a
// log line reading "never expose beyond localhost" — advice, not a guard.
//
// Two in-tree precedents already say what the shape of the answer is:
//   - --seed-demo REFUSES a non-loopback bind outright (cmd_serve.go, docs/SECURITY-HARDENING.md).
//   - the cooperative ingest REFUSES a non-loopback bind unless the operator sets
//     allow_public_bind=true (connectors/claude/claude.go, SECURITY-HARDENING §1).
//
// So the guard here is the second shape: refuse, unless the operator declares the
// exposure a SECOND time and by name. Loopback development is untouched, the
// TLS-terminating-proxy deployment stays possible, and nobody serves the setup
// token off-host having said only "dev mode".

func TestInsecureBindGuard(t *testing.T) {
	const (
		loopHTTP = "127.0.0.1:8443"
		loopGRPC = "127.0.0.1:8444"
		pubHTTP  = "0.0.0.0:8443"
		pubGRPC  = "0.0.0.0:8444"
	)
	cases := []struct {
		name        string
		insecure    bool
		allowPublic bool
		listen      string
		grpcListen  string
		wantErr     bool
		wantMention string // a substring the refusal must name, so it is actionable
	}{
		{
			name:     "the development path is untouched",
			insecure: true, listen: loopHTTP, grpcListen: loopGRPC,
		},
		{
			name:     "TLS on is free to bind the world — that is the production posture",
			insecure: false, listen: pubHTTP, grpcListen: pubGRPC,
		},
		{
			name:     "plaintext HTTP off-host is refused",
			insecure: true, listen: pubHTTP, grpcListen: loopGRPC,
			wantErr: true, wantMention: pubHTTP,
		},
		{
			name:     "plaintext gRPC off-host is refused too (it carries the same bearer tokens)",
			insecure: true, listen: loopHTTP, grpcListen: pubGRPC,
			wantErr: true, wantMention: pubGRPC,
		},
		{
			name:     "an empty host is a wildcard bind, not a loopback one",
			insecure: true, listen: ":8443", grpcListen: loopGRPC,
			wantErr: true, wantMention: ":8443",
		},
		{
			name:     "a routable address is refused",
			insecure: true, listen: "10.0.0.5:8443", grpcListen: loopGRPC,
			wantErr: true, wantMention: "10.0.0.5:8443",
		},
		{
			name:     "declared a second time and by name, it is the operator's call",
			insecure: true, allowPublic: true, listen: pubHTTP, grpcListen: pubGRPC,
		},
		{
			name:     "the opt-in is inert on its own — it must never imply --insecure",
			insecure: false, allowPublic: true, listen: pubHTTP, grpcListen: pubGRPC,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := insecureBindGuard(c.insecure, c.allowPublic, c.listen, c.grpcListen)
			switch {
			case c.wantErr && err == nil:
				t.Fatalf("plaintext was allowed on the off-host bind %q/%q — the setup token and every bearer token travel in the clear there", c.listen, c.grpcListen)
			case !c.wantErr && err != nil:
				t.Fatalf("a supported posture was refused: %v", err)
			case !c.wantErr:
				return
			}
			if !strings.Contains(err.Error(), c.wantMention) {
				t.Fatalf("the refusal must name the offending address %q so the operator can act on it, got: %v", c.wantMention, err)
			}
			if !strings.Contains(err.Error(), "--insecure-allow-public-bind") {
				t.Fatalf("the refusal must name the escape hatch, or it reads as a bug rather than a decision: %v", err)
			}
		})
	}
}

// TestInsecureBindGuardIsWiredIntoTheBootPath is the half a pure-function test
// cannot see: a guard nobody calls is a comment. runEngine must consult it before
// anything binds, so the refusal happens instead of the exposure — not alongside it.
func TestInsecureBindGuardIsWiredIntoTheBootPath(t *testing.T) {
	dataDir := t.TempDir()
	opts := serveOptions{
		insecure:   true,
		listen:     "0.0.0.0:8443",
		grpcListen: "127.0.0.1:8444",
		dataDir:    dataDir,
	}
	announce := func(context.Context, io.Writer, *engine) error {
		t.Fatal("announce ran, so the engine had already booted before the bind guard")
		return nil
	}
	err := runEngine(context.Background(), &strings.Builder{}, opts, announce)
	if err == nil {
		t.Fatal("runEngine accepted --insecure on a wildcard bind")
	}
	if !strings.Contains(err.Error(), "--insecure-allow-public-bind") {
		t.Fatalf("runEngine failed for some other reason than the bind guard: %v", err)
	}

	// The ordering claim needs an observable, and a fatal `announce` is not one:
	// announce runs AFTER boot, so moving the guard to just after boot left this
	// test green while three signing keys and the SQLite store had already been
	// created — measured by the Codex contrast of 2026-08-06 (F-04). The data
	// directory is the observable: boot mints audit-signing.key, catalog-signing.key
	// and policy-signing.key into it, so an untouched directory is proof the
	// refusal came first.
	entries, rerr := os.ReadDir(dataDir)
	if rerr != nil {
		t.Fatalf("read the data dir: %v", rerr)
	}
	if len(entries) != 0 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("the guard refused only AFTER boot created state in the data dir (%v): a refusal that happens after keys are minted and the store is opened is not a refusal before the exposure", names)
	}
}

// TestServeHTTPRefusesAPlaintextPublicBind closes what the Codex contrast found
// (F-01, 2026-08-06) and what the guard above could not see on its own.
//
// insecureBindGuard reads --listen and --grpc-listen. It has no input for the SIX
// auxiliary listeners — HITL, voice webhook, agent gateway, Claude-hook PEP,
// Codex PEP, inference proxy — whose addresses come from operator config files,
// not flags (e.g. agentGatewayConfig.Listen, mcpgateway.go). They are served with
// the same global opts.insecure switch, so loopback primaries let the guard
// return nil while `"listen":"0.0.0.0:8446"` in a gateway config served plain
// HTTP off-host. Adding a seventh address to the guard's argument list would fix
// today's six and silently miss the eighth listener someone adds next year.
//
// So the refusal lives at the choke point those listeners go through. Note the
// BOUND, which an earlier version of this comment overstated and cmd_serve.go now
// states properly: serveHTTP is not where EVERY listener in the process is born.
// serveGRPC creates its own (covered by the flag-level guard), and in-process
// source connectors create theirs outside both guards.
func TestServeHTTPRefusesAPlaintextPublicBind(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	const publicAddr = "0.0.0.0:19999"
	const probeAddr = "127.0.0.1:19999" // the same socket, dialed

	// Prove the port is FREE first. The closing assertion reads an open socket as
	// "serveHTTP bound before refusing", and an unrelated process already on this
	// port produces that exact failure — a message indistinguishable from the
	// product defect it claims to diagnose.
	if c, derr := net.DialTimeout("tcp", probeAddr, 300*time.Millisecond); derr == nil {
		_ = c.Close()
		// Fatalf, NOT Skip. The first cut of this precheck skipped, and an
		// adversarial panel measured what that buys: hold the port, delete the
		// plaintext refusal outright, and the package reports SKIP + PASS + exit 0
		// while the off-host guard is GONE. A gate reading $? sees green. That
		// traded a confusing false positive for a SILENT FALSE NEGATIVE on the one
		// test that observes an exposure the flag-level guard cannot see — and this
		// repository's own rule is that "I could not look" is never "it is clean".
		t.Fatalf("%s is already in use, so this fixture cannot attribute a bind to serveHTTP — this is a broken FIXTURE, not a product defect, but it fails closed on purpose: free the port and re-run", probeAddr)
	}

	errCh := make(chan error, 1)
	srv := &http.Server{Addr: publicAddr, Handler: http.NotFoundHandler(), ReadHeaderTimeout: time.Second}
	// In a goroutine with a deadline, NOT inline: a serveHTTP that fails to refuse
	// goes on to serve and never returns, and a mutation test whose mutant HANGS
	// reports a goroutine dump after the framework's ten-minute panic instead of
	// saying what broke. Measured while writing this — the first version of this
	// test did exactly that.
	t.Cleanup(func() { _ = srv.Close() })
	go serveHTTP(srv, publicAddr, true /*insecure*/, false /*allowPublic*/, false, logger, errCh)

	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("serveHTTP reported nil for a refused bind")
		}
		if !strings.Contains(err.Error(), publicAddr) || !strings.Contains(err.Error(), "--insecure-allow-public-bind") {
			t.Fatalf("the refusal must name the address and the escape hatch, got: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("an auxiliary listener bound plaintext off-host without a word — this is the bypass the primary-address guard cannot see")
	}

	// Nothing may be listening: refusing after binding is not refusing.
	if c, derr := net.DialTimeout("tcp", probeAddr, time.Second); derr == nil {
		_ = c.Close()
		t.Fatal("the port is open, so serveHTTP bound the socket before refusing")
	}
}

// TestServeHTTPServesTheAllowedPostures is the other direction: the choke-point
// refusal must not break the postures the guard deliberately allows.
func TestServeHTTPServesTheAllowedPostures(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cases := []struct {
		name              string
		insecure, allowed bool
		addr              string
	}{
		{"loopback plaintext is development", true, false, "127.0.0.1:0"},
		{"declared off-host plaintext is the operator's call", true, true, "0.0.0.0:0"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			errCh := make(chan error, 1)
			srv := &http.Server{Addr: c.addr, Handler: http.NotFoundHandler(), ReadHeaderTimeout: time.Second}
			go serveHTTP(srv, c.addr, c.insecure, c.allowed, false, logger, errCh)
			t.Cleanup(func() { _ = srv.Close() })
			select {
			case err := <-errCh:
				if err != nil && !errors.Is(err, http.ErrServerClosed) {
					t.Fatalf("a supported posture was refused: %v", err)
				}
			case <-time.After(500 * time.Millisecond):
				// Still serving, which is the pass condition here.
			}
		})
	}
}
