// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"

	"github.com/olivaresai/olivares/connectors/contentsource"
	"github.com/olivaresai/olivares/sdk"
)

func wrapSDKContentSource(src sdk.ContentSource) contentsource.Source {
	if src == nil {
		return nil
	}
	base := sdkContentSourceAdapter{src: src}
	if live, ok := src.(sdk.DeltaContentSource); ok {
		return sdkLiveContentSourceAdapter{sdkContentSourceAdapter: base, live: live}
	}
	return base
}

type sdkContentSourceAdapter struct {
	src sdk.ContentSource
}

var _ contentsource.Source = (*sdkContentSourceAdapter)(nil)

func (a sdkContentSourceAdapter) Descriptor() sdk.Descriptor { return a.src.Descriptor() }

func (a sdkContentSourceAdapter) Kind() contentsource.ContentClass {
	return contentsource.ClassDocument
}

func (a sdkContentSourceAdapter) Open(ctx context.Context, cfg sdk.Config) error {
	return a.src.Open(ctx, cfg)
}

func (a sdkContentSourceAdapter) List(ctx context.Context, cursor string) ([]contentsource.DocRef, string, error) {
	refs, next, err := a.src.List(ctx, cursor)
	if err != nil {
		return nil, "", err
	}
	out := make([]contentsource.DocRef, len(refs))
	for i, ref := range refs {
		out[i] = contentsource.DocRef{
			DocID:       ref.DocID,
			Title:       ref.Title,
			ContentType: ref.ContentType,
			ModifiedAt:  ref.ModifiedAt,
		}
	}
	return out, next, nil
}

func (a sdkContentSourceAdapter) Fetch(ctx context.Context, docID string) (contentsource.Document, error) {
	doc, err := a.src.Fetch(ctx, docID)
	if err != nil {
		return contentsource.Document{}, err
	}
	return contentsource.Document{
		Source:         contentsource.SourceKind(doc.Source),
		DocID:          doc.DocID,
		Title:          doc.Title,
		Body:           string(doc.Body),
		ContentType:    doc.ContentType,
		ACL:            doc.ACL,
		Classification: doc.Classification,
		SpaceRef:       doc.SpaceRef,
		ModifiedAt:     doc.ModifiedAt,
		Attributes:     doc.Attributes,
		ExternalLabels: doc.ExternalLabels,
	}, nil
}

// ListPage makes a plugin-backed source a contentsource.PagedSource (F5): the host's
// per-page ceilings reach the plugin over the wire, so the reconciliation pages through a
// large/hostile external source with bounded RAM instead of draining it. It returns the
// PER-CALL completeness (never shared state), so overlapping syncs cannot clobber it. A
// source whose sdk client predates the paged capability falls back to the bounded List and
// is reported complete (its List is already bounded host-side).
func (a sdkContentSourceAdapter) ListPage(ctx context.Context, cursor string, maxItems, maxBytes int) ([]contentsource.DocRef, string, bool, error) {
	var (
		refs     []sdk.DocRef
		next     string
		complete bool
		err      error
	)
	if paged, ok := a.src.(sdk.PagedContentSource); ok {
		refs, next, complete, err = paged.ListPage(ctx, cursor, maxItems, maxBytes)
	} else {
		refs, next, err = a.src.List(ctx, cursor)
		complete = true
	}
	if err != nil {
		return nil, "", false, err
	}
	out := make([]contentsource.DocRef, len(refs))
	for i, ref := range refs {
		out[i] = contentsource.DocRef{
			DocID:       ref.DocID,
			Title:       ref.Title,
			ContentType: ref.ContentType,
			ModifiedAt:  ref.ModifiedAt,
		}
	}
	return out, next, complete, nil
}

func (a sdkContentSourceAdapter) Close(ctx context.Context) error { return a.src.Close(ctx) }

var _ contentsource.PagedSource = sdkContentSourceAdapter{}

type sdkLiveContentSourceAdapter struct {
	sdkContentSourceAdapter
	live sdk.DeltaContentSource
}

var _ contentsource.LiveSource = (*sdkLiveContentSourceAdapter)(nil)

func (a sdkLiveContentSourceAdapter) DeltaList(ctx context.Context, sinceToken string) (contentsource.DeltaPage, error) {
	page, err := a.live.DeltaList(ctx, sinceToken)
	if err != nil {
		return contentsource.DeltaPage{}, err
	}
	out := contentsource.DeltaPage{
		Changes:     make([]contentsource.DeltaEntry, len(page.Changes)),
		NextToken:   page.NextToken,
		ResumeToken: page.ResumeToken,
		Expired:     page.Expired,
	}
	for i, change := range page.Changes {
		out.Changes[i] = contentsource.DeltaEntry{
			DocRef: contentsource.DocRef{
				DocID:       change.DocRef.DocID,
				Title:       change.DocRef.Title,
				ContentType: change.DocRef.ContentType,
				ModifiedAt:  change.DocRef.ModifiedAt,
			},
			ChangeKind: contentsource.ChangeKind(change.ChangeKind),
		}
	}
	return out, nil
}

func (a sdkLiveContentSourceAdapter) FetchACL(ctx context.Context, docID string) (contentsource.ACLResult, error) {
	res, err := a.live.FetchACL(ctx, docID)
	if err != nil {
		return contentsource.ACLResult{}, err
	}
	return contentsource.ACLResult{
		ACL:            res.ACL,
		ExternalLabels: res.ExternalLabels,
		Classification: res.Classification,
	}, nil
}
