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
// cover unscanned) closes the bypass. This collection runs in BOTH builds (it is core,
// AGPL); the closed firewall's deep detectors are the additive paid layer above it.
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

package claudeapi

import (
	"encoding/base64"
	"encoding/json"
	"net/url"
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
// tool_result content array). Anthropic's shapes nest at most a couple of levels; this is
// a hostile-input backstop, not a real limit — content below it is marked unscanned, never
// silently dropped.
const (
	maxContentDepth       = 6
	maxDecodedContentSize = 1 << 20 // 1 MiB total decoded plaintext per request/response
)

// ContentChannel is one extracted unit of message/response content. Text is the classifiable
// plaintext ("" when the channel is opaque); Scannable=false means it contributed to
// Unscanned (binary/file_id/encrypted/opaque/unknown). Ref is non-sensitive structural
// context (media_type, url host, file_id, tool name, block type) — never the content bytes.
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
type CollectedContent struct {
	Channels  []ContentChannel
	Texts     []string
	Unscanned bool
}

// CollectRequestContent walks a request's system prefix and message content. System blocks
// are attributed role "system"; message blocks carry their message role.
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
func CollectResponseContent(resp MessageResponse) CollectedContent {
	c := &contentCollector{}
	c.blocks(roleAssistant, resp.Content, 0, "")
	return c.result()
}

// --- collector --------------------------------------------------------------------------

type contentCollector struct {
	channels  []ContentChannel
	texts     []string
	seenText  map[string]bool
	unscanned bool
	decoded   int
}

func (c *contentCollector) result() CollectedContent {
	return CollectedContent{Channels: c.channels, Texts: c.texts, Unscanned: c.unscanned}
}

// scannable records an extractable-text channel (and de-duplicates the text for the
// classifier). Empty text is ignored (nothing to classify), but the channel is still
// recorded so the firewall can see structure.
func (c *contentCollector) scannable(kind, role, text, ref string) {
	c.channels = append(c.channels, ContentChannel{Kind: kind, Role: role, Text: text, Scannable: true, Ref: ref})
	c.addTextAndDecoded(kind, role, text, 0)
}

func (c *contentCollector) addText(text string) bool {
	t := strings.TrimSpace(text)
	if t == "" {
		return false
	}
	if c.seenText == nil {
		c.seenText = map[string]bool{}
	}
	if c.seenText[t] {
		return false
	}
	c.seenText[t] = true
	c.texts = append(c.texts, text)
	return true
}

// addTextAndDecoded records the original text and recursively adds bounded URL/base64
// decodings for the sensitivity classifier. Decoded values remain in-memory only. The
// same depth-six cap as nested content plus a one-MiB aggregate cap prevents decode bombs;
// content that would exceed either bound becomes unscanned so the effective policy denies
// it by default.
func (c *contentCollector) addTextAndDecoded(kind, role, text string, depth int) {
	c.addText(text)
	variants, unsafe := decodedTextVariants(text)
	if unsafe {
		c.opaque(kind, role, "encoded/undecodable-or-oversized")
	}
	if len(variants) == 0 {
		return
	}
	if depth >= maxContentDepth {
		c.opaque(kind, role, "encoded/max-depth")
		return
	}
	for _, decoded := range variants {
		if len(decoded) > maxDecodedContentSize-c.decoded {
			c.opaque(kind, role, "encoded/oversized")
			return
		}
		if !c.addText(decoded) {
			continue
		}
		c.decoded += len(decoded)
		c.addTextAndDecoded(kind, role, decoded, depth+1)
	}
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
		c.toolUse(ChannelToolUse, role, b.raw)
	case "server_tool_use":
		c.toolUse(ChannelServerToolUse, role, b.raw)
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
		c.addTextAndDecoded(ChannelDocument, role, d.Source.URL, 0)
		c.opaque(ChannelDocument, role, "document/url:"+hostOf(d.Source.URL))
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
		c.addTextAndDecoded(ChannelImage, role, im.Source.URL, 0)
		c.opaque(ChannelImage, role, "image/url:"+hostOf(im.Source.URL))
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
// The unsafe-action detector reads the action args from here.
func (c *contentCollector) toolUse(kind, role string, raw json.RawMessage) {
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
		sb.Write(tu.Input) // compact JSON of the input args (in-flight; never persisted)
	}
	c.scannable(kind, role, sb.String(), kind+":"+tu.Name)
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
	if !ok || len(decoded) > maxDecodedContentSize-c.decoded || !printableText(decoded) {
		c.opaque(kind, role, ref+"/undecodable")
		return
	}
	c.decoded += len(decoded)
	text := string(decoded)
	if textMediaType(mediaType) {
		c.scannable(kind, role, text, ref)
		return
	}
	c.addTextAndDecoded(kind, role, text, 0)
	c.opaque(kind, role, ref)
}

// decodedTextVariants returns distinct, printable one-layer decodings of a plaintext
// field. URL decoding covers percent/form encoding. Base64 candidates may be the entire
// field or a token embedded in prose; only sufficiently long candidates are considered
// to avoid treating ordinary short words as encodings.
func decodedTextVariants(text string) ([]string, bool) {
	seen := map[string]bool{}
	var out []string
	unsafe := false
	add := func(s string) {
		if s == "" || s == text || seen[s] || !printableText([]byte(s)) {
			return
		}
		seen[s] = true
		out = append(out, s)
	}
	if hasPercentEscape(text) {
		if len(text) > maxDecodedContentSize {
			unsafe = true
		} else if decoded, err := url.QueryUnescape(text); err == nil {
			add(decoded)
		} else {
			unsafe = true
		}
	}
	for _, candidate := range base64Candidates(text) {
		if len(candidate) > base64.StdEncoding.EncodedLen(maxDecodedContentSize) {
			unsafe = true
			continue
		}
		if decoded, ok := decodeBase64(candidate); ok {
			add(string(decoded))
		} else if strings.ContainsAny(candidate, "=+/_") {
			// Padded/alternate-alphabet candidates are explicit enough to treat a failed or
			// binary decode as opaque. Pure alphanumeric prose remains ordinary text.
			unsafe = true
		}
	}
	return out, unsafe
}

func base64Candidates(text string) []string {
	// The shortest secret recognized by the key=value rule is four value bytes;
	// encoding e.g. "token=s373" is only 16 characters. Keep the threshold below
	// that attack while requiring enough bytes to avoid ordinary short words.
	const minBase64Candidate = 12
	var out []string
	start := -1
	flush := func(end int) {
		if start < 0 {
			return
		}
		candidate := text[start:end]
		start = -1
		if len(candidate) >= minBase64Candidate && plausibleBase64Candidate(candidate) {
			out = append(out, candidate)
		}
	}
	for i, r := range text {
		if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') ||
			(r >= '0' && r <= '9') || r == '+' || r == '/' || r == '_' || r == '-' || r == '=' {
			if start < 0 {
				start = i
			}
			continue
		}
		flush(i)
	}
	flush(len(text))
	return out
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
