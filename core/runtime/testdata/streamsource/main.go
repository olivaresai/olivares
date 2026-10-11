// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Command streamsource is a TEST FIXTURE: a real out-of-process STREAMING source
// plugin. Each Gather emits one edge and then blocks until the engine cancels it,
// like the claude and broker plugins do. It exists so a test can kill the plugin
// process while Gather is running and measure that the engine starts a new one.
//
// With the setting fail=1 its Gather returns an error at once while the process
// stays alive: the connector's own failure, which must not be taken for a crash.
// With crash=1 the process exits right after its edge: a plugin in a crash loop.
//
// It lives under testdata, so `./...` never builds, embeds or ships it.
package main

import (
	"context"
	"errors"
	"os"
	"strconv"
	"time"

	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
	"github.com/olivaresai/olivares/sdk/plugin"
)

type streamSource struct{ fail, crash bool }

func (*streamSource) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{Name: "olivares.test-stream", Type: sdk.TypeSource, APIVersion: sdk.APIVersion}
}

func (s *streamSource) Open(_ context.Context, cfg sdk.Config) error {
	s.fail = cfg.Settings["fail"] == "1"
	s.crash = cfg.Settings["crash"] == "1"
	return nil
}

// Gather emits one edge whose resource is this process's pid, then streams nothing
// until canceled. The pid lets the test tell a restarted process from the first one.
func (s *streamSource) Gather(ctx context.Context, sink sdk.Sink) error {
	if s.fail {
		return errors.New("streamsource: gather failed on request")
	}
	if err := sink.Emit(ctx, model.EdgeObservation{
		OriginKind:   "agent",
		OriginRef:    "stream-fixture",
		ResourceKind: "test.process",
		ResourceRef:  "pid-" + strconv.Itoa(os.Getpid()),
		Mode:         model.ModeRead,
		Source:       model.SignalOTEL,
		Confidence:   model.ConfidenceApproximate,
		ObservedAt:   time.Now().UTC(),
	}); err != nil {
		return err
	}
	if s.crash {
		time.Sleep(300 * time.Millisecond) // Emit only queued the edge on the stream; let it flush
		os.Exit(3)
	}
	<-ctx.Done()
	return ctx.Err()
}

func (*streamSource) Close(context.Context) error { return nil }

func main() { plugin.ServeSource(&streamSource{}) }
