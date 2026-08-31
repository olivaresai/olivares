// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	goplugin "github.com/hashicorp/go-plugin"

	"github.com/olivaresai/olivares/sdk"
	sdkplugin "github.com/olivaresai/olivares/sdk/plugin"
)

// LoadContentSourcePluginVerified launches an EXTERNAL (third-party)
// content-source plugin with its sha256 pinned at exec time, opens it with cfg,
// and returns a tracked sdk.ContentSource client. The pinning semantics match
// LoadSourcePluginVerified exactly: malformed pins refuse, and go-plugin
// re-hashes the executable immediately before exec so the verified bytes are the
// executed bytes. Signature admission happens in the composition root before
// this call (cmd/olivares/externalplugins.go, admitExternalPlugin).
//
// Runtime deliberately returns the Apache SDK interface, not the in-tree
// connectors/contentsource interface, so core does not import outward across the
// license/layer boundary. The composition root wraps this SDK handle for the
// knowledge module.
func (r *Runtime) LoadContentSourcePluginVerified(path string, cfg sdk.Config, _ string, sha256Hex string) (sdk.ContentSource, error) {
	sum, err := hex.DecodeString(sha256Hex)
	if err != nil || len(sum) != sha256.Size {
		return nil, fmt.Errorf("runtime: external content-source plugin %q: pinned digest is not a sha256 hex digest (a supplied-but-unusable pin refuses, never degrades to an unpinned launch)", path)
	}
	secure := &goplugin.SecureConfig{Checksum: sum, Hash: sha256.New()}
	raw, client, err := r.dispense(path, sdkplugin.ContentSourcePluginMap(), sdkplugin.ContentSourcePluginName, secure)
	if err != nil {
		return nil, err
	}
	conn, ok := raw.(sdk.ContentSource)
	if !ok {
		client.Kill()
		return nil, fmt.Errorf("runtime: plugin %q did not dispense a ContentSource (%T)", path, raw)
	}
	base := &trackedContentSourcePlugin{src: conn, client: client, rt: r}
	if err := base.Open(context.Background(), cfg); err != nil {
		client.Kill()
		return nil, err
	}
	r.trackClient(client)
	if live, ok := conn.(sdk.DeltaContentSource); ok {
		return &trackedDeltaContentSourcePlugin{trackedContentSourcePlugin: base, live: live}, nil
	}
	return base, nil
}

type trackedContentSourcePlugin struct {
	src    sdk.ContentSource
	client *goplugin.Client
	rt     *Runtime
}

var (
	_ sdk.ContentSource      = (*trackedContentSourcePlugin)(nil)
	_ sdk.PagedContentSource = (*trackedContentSourcePlugin)(nil)
)

func (a *trackedContentSourcePlugin) Descriptor() sdk.Descriptor { return a.src.Descriptor() }

func (a *trackedContentSourcePlugin) Open(ctx context.Context, cfg sdk.Config) error {
	return a.src.Open(ctx, cfg)
}

func (a *trackedContentSourcePlugin) List(ctx context.Context, cursor string) ([]sdk.DocRef, string, error) {
	return a.src.List(ctx, cursor)
}

// ListPage re-exposes the wrapped client's bounded-pagination capability, which this concrete
// wrapper would otherwise ERASE (F5): the knowledge host asserts sdk.PagedContentSource,
// and without this the per-page RAM ceiling + completeness signal never reach it and the whole
// F5 bounded-wire fix is inert in production. Delegates to the client's ListPage when present;
// a client without it falls back to the (bounded) List, reported complete.
func (a *trackedContentSourcePlugin) ListPage(ctx context.Context, cursor string, maxItems, maxBytes int) ([]sdk.DocRef, string, bool, error) {
	if paged, ok := a.src.(sdk.PagedContentSource); ok {
		return paged.ListPage(ctx, cursor, maxItems, maxBytes)
	}
	refs, next, err := a.src.List(ctx, cursor)
	return refs, next, true, err
}

func (a *trackedContentSourcePlugin) Fetch(ctx context.Context, docID string) (sdk.Document, error) {
	return a.src.Fetch(ctx, docID)
}

func (a *trackedContentSourcePlugin) Close(ctx context.Context) error {
	err := a.src.Close(ctx)
	if a.rt != nil {
		a.rt.untrackClient(a.client)
	}
	if a.client != nil {
		a.client.Kill()
	}
	return err
}

type trackedDeltaContentSourcePlugin struct {
	*trackedContentSourcePlugin
	live sdk.DeltaContentSource
}

var _ sdk.DeltaContentSource = (*trackedDeltaContentSourcePlugin)(nil)

func (a *trackedDeltaContentSourcePlugin) DeltaList(ctx context.Context, sinceToken string) (sdk.DeltaPage, error) {
	return a.live.DeltaList(ctx, sinceToken)
}

func (a *trackedDeltaContentSourcePlugin) FetchACL(ctx context.Context, docID string) (sdk.ACLResult, error) {
	return a.live.FetchACL(ctx, docID)
}
