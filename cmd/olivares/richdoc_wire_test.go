// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"testing"

	"github.com/olivaresai/olivares/connectors/contentsource"
	"github.com/olivaresai/olivares/sdk"
)

// minimalContentSource is a trivial contentsource.Source; the completeness variant adds
// the capability so we can prove the mode wrapper re-exposes it (the wrapper embeds the
// Source INTERFACE, which erases the concrete type — the capability must be forwarded).
type minimalContentSource struct{ complete *bool }

func (minimalContentSource) Descriptor() sdk.Descriptor { return sdk.Descriptor{} }
func (minimalContentSource) Kind() contentsource.ContentClass {
	return contentsource.ClassDocument
}
func (minimalContentSource) Open(context.Context, sdk.Config) error { return nil }
func (minimalContentSource) List(context.Context, string) ([]contentsource.DocRef, string, error) {
	return nil, "", nil
}
func (minimalContentSource) Fetch(context.Context, string) (contentsource.Document, error) {
	return contentsource.Document{}, nil
}
func (minimalContentSource) Close(context.Context) error { return nil }

func (s minimalContentSource) ListingComplete() bool { return s.complete != nil && *s.complete }

func TestWrapContentSourceMode_ForwardsCompleteness(t *testing.T) {
	incomplete := false
	src := minimalContentSource{complete: &incomplete}
	wrapped := wrapContentSourceMode(src, "live")

	r, ok := wrapped.(contentsource.CompletenessReporter)
	if !ok {
		t.Fatal("wrapped source does not expose CompletenessReporter — the #7 delete gate would silently never fire")
	}
	if r.ListingComplete() {
		t.Error("wrapped source reported complete, want the forwarded false")
	}
	incomplete = true // flips through the forwarded pointer
	if !r.ListingComplete() {
		t.Error("wrapped source did not reflect the underlying completeness change")
	}
}

func TestWrapContentSourceMode_DefaultsCompleteWithoutCapability(t *testing.T) {
	// gdrive.New() is a real content source that does not implement CompletenessReporter.
	wrapped := wrapContentSourceMode(buildMustSource(t, "gdrive"), "live")
	r, ok := wrapped.(contentsource.CompletenessReporter)
	if !ok {
		t.Fatal("mode wrapper should always expose CompletenessReporter")
	}
	if !r.ListingComplete() {
		t.Error("a source without the capability must be reported complete (listing authoritative)")
	}
}

func buildMustSource(t *testing.T, kind string) contentsource.Source {
	t.Helper()
	src, ok := buildContentSource(kind, nil)
	if !ok {
		t.Fatalf("buildContentSource(%q) not wired", kind)
	}
	return src
}
