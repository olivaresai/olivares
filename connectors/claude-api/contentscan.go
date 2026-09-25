// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// contentscan.go is the inline proxy's CONTENT-COLLECTION surface: it walks a
// /v1/messages request or response and extracts, per the Anthropic content-block wire
// shapes, the classifiable plaintext plus the channels that carry content it CANNOT
// reduce to plaintext (non-text base64, a remote file_id, an opaque url source, an
// Anthropic-encrypted web-search blob, or an unmodeled block type). It is the wire half
// the governed decider (cmd/olivares, AGPL) and the OPTIONAL commercial content firewall
// (enterprise/contentfirewall, closed) both read — neither imports the other's package,
// exactly as egressdecision.go does for the server-tool gate.
//
// THE GAP IT CLOSES (capability-gaps #9). The decider's DLP previously read ONLY the
// typed text blocks (b.Text), so a prompt or response that smuggled sensitive data inside
// a document, an image, a tool_result, a file_id reference or a web_search result bypassed
// DLP entirely. This collector surfaces those channels: extractable text goes to the
// classifier; bounded base64/URL encodings are decoded and rescanned, while anything
// opaque is marked UNSCANNED, so the decider's
// deny-closed posture (modules/inferenceproxy.dlpPolicy.unscannedDenied — "*" does NOT
// cover unscanned) closes the bypass. This Apache-licensed collection runs in BOTH
// builds; the closed firewall's deep detectors are the additive paid layer above it.
//
// MINIMAL DATA (docs/SECURITY-HARDENING.md). The collector handles prompt/response content in flight — the
// same posture as the connector's existing fingerprinting — and returns the extracted
// text to its in-process caller (the deterministic classifier / firewall detectors run on
// it and emit only redacted hashes). It NEVER persists, logs, or transmits the content;
// a channel's Ref carries only non-sensitive structure (a media_type, a url HOST, a
// file_id handle, a tool name, the block type), never the bytes, the data: payload, or a
// query string.
//
// DENY-CLOSED BY CONSTRUCTION. Unrecognized or unparseable non-text blocks are marked
// unscanned (not silently skipped), so a block shape this connector does not yet model can
// never slip sensitive content past DLP — the fail-safe direction.
//
// TWO ACCOUNTING DOMAINS. Compatibility is what the collector did before decoded channels:
// the original channels (text, the raw tool name+input, a source URL, a decoded text/plain
// base64 document) and the classifier texts with their bounded URL/base64 decodings, under
// the legacy limits and with the legacy opaque outcomes. Added work is everything beyond
// it: every JSON-unescaped key and string of a tool input, the decodings of those strings,
// and one EXPANSION channel, with the source's kind and role, for each decoding of any
// source occurrence. Added work has its own limits. Reaching one stops only added work and
// marks each affected source unscanned once; it never removes, starves or charges
// compatibility coverage. A result therefore holds the original channels, plus bounded
// expansion channels, plus at most one overflow disposition per original source.

package claudeapi

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/url"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Content channel kinds. They name WHERE a piece of content came from so a consumer can
// treat untrusted channels (tool_result, web_search_result, document, search_result)
// differently from the caller's own message text. Stable identifiers (used in findings).
const (
	ChannelSystemText      = "system_text"
	ChannelMessageText     = "message_text"
	ChannelDocument        = "document"
	ChannelImage           = "image"
	ChannelFileID          = "file_id"
	ChannelToolResult      = "tool_result"
	ChannelWebSearchResult = "web_search_result"
	ChannelSearchResult    = "search_result"
	ChannelThinking        = "thinking"
	ChannelToolUse         = "tool_use"
	ChannelServerToolUse   = "server_tool_use"
	ChannelUnknown         = "unknown"
)

// maxContentDepth bounds recursion into nested content (a document "content" source, a
// tool_result content array) and, continuing from the enclosing block's depth, into tool
// input nesting: the input value sits at the block's depth and a container's members one
// level deeper. maxEncodingDepth separately bounds nested URL/base64 layers; every source
// string, an argument string included, starts at encoding depth zero. Anthropic's shapes
// nest at most a couple of levels; these are hostile-input backstops, not real limits —
// content below them is marked unscanned, never silently dropped.
//
// maxDecodedContentSize is the unchanged legacy bound. Per request or response, it caps the
// decoded text the compatibility decoding may newly add to the classifier input. The same
// value is also the per-item guard of every decoder in both domains: a percent-encoded text
// or a base64 source above it, or a base64 candidate longer than its encoded length, is
// marked unscanned without being decoded.
//
// The four added-work limits apply per request or response, each to its own unit and with
// its own fixed opaque reason. They are provisional construction bounds on the work this
// collector adds, not measured capacity and not a bound on the process heap. Input is
// charged before a decoder is constructed or advanced, and a token before each Token call;
// output is charged after the standard decoder has materialized the string it emits.
const (
	maxContentDepth       = 6
	maxEncodingDepth      = 6
	maxDecodedContentSize = 1 << 20
	maxAddedOutput        = 32 << 20 // "decoded/output-limit": bytes of every expansion channel
	maxAddedInput         = 64 << 20 // "decoded/input-limit": argument tokenizer and added decoder input
	maxAddedTokens        = 1 << 18  // "arguments/token-limit": JSON tokens read by the argument walk
	maxAddedChannels      = 1 << 16  // "decoded/channel-limit": expansion channels, scannable and opaque
)

// ContentChannel is one extracted unit of message/response content. Text is the classifiable
// plaintext ("" when the channel is opaque); Scannable=false means it contributed to
// Unscanned (binary/file_id/encrypted/opaque/unknown). Ref is non-sensitive structural
// context (media_type, url host, file_id, tool name, block type) — never the content bytes.
// Expansion channels use the fixed refs "arguments/key", "arguments/string" and
// "encoded"; these refs never contain the decoded key or value. Opaque reasons include
// "arguments/unparseable", "arguments/max-depth", "arguments/ambiguous-member",
// "encoded/undecodable-or-oversized", "encoded/max-depth" and "encoded/oversized".
// Added-work overflow uses "decoded/output-limit", "decoded/input-limit",
// "arguments/token-limit" or "decoded/channel-limit". These are dispositions of an
// original source, not additional wire blocks.
type ContentChannel struct {
	Kind      string
	Role      string // "user" | "assistant" | "system" | ""
	Text      string
	Scannable bool
	Ref       string
}

// CollectedContent is the result of walking a request or response: every channel found,
// the distinct extractable texts (the classifier input), and whether ANY channel carried
// content that could not be reduced to plaintext (the unscanned signal the deny-closed DLP
// policy consumes).
//
// Channels retains original channels and adds bounded argument/encoding expansions.
// Each original source has at most one copy of an opaque reason and at most one
// added-work overflow disposition. Repeated expansions still consume the added budget
// even when Texts deduplicates their text. Reaching an added limit does not stop the
// original walk or spend the separate legacy decoding allowance.
//
// Per call, added work is bounded by 32 MiB of emitted expansion text, 64 MiB of
// tokenizer/decoder input, 262,144 JSON tokens and 65,536 added channels (including
// opaque channels). Argument and encoding depth each have a six-level bound. The
// unchanged 1 MiB legacy decoding allowance and decoder per-item guards also apply.
// These are work limits, not a heap or transport-capacity guarantee: a decoder can
// materialize a string before its output charge. Unscanned reports incomplete coverage;
// this collector neither grants a policy exception nor decides whether to forward.
type CollectedContent struct {
	Channels  []ContentChannel
	Texts     []string
	Unscanned bool
}

// CollectRequestContent walks a request's system prefix and message content. System blocks
// are attributed role "system"; message blocks carry their message role. The work
// limits and expansion/overflow invariants of CollectedContent apply to the whole call.
func CollectRequestContent(req MessageRequest) CollectedContent {
	c := &contentCollector{}
	c.blocks("system", req.System, 0, "")
	for _, m := range req.Messages {
		c.blocks(m.Role, m.Content, 0, "")
	}
	return c.result()
}

// CollectResponseContent walks a response's content (the assistant turn) — text, thinking,
// tool_use/server_tool_use action args, and any web_search/search results round-tripped in.
// It starts a new set of the per-call allowances documented on CollectedContent.
func CollectResponseContent(resp MessageResponse) CollectedContent {
	c := &contentCollector{}
	c.blocks(roleAssistant, resp.Content, 0, "")
	return c.result()
}

// --- collector --------------------------------------------------------------------------

type contentCollector struct {
	channels  []ContentChannel
	texts     []string
	seenText  map[string]bool // classifier texts; true once the compatibility domain has seen one
	unscanned bool

	// compatDecoded is the legacy total of decoded text the compatibility decoding newly
	// classified, bounded by maxDecodedContentSize. Added work never reads or changes it.
	compatDecoded int

	// Added work, each unit counted against its own limit. addedExhausted is the fixed
	// reason of the first limit reached; no added work runs after it.
	addedInput, addedOutput, addedTokens, addedChannels int
	addedExhausted                                      string
}

// source is one original occurrence: a channel read from the wire, such as a text, a raw
// tool call or a URL. Everything decoded from it keeps its kind and role, and it carries
// each opaque reason at most once, however many of its strings produce that reason. Two
// occurrences with the same kind, role and text are still distinct sources.
type source struct {
	kind, role string
	reasons    []string
}

func (c *contentCollector) result() CollectedContent {
	return CollectedContent{Channels: c.channels, Texts: c.texts, Unscanned: c.unscanned}
}

// scannable records an original channel: extractable text read from the wire. It is
// compatibility output, never charged, and it returns the source that owns whatever is
// decoded from it. Empty text is not classified, but the channel is still recorded so
// the firewall can see structure.
func (c *contentCollector) scannable(kind, role, text, ref string) *source {
	c.channels = append(c.channels, ContentChannel{Kind: kind, Role: role, Text: text, Scannable: true, Ref: ref})
	src := &source{kind: kind, role: role}
	c.addText(text, true)
	c.decode(src, text, 0, true)
	return src
}

// addText adds text to the classifier input once. legacy marks a text the compatibility
// domain classifies; the result reports whether that domain sees it for the first time,
// its legacy condition for charging and decoding it further. A text that added work put
// into the classifier input first does not change that answer.
func (c *contentCollector) addText(text string, legacy bool) bool {
	t := strings.TrimSpace(text)
	if t == "" {
		return false
	}
	if c.seenText == nil {
		c.seenText = map[string]bool{}
	}
	seenLegacy, classified := c.seenText[t]
	if !classified {
		c.texts = append(c.texts, text)
	}
	if !legacy {
		if !classified {
			c.seenText[t] = false
		}
		return false
	}
	c.seenText[t] = true
	return !seenLegacy
}

// decode handles the bounded URL/base64 decodings of text, a string of src at encoding
// depth depth: each decoding is classified, recorded as an expansion channel of src and
// decoded further. Classifier deduplication never suppresses an expansion channel, so a
// decoding keeps its own provenance even when another source supplied the same bytes.
//
// legacy is true when the pre-decoded-channel collector decoded text. That decoding is
// compatibility work: it is not charged to added work, keeps the legacy bounds and
// outcomes, and continues only into decodings it classifies for the first time. The
// deeper layers of an already classified decoding, and every decoding of an argument
// string, are added work, done to keep this occurrence's provenance in the channels.
func (c *contentCollector) decode(src *source, text string, depth int, legacy bool) {
	var charge func(int) bool
	if !legacy {
		charge = func(n int) bool { return c.chargeInput(src, n) }
	}
	variants, unsafe, ok := textVariants(text, charge)
	if !ok {
		return // chargeInput recorded the overflow disposition
	}
	if unsafe {
		c.mark(src, "encoded/undecodable-or-oversized", !legacy)
	}
	if len(variants) == 0 {
		return
	}
	if depth >= maxEncodingDepth {
		c.mark(src, "encoded/max-depth", !legacy)
		return
	}
	for _, v := range variants {
		fresh := false // v continues as compatibility work
		if legacy {
			// The legacy bound is checked before deduplication, as it always was.
			if len(v) > maxDecodedContentSize-c.compatDecoded {
				c.mark(src, "encoded/oversized", false)
				return
			}
			if c.addText(v, true) {
				c.compatDecoded += len(v)
				fresh = true
			}
		}
		recorded := c.expand(src, v, "encoded")
		if !legacy {
			if !recorded {
				return
			}
			c.addText(v, false)
		}
		if fresh || recorded {
			c.decode(src, v, depth+1, fresh)
		}
	}
}

// expand records one decoded occurrence of src as a scannable expansion channel and
// reports whether it fit. Every occurrence is charged, even when the classifier already
// has the text, so repetition cannot make expansion free.
func (c *contentCollector) expand(src *source, text, ref string) bool {
	if !c.chargeChannel(src, len(text)) {
		return false
	}
	c.channels = append(c.channels, ContentChannel{
		Kind: src.kind, Role: src.role, Text: text, Scannable: true, Ref: ref,
	})
	return true
}

// mark records an opaque reason for src once. A reason found by added work is an expansion
// channel and is charged as one; a compatibility reason is not.
func (c *contentCollector) mark(src *source, reason string, added bool) {
	if slices.Contains(src.reasons, reason) {
		return
	}
	if added && !c.chargeChannel(src, 0) {
		return
	}
	src.reasons = append(src.reasons, reason)
	c.opaque(src.kind, src.role, reason)
}

// overflow marks src unscanned with the reason of the exhausted added-work limit. It is
// recorded at most once per source and is not charged, so it stays outside the exhausted
// expansion allowance.
func (c *contentCollector) overflow(src *source) {
	c.mark(src, c.addedExhausted, false)
}

// chargeInput charges bytes about to be given to the argument tokenizer or an added decoder.
func (c *contentCollector) chargeInput(src *source, n int) bool {
	return c.charge(src, &c.addedInput, n, maxAddedInput, "decoded/input-limit")
}

// chargeToken charges one JSON token about to be read by the argument walk.
func (c *contentCollector) chargeToken(src *source) bool {
	return c.charge(src, &c.addedTokens, 1, maxAddedTokens, "arguments/token-limit")
}

// chargeChannel charges one expansion channel that carries n bytes of text.
func (c *contentCollector) chargeChannel(src *source, n int) bool {
	return c.charge(src, &c.addedChannels, 1, maxAddedChannels, "decoded/channel-limit") &&
		c.charge(src, &c.addedOutput, n, maxAddedOutput, "decoded/output-limit")
}

// charge adds n to *used while added work runs and n fits under limit. Otherwise the first
// limit reached stops all added work, and src gets its overflow disposition. *used never
// exceeds limit, so the remaining-capacity comparison cannot overflow.
func (c *contentCollector) charge(src *source, used *int, n, limit int, reason string) bool {
	if c.addedExhausted == "" && n <= limit-*used {
		*used += n
		return true
	}
	if c.addedExhausted == "" {
		c.addedExhausted = reason
	}
	c.overflow(src)
	return false
}

// opaque records a channel whose content could NOT be reduced to plaintext, and trips the
// unscanned signal (the deny-closed bypass-closer). Ref is non-sensitive structure only.
func (c *contentCollector) opaque(kind, role, ref string) {
	c.channels = append(c.channels, ContentChannel{Kind: kind, Role: role, Scannable: false, Ref: ref})
	c.unscanned = true
}

// blocks walks a slice of content blocks. kindCtx, when set, is the enclosing untrusted
// channel (e.g. "tool_result", "document") so a nested text block is attributed to that
// channel rather than to the caller's own message text — the distinction the injection
// detector relies on.
func (c *contentCollector) blocks(role string, blocks []ContentBlock, depth int, kindCtx string) {
	if depth > maxContentDepth {
		// Too deep to parse safely — do not silently drop; mark unscanned (deny-closed).
		c.opaque(ChannelUnknown, role, "nested/max-depth")
		return
	}
	for _, b := range blocks {
		c.block(role, b, depth, kindCtx)
	}
}

func (c *contentCollector) block(role string, b ContentBlock, depth int, kindCtx string) {
	// A text block carries its text in the typed field (constructor- or wire-built).
	if b.Type == blockText {
		kind := kindCtx
		if kind == "" {
			if role == roleSystem {
				kind = ChannelSystemText
			} else {
				kind = ChannelMessageText
			}
		}
		c.scannable(kind, role, b.Text, "")
		return
	}

	// Every other block type is carried opaque in raw (UnmarshalJSON stashes the full
	// bytes there). A non-text block with no raw (an in-process constructor that set no
	// raw and no text) is unclassifiable → unscanned.
	if len(b.raw) == 0 {
		if b.Text != "" {
			c.scannable(firstNonEmptyStr(kindCtx, ChannelMessageText), role, b.Text, b.Type)
			return
		}
		c.opaque(firstNonEmptyStr(b.Type, ChannelUnknown), role, b.Type)
		return
	}

	switch b.Type {
	case "document":
		c.document(role, b.raw, depth)
	case "image":
		c.image(role, b.raw)
	case "tool_result":
		c.toolResult(role, b.raw, depth)
	case "web_search_tool_result":
		c.webSearchResult(role, b.raw)
	case blockSearchResult: // "search_result"
		c.searchResult(role, b.raw)
	case "thinking":
		c.thinking(role, b.raw)
	case "redacted_thinking":
		c.opaque(ChannelThinking, role, "redacted_thinking")
	case "tool_use":
		c.toolUse(ChannelToolUse, role, b.raw, depth)
	case "server_tool_use":
		c.toolUse(ChannelServerToolUse, role, b.raw, depth)
	default:
		// An unmodeled block type carrying content we cannot parse to plaintext. Deny-closed:
		// mark unscanned so the DLP policy can refuse it, never forward it blind.
		c.opaque(ChannelUnknown, role, b.Type)
	}
}

// --- per-block-type parsers (verified jun-2026 wire shapes) ------------------------------

type docSourceWire struct {
	Type      string          `json:"type"`
	MediaType string          `json:"media_type"`
	Data      string          `json:"data"`
	URL       string          `json:"url"`
	FileID    string          `json:"file_id"`
	Content   json.RawMessage `json:"content"`
}

type documentWire struct {
	Source  *docSourceWire `json:"source"`
	Title   string         `json:"title"`
	Context string         `json:"context"`
}

// document: source ∈ {text(plain), base64(binary), url(remote), file(file_id), content(nested)}.
// title/context are caller-supplied plaintext. Anything not reducible to text is unscanned.
func (c *contentCollector) document(role string, raw json.RawMessage, depth int) {
	var d documentWire
	if err := json.Unmarshal(raw, &d); err != nil {
		c.opaque(ChannelDocument, role, "document/unparseable")
		return
	}
	if d.Title != "" {
		c.scannable(ChannelDocument, role, d.Title, "document/title")
	}
	if d.Context != "" {
		c.scannable(ChannelDocument, role, d.Context, "document/context")
	}
	if d.Source == nil {
		c.opaque(ChannelDocument, role, "document/no-source")
		return
	}
	switch d.Source.Type {
	case "text":
		c.scannable(ChannelDocument, role, d.Source.Data, "document/text:"+d.Source.MediaType)
	case "content":
		nested := c.decodeBlocks(d.Source.Content)
		if nested == nil {
			c.opaque(ChannelDocument, role, "document/content-unparseable")
			return
		}
		c.blocks(role, nested, depth+1, ChannelDocument)
	case "base64":
		c.base64Source(ChannelDocument, role, d.Source.Data, d.Source.MediaType, "document/base64:")
	case "url":
		// The URL string is inspectable; the remote document it names is not.
		ref := "document/url:" + hostOf(d.Source.URL)
		c.scannable(ChannelDocument, role, d.Source.URL, ref)
		c.opaque(ChannelDocument, role, ref)
	case "file":
		c.opaque(ChannelFileID, role, "document/file:"+d.Source.FileID)
	default:
		c.opaque(ChannelDocument, role, "document/"+firstNonEmptyStr(d.Source.Type, "unknown-source"))
	}
}

type imageWire struct {
	Source *docSourceWire `json:"source"`
}

// image: vision content is never plaintext — every source kind is unscanned (the brief
// lists image as an unscanned channel).
func (c *contentCollector) image(role string, raw json.RawMessage) {
	var im imageWire
	if err := json.Unmarshal(raw, &im); err != nil || im.Source == nil {
		c.opaque(ChannelImage, role, "image/unparseable")
		return
	}
	switch im.Source.Type {
	case "base64":
		c.base64Source(ChannelImage, role, im.Source.Data, im.Source.MediaType, "image/base64:")
	case "url":
		ref := "image/url:" + hostOf(im.Source.URL)
		c.scannable(ChannelImage, role, im.Source.URL, ref)
		c.opaque(ChannelImage, role, ref)
	case "file":
		c.opaque(ChannelFileID, role, "image/file:"+im.Source.FileID)
	default:
		c.opaque(ChannelImage, role, "image/"+firstNonEmptyStr(im.Source.Type, "unknown-source"))
	}
}

// toolResult: content is either a plain string OR an array of blocks (text/image/...). It
// is the highest-value untrusted channel (external tool output fed back to the model), so
// nested blocks inherit the tool_result kind for the injection detector.
func (c *contentCollector) toolResult(role string, raw json.RawMessage, depth int) {
	var tr struct {
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(raw, &tr); err != nil || len(tr.Content) == 0 {
		c.opaque(ChannelToolResult, role, "tool_result/unparseable")
		return
	}
	if s, ok := decodeJSONString(tr.Content); ok {
		c.scannable(ChannelToolResult, role, s, "tool_result/string")
		return
	}
	if nested := c.decodeBlocks(tr.Content); nested != nil {
		c.blocks(role, nested, depth+1, ChannelToolResult)
		return
	}
	c.opaque(ChannelToolResult, role, "tool_result/opaque")
}

type webSearchResultWire struct {
	Type             string `json:"type"`
	Title            string `json:"title"`
	URL              string `json:"url"`
	EncryptedContent string `json:"encrypted_content"`
	ErrorCode        string `json:"error_code"`
}

// webSearchResult: content is an array of web_search_result items (title + url are
// plaintext; encrypted_content is Anthropic-opaque → unscanned) or an error object.
func (c *contentCollector) webSearchResult(role string, raw json.RawMessage) {
	var wr struct {
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(raw, &wr); err != nil || len(wr.Content) == 0 {
		c.opaque(ChannelWebSearchResult, role, "web_search/unparseable")
		return
	}
	var items []webSearchResultWire
	if err := json.Unmarshal(wr.Content, &items); err != nil {
		// The error shape ({type:web_search_tool_result_error, error_code}) is benign — an
		// error code, no content. Anything else opaque → unscanned.
		var errObj webSearchResultWire
		if json.Unmarshal(wr.Content, &errObj) == nil && errObj.ErrorCode != "" {
			return
		}
		c.opaque(ChannelWebSearchResult, role, "web_search/opaque")
		return
	}
	for _, it := range items {
		produced := false
		if t := strings.TrimSpace(it.Title + " " + it.URL); t != "" {
			c.scannable(ChannelWebSearchResult, role, t, "web_search/"+hostOf(it.URL))
			produced = true
		}
		if it.EncryptedContent != "" {
			// The model reads the DECRYPTED page; we cannot. Mark unscanned.
			c.opaque(ChannelWebSearchResult, role, "web_search/encrypted:"+hostOf(it.URL))
			produced = true
		}
		if !produced {
			// An item shape we do not model (a future web_search_result_vN, or content carried
			// in an unmodeled field) yields neither text nor an encrypted marker. Deny-closed
			// backstop: mark it unscanned, NEVER drop it — the model can read what we cannot.
			c.opaque(ChannelWebSearchResult, role, "web_search/non-text")
		}
	}
}

type searchResultWireIn struct {
	Source  string            `json:"source"`
	Title   string            `json:"title"`
	Content []json.RawMessage `json:"content"`
}

// searchResult (D2 RAG block): source + title + an array of text blocks — all plaintext.
func (c *contentCollector) searchResult(role string, raw json.RawMessage) {
	var sr searchResultWireIn
	if err := json.Unmarshal(raw, &sr); err != nil {
		c.opaque(ChannelSearchResult, role, "search_result/unparseable")
		return
	}
	if t := strings.TrimSpace(sr.Title + " " + sr.Source); t != "" {
		c.scannable(ChannelSearchResult, role, t, "search_result/meta")
	}
	any := false
	for _, blk := range sr.Content {
		var tb struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if json.Unmarshal(blk, &tb) == nil && tb.Type == blockText && tb.Text != "" {
			c.scannable(ChannelSearchResult, role, tb.Text, "search_result/text")
			any = true
		}
	}
	if !any && len(sr.Content) > 0 {
		c.opaque(ChannelSearchResult, role, "search_result/non-text")
	}
}

// thinking: the model's reasoning summary — plaintext (often empty when display:omitted).
func (c *contentCollector) thinking(role string, raw json.RawMessage) {
	var t struct {
		Thinking string `json:"thinking"`
	}
	if err := json.Unmarshal(raw, &t); err != nil {
		c.opaque(ChannelThinking, role, "thinking/unparseable")
		return
	}
	c.scannable(ChannelThinking, role, t.Thinking, "thinking")
}

// toolUse / server_tool_use: the model's tool invocation — name + JSON-stringified input.
// The unsafe-action detector reads the action args from this pre-existing raw channel, so
// it is recorded first and unconditionally. The decoded argument keys and strings follow
// as expansion channels of the same source; only that added work can stop at a limit.
func (c *contentCollector) toolUse(kind, role string, raw json.RawMessage, depth int) {
	var tu struct {
		Name  string          `json:"name"`
		Input json.RawMessage `json:"input"`
	}
	if err := json.Unmarshal(raw, &tu); err != nil {
		c.opaque(kind, role, kind+"/unparseable")
		return
	}
	var sb strings.Builder
	sb.WriteString(tu.Name)
	if len(tu.Input) > 0 {
		sb.WriteByte(' ')
		sb.Write(tu.Input) // original JSON remains transient and unchanged
	}
	src := c.scannable(kind, role, sb.String(), kind+":"+tu.Name)
	c.toolArguments(src, raw, depth)
}

var (
	// errArgumentLimit stops an argument walk after charge has recorded the source's
	// overflow disposition.
	errArgumentLimit = errors.New("claudeapi: added argument work limit reached")
	errArgumentShape = errors.New("claudeapi: tool-use block is not a JSON object")
)

// toolArguments exposes every key and string of the tool input, JSON-unescaped and in
// document order, from the standard tokenizer over the original block bytes. Unlike a
// map, the token stream keeps a repeated key's earlier values, and exact number tokens
// cannot turn an out-of-range float into a parse failure that hides its siblings.
// encoding/json matches field names case-insensitively, so every member that folds to
// "input" is exposed; a repeated input or name member marks the block ambiguous, since
// the raw channel shows only the last one and an executor may read another.
func (c *contentCollector) toolArguments(src *source, raw json.RawMessage, depth int) {
	if !c.chargeInput(src, len(raw)) {
		return
	}
	w := argumentWalk{c: c, src: src, dec: json.NewDecoder(bytes.NewReader(raw))}
	w.dec.UseNumber()
	if err := w.block(depth); err != nil && !errors.Is(err, errArgumentLimit) {
		c.mark(src, "arguments/unparseable", true)
	}
	if w.overDepth {
		c.mark(src, "arguments/max-depth", true)
	}
	if w.ambiguous {
		c.mark(src, "arguments/ambiguous-member", true)
	}
}

// argumentWalk is one bounded pass over a tool-use block. Every token is charged before
// it is read, including the tokens of skipped members and over-depth values.
type argumentWalk struct {
	c         *contentCollector
	src       *source
	dec       *json.Decoder
	overDepth bool // a key or value deeper than maxContentDepth was skipped
	ambiguous bool // an input or name member repeats
}

func (w *argumentWalk) block(depth int) error {
	tok, err := w.next()
	if err != nil {
		return err
	}
	if tok != json.Delim('{') {
		return errArgumentShape
	}
	inputs, names := 0, 0
	for w.dec.More() {
		if tok, err = w.next(); err != nil {
			return err
		}
		key, _ := tok.(string)
		switch {
		case strings.EqualFold(key, "input"):
			inputs++
			err = w.value(depth)
		case strings.EqualFold(key, "name"):
			names++
			err = w.skip()
		default:
			err = w.skip()
		}
		if err != nil {
			return err
		}
	}
	w.ambiguous = inputs > 1 || names > 1
	_, err = w.next() // the closing brace
	return err
}

// value exposes the keys and strings of the next JSON value, whose argument depth is
// depth; a container's keys and members are one level deeper. A key or value beyond
// maxContentDepth is skipped, not exposed, and its siblings are still walked.
func (w *argumentWalk) value(depth int) error {
	tok, err := w.next()
	if err != nil {
		return err
	}
	if depth > maxContentDepth {
		w.overDepth = true
		return w.skipFrom(tok)
	}
	switch tok := tok.(type) {
	case string:
		return w.expose(tok, "arguments/string")
	case json.Delim: // an opening delimiter; More stops before every closing one
		for w.dec.More() {
			if tok == '{' {
				if err := w.key(depth + 1); err != nil {
					return err
				}
			}
			if err := w.value(depth + 1); err != nil {
				return err
			}
		}
		_, err = w.next() // the matching closing delimiter
		return err
	}
	return nil // a number, bool or null carries no text
}

func (w *argumentWalk) key(depth int) error {
	tok, err := w.next()
	if err != nil {
		return err
	}
	if depth > maxContentDepth {
		w.overDepth = true
		return nil
	}
	key, _ := tok.(string)
	return w.expose(key, "arguments/key")
}

// skip consumes the next value without exposing it.
func (w *argumentWalk) skip() error {
	tok, err := w.next()
	if err != nil {
		return err
	}
	return w.skipFrom(tok)
}

// skipFrom consumes the rest of a value whose first token is tok.
func (w *argumentWalk) skipFrom(tok json.Token) error {
	open := 0
	for {
		switch tok {
		case json.Delim('{'), json.Delim('['):
			open++
		case json.Delim('}'), json.Delim(']'):
			open--
		}
		if open == 0 {
			return nil
		}
		var err error
		if tok, err = w.next(); err != nil {
			return err
		}
	}
}

// next charges one token, then reads it.
func (w *argumentWalk) next() (json.Token, error) {
	if !w.c.chargeToken(w.src) {
		return nil, errArgumentLimit
	}
	return w.dec.Token()
}

// expose records a decoded key or string as an expansion channel under a fixed Ref, then
// classifies and decodes it; the text never enters the Ref. An empty string carries
// nothing to inspect.
func (w *argumentWalk) expose(text, ref string) error {
	if text == "" {
		return nil
	}
	if !w.c.expand(w.src, text, ref) {
		return errArgumentLimit
	}
	w.c.addText(text, false)
	w.c.decode(w.src, text, 0, false)
	if w.c.addedExhausted != "" {
		return errArgumentLimit
	}
	return nil
}

// --- helpers ----------------------------------------------------------------------------

// decodeBlocks decodes a JSON array into []ContentBlock (each element round-trips through
// the connector's own UnmarshalJSON, so nested raw is preserved). Returns nil when the
// bytes are not a block array.
func (c *contentCollector) decodeBlocks(raw json.RawMessage) []ContentBlock {
	if len(raw) == 0 {
		return nil
	}
	var blocks []ContentBlock
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return nil
	}
	return blocks
}

// decodeJSONString reports whether raw is a JSON string and returns its value.
func decodeJSONString(raw json.RawMessage) (string, bool) {
	trimmed := strings.TrimSpace(string(raw))
	if len(trimmed) == 0 || trimmed[0] != '"' {
		return "", false
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", false
	}
	return s, true
}

// base64Source decodes an explicitly base64-labeled wire source before matching. Text
// media that decodes to printable UTF-8 is fully scannable. Binary media remains
// unscanned (we do not pretend to parse images/PDFs), but any printable decoded bytes are
// still included in the classifier input to catch a secret disguised with a binary type.
// Malformed and oversized blobs are unscanned and therefore denied by the stock policy.
func (c *contentCollector) base64Source(kind, role, data, mediaType, refPrefix string) {
	ref := refPrefix + mediaType
	if len(data) == 0 || len(data) > base64.StdEncoding.EncodedLen(maxDecodedContentSize) {
		c.opaque(kind, role, ref+"/oversized-or-empty")
		return
	}
	decoded, ok := decodeBase64(data)
	if !ok || len(decoded) > maxDecodedContentSize-c.compatDecoded {
		c.opaque(kind, role, ref+"/undecodable")
		return
	}
	c.compatDecoded += len(decoded)
	text := string(decoded)
	if textMediaType(mediaType) {
		c.scannable(kind, role, text, ref)
		return
	}
	// Printable bytes of binary media are classified as a decoding of this source, whatever
	// the declared type; the source keeps its opaque marker, since this parses no image.
	src := &source{kind: kind, role: role}
	c.addText(text, true)
	c.expand(src, text, ref)
	c.decode(src, text, 0, true)
	c.opaque(kind, role, ref)
}

// textVariants returns the distinct printable one-layer URL/base64 decodings of text, and
// whether an explicit encoding could not be decoded or exceeded the per-item guard. Base64
// candidates may be the entire text or a token embedded in prose; only sufficiently long
// candidates count, so ordinary short words are not treated as encodings. charge, when
// set, receives the size of each decoder input before that decoder runs; when it refuses,
// textVariants stops and reports ok false.
func textVariants(text string, charge func(int) bool) (variants []string, unsafe, ok bool) {
	var seen map[string]bool
	add := func(decoded string) {
		if decoded == "" || decoded == text || seen[decoded] || !printableText([]byte(decoded)) {
			return
		}
		if seen == nil {
			seen = map[string]bool{}
		}
		seen[decoded] = true
		variants = append(variants, decoded)
	}
	if hasPercentEscape(text) {
		switch {
		case len(text) > maxDecodedContentSize:
			unsafe = true
		case charge != nil && !charge(len(text)):
			return nil, false, false
		default:
			if decoded, err := url.QueryUnescape(text); err != nil {
				unsafe = true
			} else {
				add(decoded)
			}
		}
	}
	ok = true
	visitBase64Candidates(text, func(candidate string) bool {
		if len(candidate) > base64.StdEncoding.EncodedLen(maxDecodedContentSize) {
			unsafe = true
			return true
		}
		if charge != nil && !charge(len(candidate)) {
			ok = false
			return false
		}
		if decoded, isText := decodeBase64(candidate); isText {
			add(string(decoded))
		} else if strings.ContainsAny(candidate, "=+/_") {
			// Padded/alternate-alphabet candidates are explicit enough to treat a failed or
			// binary decode as opaque. Pure alphanumeric prose remains ordinary text.
			unsafe = true
		}
		return true
	})
	if !ok {
		return nil, false, false
	}
	return variants, unsafe, true
}

// visitBase64Candidates yields candidates without allocating a slice proportional
// to the input. The callback can stop the walk as soon as a charge is refused.
func visitBase64Candidates(text string, visit func(string) bool) {
	// The shortest key=value secret is four value bytes: token=s373 encodes to 16.
	const minBase64Candidate = 12
	start := -1
	flush := func(end int) bool {
		if start < 0 {
			return true
		}
		candidate := text[start:end]
		start = -1
		if len(candidate) >= minBase64Candidate && plausibleBase64Candidate(candidate) {
			return visit(candidate)
		}
		return true
	}
	for i, r := range text {
		if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') ||
			(r >= '0' && r <= '9') || r == '+' || r == '/' || r == '_' || r == '-' || r == '=' {
			if start < 0 {
				start = i
			}
			continue
		}
		if !flush(i) {
			return
		}
	}
	flush(len(text))
}

func plausibleBase64Candidate(s string) bool {
	if i := strings.IndexByte(s, '='); i >= 0 {
		padding := s[i:]
		if len(padding) > 2 || strings.Trim(padding, "=") != "" || len(s)%4 != 0 {
			return false
		}
	}
	return len(s)%4 != 1
}

func decodeBase64(s string) ([]byte, bool) {
	s = strings.TrimSpace(s)
	for _, enc := range []*base64.Encoding{
		base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding,
	} {
		decoded, err := enc.DecodeString(s)
		if err == nil && len(decoded) > 0 && printableText(decoded) {
			return decoded, true
		}
	}
	return nil, false
}

func printableText(b []byte) bool {
	if len(b) == 0 || !utf8.Valid(b) {
		return false
	}
	printable := 0
	total := 0
	for _, r := range string(b) {
		total++
		if unicode.IsPrint(r) || r == '\n' || r == '\r' || r == '\t' {
			printable++
		}
	}
	return total > 0 && printable*100/total >= 85
}

func textMediaType(mediaType string) bool {
	mt := strings.ToLower(strings.TrimSpace(strings.Split(mediaType, ";")[0]))
	return strings.HasPrefix(mt, "text/") || mt == "application/json" || mt == "application/xml" ||
		mt == "application/yaml" || mt == "application/x-yaml" || mt == "application/x-www-form-urlencoded"
}

func hasPercentEscape(s string) bool {
	for i := 0; i+2 < len(s); i++ {
		if s[i] == '%' && isHex(s[i+1]) && isHex(s[i+2]) {
			return true
		}
	}
	return false
}

func isHex(b byte) bool {
	return (b >= '0' && b <= '9') || (b >= 'a' && b <= 'f') || (b >= 'A' && b <= 'F')
}

// hostOf returns the HOST of a url (minimal data — never the path/query/fragment, which
// can carry sensitive tokens). A non-URL string yields a short, non-sensitive marker.
func hostOf(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "non-url"
	}
	return u.Hostname()
}

func firstNonEmptyStr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
