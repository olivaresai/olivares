// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package plugin

import (
	"context"
	"fmt"
	"io"

	goplugin "github.com/hashicorp/go-plugin"
	"google.golang.org/grpc"

	"github.com/olivaresai/olivares/sdk"
	pb "github.com/olivaresai/olivares/sdk/plugin/genpb/olivaresv1"
)

// ContentSourcePlugin adapts a ContentSource to hashicorp/go-plugin. On the
// plugin side Impl is the real source; on the host side Impl is nil and
// GRPCClient returns a client satisfying sdk.ContentSource.
type ContentSourcePlugin struct {
	goplugin.NetRPCUnsupportedPlugin
	// Impl is the content source served by a plugin process; nil on the host.
	Impl sdk.ContentSource
}

var _ goplugin.GRPCPlugin = (*ContentSourcePlugin)(nil)

// GRPCServer registers the content source's gRPC service on the plugin side.
func (p *ContentSourcePlugin) GRPCServer(_ *goplugin.GRPCBroker, s *grpc.Server) error {
	pb.RegisterContentSourceServiceServer(s, &contentSourceServer{impl: p.Impl})
	return nil
}

// GRPCClient builds the host-side adapter, caching the descriptor and declared
// capabilities. Delta methods are exposed only when the plugin declared
// sdk.CapabilityContentDelta in Describe.
func (p *ContentSourcePlugin) GRPCClient(ctx context.Context, _ *goplugin.GRPCBroker, c *grpc.ClientConn) (any, error) {
	client := pb.NewContentSourceServiceClient(c)
	dctx, cancel := context.WithTimeout(ctx, describeTimeout)
	defer cancel()
	resp, err := client.Describe(dctx, &pb.Empty{})
	if err != nil {
		return nil, fmt.Errorf("plugin: content source describe failed at dispense: %w", err)
	}
	base := &contentSourceClient{
		client: client,
		desc:   descriptorFromPB(resp.GetDescriptor_()),
	}
	if hasCapability(resp.GetCapabilities(), sdk.CapabilityContentDelta) {
		return &deltaContentSourceClient{contentSourceClient: base}, nil
	}
	return base, nil
}

// --- server side (runs in the plugin process) ---------------------------------

type contentSourceServer struct {
	pb.UnimplementedContentSourceServiceServer
	impl sdk.ContentSource
}

func (s *contentSourceServer) Describe(_ context.Context, _ *pb.Empty) (*pb.DescribeResponse, error) {
	caps := []string(nil)
	if _, ok := s.impl.(sdk.DeltaContentSource); ok {
		caps = append(caps, sdk.CapabilityContentDelta)
	}
	return &pb.DescribeResponse{Descriptor_: descriptorToPB(s.impl.Descriptor()), Capabilities: caps}, nil
}

func (s *contentSourceServer) Open(ctx context.Context, req *pb.OpenRequest) (*pb.Empty, error) {
	if err := s.impl.Open(ctx, configFromPB(req.GetConfig())); err != nil {
		return nil, err
	}
	return &pb.Empty{}, nil
}

// List streams ONE bounded page of refs (F5). It honors the host's resume cursor and
// per-page ceilings and — unlike the old handler — does NOT drain the source's whole corpus:
// it stops at the ceiling (checked at plugin-page boundaries so a resume cursor stays valid)
// or on exhaustion, then sends the page TERMINATOR (an empty-doc_id ref carrying next_cursor;
// "" when exhausted). The host resumes with that cursor. A giant single plugin page is still
// capped host-side (contentSourceClient.ListPage), which fails closed against a hostile source.
func (s *contentSourceServer) List(req *pb.ContentListRequest, stream grpc.ServerStreamingServer[pb.ContentDocRef]) error {
	maxItems := int(req.GetMaxItems())
	maxBytes := req.GetMaxBytes()
	cursor := req.GetCursor()
	sent := 0
	var nbytes int64
	for {
		refs, next, err := s.impl.List(stream.Context(), cursor)
		if err != nil {
			return err
		}
		for _, ref := range refs {
			m := contentDocRefToPB(ref)
			if err := stream.Send(m); err != nil {
				return err
			}
			sent++
			nbytes += contentRefWireSize(m)
		}
		cursor = next
		// Stop at the source's exhaustion or the host's page ceiling (checked at the
		// plugin-page boundary so `next` remains a valid resume cursor). Send the terminator.
		if next == "" || (maxItems > 0 && sent >= maxItems) || (maxBytes > 0 && nbytes >= maxBytes) {
			return stream.Send(&pb.ContentDocRef{NextCursor: next})
		}
	}
}

// contentRefWireSize approximates a ref's on-wire footprint for the byte ceiling.
func contentRefWireSize(m *pb.ContentDocRef) int64 {
	return int64(len(m.GetDocId()) + len(m.GetTitle()) + len(m.GetContentType()))
}

func (s *contentSourceServer) Fetch(ctx context.Context, req *pb.ContentFetchRequest) (*pb.ContentDocument, error) {
	doc, err := s.impl.Fetch(ctx, req.GetDocId())
	if err != nil {
		return nil, err
	}
	return contentDocumentToPB(doc), nil
}

func (s *contentSourceServer) FetchACL(ctx context.Context, req *pb.ContentFetchRequest) (*pb.ContentACLResult, error) {
	live, ok := s.impl.(sdk.DeltaContentSource)
	if !ok {
		return nil, fmt.Errorf("plugin: content source does not declare %s", sdk.CapabilityContentDelta)
	}
	res, err := live.FetchACL(ctx, req.GetDocId())
	if err != nil {
		return nil, err
	}
	return contentACLResultToPB(res), nil
}

func (s *contentSourceServer) DeltaList(req *pb.ContentDeltaRequest, stream grpc.ServerStreamingServer[pb.ContentChange]) error {
	live, ok := s.impl.(sdk.DeltaContentSource)
	if !ok {
		return fmt.Errorf("plugin: content source does not declare %s", sdk.CapabilityContentDelta)
	}
	page, err := live.DeltaList(stream.Context(), req.GetCursor())
	if err != nil {
		return err
	}
	if len(page.Changes) == 0 {
		if page.NextToken != "" || page.ResumeToken != "" || page.Expired {
			return stream.Send(contentDeltaPageMetaToPB(page))
		}
		return nil
	}
	for _, change := range page.Changes {
		if err := stream.Send(contentChangeToPB(change, page)); err != nil {
			return err
		}
	}
	return nil
}

func (s *contentSourceServer) Close(ctx context.Context, _ *pb.Empty) (*pb.Empty, error) {
	if err := s.impl.Close(ctx); err != nil {
		return nil, err
	}
	return &pb.Empty{}, nil
}

// --- client side (runs in the host process) -----------------------------------

// defaultListPageBytes bounds a legacy List() call (no explicit host ceiling) so even the
// non-paged code path cannot drain an unbounded source into host RAM (F5).
const defaultListPageBytes = 8 << 20 // 8 MiB

// hardListItemsFloor is the item-count backstop used only when the caller sets NO byte
// ceiling. The BYTE ceiling is the real RAM guard; item count is a secondary bound sized far
// above any realistic single plugin page so a well-behaved source with a large page (that
// still returns a resume cursor) is never falsely truncated (F5 review finding).
const hardListItemsFloor = 1 << 20 // 1,048,576 refs

type contentSourceClient struct {
	client pb.ContentSourceServiceClient
	desc   sdk.Descriptor
}

var (
	_ sdk.ContentSource      = (*contentSourceClient)(nil)
	_ sdk.PagedContentSource = (*contentSourceClient)(nil)
)

func (c *contentSourceClient) Descriptor() sdk.Descriptor { return c.desc }

func (c *contentSourceClient) Open(ctx context.Context, cfg sdk.Config) error {
	_, err := c.client.Open(ctx, &pb.OpenRequest{Config: configToPB(cfg)})
	return err
}

// List reads one page with a conservative default BYTE ceiling so even this legacy path is
// bounded; hosts that page through a large corpus should call ListPage with their ceilings.
func (c *contentSourceClient) List(ctx context.Context, cursor string) ([]sdk.DocRef, string, error) {
	refs, next, _, err := c.ListPage(ctx, cursor, 0, defaultListPageBytes)
	return refs, next, err
}

// ListPage reads ONE bounded, resumable page from the plugin (F5). The BYTE ceiling
// (hardBytes) is the real RAM guard; the item ceiling is a large backstop for callers that
// pass no byte bound, sized far above any realistic plugin page so a well-behaved source
// with a large page (that DOES return a resume cursor) is never falsely truncated. The hard
// ceilings sit ABOVE the request so a well-behaved server's terminator lands first. If a
// ceiling is hit before the terminator (a source that ignored the bound), it stops reading —
// RAM stays bounded — and returns complete=false so the host withholds orphan deletion.
// complete is a PER-CALL return, never shared state, so concurrent syncs cannot clobber it.
func (c *contentSourceClient) ListPage(ctx context.Context, cursor string, maxItems, maxBytes int) (refs []sdk.DocRef, next string, complete bool, err error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stream, serr := c.client.List(ctx, &pb.ContentListRequest{
		Cursor: cursor, MaxItems: int32(maxItems), MaxBytes: int64(maxBytes),
	})
	if serr != nil {
		return nil, "", false, serr
	}
	// hardBytes: the RAM guard, generously above the requested byte ceiling.
	var hardBytes int64
	if maxBytes > 0 {
		hardBytes = 2 * int64(maxBytes)
	}
	// hardItems: a large backstop above max(2*maxItems, floor) so a legitimate large page
	// completes; it only bites when there is no byte ceiling or on a pathological ref count.
	hardItems := hardListItemsFloor
	if maxItems > 0 && 2*maxItems+16 > hardItems {
		hardItems = 2*maxItems + 16
	}
	var nbytes int64
	for {
		msg, rerr := stream.Recv()
		if rerr == io.EOF {
			// Clean end with no terminator: an exhausted source (or a legacy server).
			return refs, "", true, nil
		}
		if rerr != nil {
			return nil, "", false, rerr
		}
		if msg.GetDocId() == "" { // page terminator: carries the resume cursor
			return refs, msg.GetNextCursor(), true, nil
		}
		refs = append(refs, contentDocRefFromPB(msg))
		nbytes += contentRefWireSize(msg)
		if (hardBytes > 0 && nbytes >= hardBytes) || len(refs) >= hardItems {
			// The server ignored the ceiling. Stop reading (RAM bounded) and report the page
			// incomplete — there is no resume cursor, so deletes must be withheld.
			cancel()
			return refs, "", false, nil
		}
	}
}

func (c *contentSourceClient) Fetch(ctx context.Context, docID string) (sdk.Document, error) {
	doc, err := c.client.Fetch(ctx, &pb.ContentFetchRequest{DocId: docID})
	if err != nil {
		return sdk.Document{}, err
	}
	return contentDocumentFromPB(doc), nil
}

func (c *contentSourceClient) Close(ctx context.Context) error {
	_, err := c.client.Close(ctx, &pb.Empty{})
	return err
}

type deltaContentSourceClient struct {
	*contentSourceClient
}

var _ sdk.DeltaContentSource = (*deltaContentSourceClient)(nil)

func (c *deltaContentSourceClient) DeltaList(ctx context.Context, cursor string) (sdk.DeltaPage, error) {
	stream, err := c.client.DeltaList(ctx, &pb.ContentDeltaRequest{Cursor: cursor})
	if err != nil {
		return sdk.DeltaPage{}, err
	}
	var page sdk.DeltaPage
	for {
		msg, err := stream.Recv()
		if err == io.EOF {
			return page, nil
		}
		if err != nil {
			return sdk.DeltaPage{}, err
		}
		applyContentDeltaMetaFromPB(&page, msg)
		if change, ok := contentChangeFromPB(msg); ok {
			page.Changes = append(page.Changes, change)
		}
	}
}

func (c *deltaContentSourceClient) FetchACL(ctx context.Context, docID string) (sdk.ACLResult, error) {
	res, err := c.client.FetchACL(ctx, &pb.ContentFetchRequest{DocId: docID})
	if err != nil {
		return sdk.ACLResult{}, err
	}
	return contentACLResultFromPB(res), nil
}

func hasCapability(caps []string, want string) bool {
	for _, cap := range caps {
		if cap == want {
			return true
		}
	}
	return false
}
