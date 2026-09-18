// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

// THE PRODUCT IS A SERVER, SO ITS DEFAULT BIND IS EVERY INTERFACE.
//
// This file owns one decision and every command reads it from here, because the
// same decision used to be written out eight times — three commands, two service
// units, the env-file generator, the guided setup and the installer — and a
// default spelled eight times is eight defaults.
//
// WHAT THE DEFAULT IS. ":8443" is how you ask Go for the dual-stack wildcard: it
// accepts on 0.0.0.0 and, where the kernel has IPv6, on :: as well. "0.0.0.0:8443"
// would bind IPv4 ONLY, which is why the prose says 0.0.0.0 (the address an
// operator recognizes) and the code says ":8443" (the bind that also works on an
// IPv6 host). The two are not in conflict and the help text states both.
//
// WHY IT IS NOT LOOPBACK. A loopback default on a server product is an
// anti-pattern with a measured cost, not a taste: the operator installs on a
// server, the console answers on that server and nowhere else, and nothing on
// screen says why. The engine's own security posture does not come from the bind
// — TLS is on by default, there are NO default credentials, and first setup is
// gated by a single-use token printed once to stdout. A bind that hides a
// correctly-secured console from the machine it was installed to buys nothing and
// costs the first-run experience.
//
// WHAT THIS DOES NOT CHANGE, and the distinction is the whole safety argument:
//
//   - PLAINTEXT IS STILL REFUSED OFF-HOST. --insecure on a non-loopback bind is
//     refused by insecureBindGuard and again by plaintextBindRefusal. Widening the
//     default therefore does NOT widen plaintext exposure: it makes `--insecure`
//     require an explicit loopback --listen, which is what a plaintext listener
//     always needed and used to get by accident.
//   - THE SEEDED DEMO IS STILL LOOPBACK-ONLY. --seed-demo mints a superadmin whose
//     password is in the public source tree; its guard is unchanged, so the demo
//     now also requires an explicit loopback --listen.
//   - CLIENT DEFAULTS ARE UNCHANGED. `readyz`, `doctor`, `support bundle` and
//     `auth bootstrap` still default to https://127.0.0.1:8443, because "which
//     engine do I talk to" is a different question from "where do I accept
//     connections", and the answer to the first is still "the one on this host".
//   - AUXILIARY LISTENERS ARE UNCHANGED. The agent gateway and the Claude Code
//     hook PEP take their addresses from operator config files and keep their
//     loopback defaults; they are opt-in surfaces with their own guards and are
//     not part of "the console is a server".
const (
	// defaultHTTPListen is the default --listen: REST + web console, TLS on.
	defaultHTTPListen = ":8443"
	// defaultGRPCListen is the default --grpc-listen: the collector/ingest API.
	defaultGRPCListen = ":8444"

	// loopbackHTTPListen and loopbackGRPCListen are the addresses the
	// documentation, the help text and the refusal messages name as the way to
	// restrict the engine to the machine it runs on. They are spelled once so a
	// remedy printed to an operator cannot drift from the remedy in the docs.
	loopbackHTTPListen = "127.0.0.1:8443"
	loopbackGRPCListen = "127.0.0.1:8444"
)
