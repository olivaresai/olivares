// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package plugin_test

import (
	"testing"

	"google.golang.org/protobuf/types/descriptorpb"

	"github.com/olivaresai/olivares/sdk/plugin/genpb/olivaresv1"
)

// TestDeprecatedModuleTransportSet pins the announced deprecation (sdk/VERSIONING.md,
// "Deprecation": the out-of-process module transport, 2026-10-06) to the COMPILED
// descriptors: exactly its two services and their three exclusive messages carry the
// protobuf `deprecated` option, and nothing else in olivares.sdk.v1 does.
//
// Why a test and not the diff: every existing gate passes on a proto that silently
// omits one `option deprecated = true;` — buf lint has no such rule, adding an option
// is non-breaking, proto:check only proves genpb matches the proto it came from, and
// the guide-docs gate reads method rosters, not options. The compiled metadata is
// also what non-Go toolchains consume, so the announcement and the descriptor can
// drift with nothing red anywhere. This is the sdk-side analogue of the route
// deprecation table's window tests (core/api).
//
// If a future release un-deprecates the transport on purpose, update this set and
// sdk/VERSIONING.md in the same change — the failure is the point.
func TestDeprecatedModuleTransportSet(t *testing.T) {
	wantServices := map[string]bool{
		"ModuleService": true,
		"HostService":   true,
	}
	wantMessages := map[string]bool{
		"InitRequest":      true,
		"SubscribeRequest": true,
		"LogRecord":        true,
	}

	fd := olivaresv1.File_olivaresv1_v1_proto
	svcs := map[string]bool{}
	for i := 0; i < fd.Services().Len(); i++ {
		svc := fd.Services().Get(i)
		opts, ok := svc.Options().(*descriptorpb.ServiceOptions)
		deprecated := ok && opts.GetDeprecated()
		svcs[string(svc.Name())] = deprecated
	}
	msgs := map[string]bool{}
	for i := 0; i < fd.Messages().Len(); i++ {
		msg := fd.Messages().Get(i)
		opts, ok := msg.Options().(*descriptorpb.MessageOptions)
		deprecated := ok && opts.GetDeprecated()
		msgs[string(msg.Name())] = deprecated
	}

	for name, want := range wantServices {
		if got := svcs[name]; got != want {
			t.Errorf("service %s: descriptor deprecated = %v, want %v (sdk/VERSIONING.md announces this deprecation)", name, got, want)
		}
	}
	for name, deprecated := range svcs {
		if deprecated && !wantServices[name] {
			t.Errorf("service %s is deprecated in the descriptor but not announced in sdk/VERSIONING.md", name)
		}
	}
	for name, want := range wantMessages {
		if got := msgs[name]; got != want {
			t.Errorf("message %s: descriptor deprecated = %v, want %v (sdk/VERSIONING.md announces this deprecation)", name, got, want)
		}
	}
	for name, deprecated := range msgs {
		if deprecated && !wantMessages[name] {
			t.Errorf("message %s is deprecated in the descriptor but not announced in sdk/VERSIONING.md", name)
		}
	}
}
