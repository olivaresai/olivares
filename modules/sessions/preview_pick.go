// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"io"
	"mime"
	"net/http"
	"strings"
)

// previewPick is the one script the preview adds to a page. The page runs in an
// opaque origin, so the console cannot read its elements; this script, running in
// that same origin and so with no more power than the page's own, tells the console
// which element the person clicked. It is idle until the console asks (message
// olivares-annotate from the parent), then outlines the element under the pointer
// and, on the click that picks one, answers olivares-element with a selector, the
// element's text and the page's path (the engine's, token included: the console keeps
// only what follows the token). The click goes to the script, not to the page.
const previewPick = `<script>(function () {
  var on = false, hot = null, was = '';
  function selector(start) {
    var out = [];
    for (var el = start; el && el.nodeType === 1 && el !== document.documentElement && out.length < 4; el = el.parentElement) {
      var part = el.localName;
      if (el.id) { out.unshift(part + '#' + CSS.escape(el.id)); break; }
      var same = el.parentElement ? [].filter.call(el.parentElement.children, function (c) { return c.localName === part; }) : [];
      if (same.length > 1) part += ':nth-of-type(' + (same.indexOf(el) + 1) + ')';
      out.unshift(part);
    }
    return out.join(' > ') || start.localName;
  }
  function unmark() { if (hot) { hot.style.outline = was; hot = null; } }
  addEventListener('message', function (e) {
    if (e.source !== parent || !e.data || e.data.type !== 'olivares-annotate') return;
    on = !!e.data.on;
    if (!on) unmark();
  });
  addEventListener('mouseover', function (e) {
    if (!on) return;
    unmark();
    hot = e.target;
    was = hot.style.outline;
    hot.style.outline = '2px solid #2563eb';
  }, true);
  addEventListener('click', function (e) {
    if (!on) return;
    e.preventDefault();
    e.stopPropagation();
    var el = e.target;
    unmark();
    on = false;
    var text = (el.innerText || el.value || el.textContent || '').replace(/\s+/g, ' ').trim().slice(0, 80);
    parent.postMessage({ type: 'olivares-element', selector: selector(el), text: text, path: location.pathname }, '*');
  }, true);
})();</script>`

// addPicker appends previewPick to an HTML answer. The answer grows, so its length
// is dropped; an encoded answer stays whole (the proxy asks the app for none).
func addPicker(resp *http.Response) {
	switch {
	case resp.Request != nil && resp.Request.Method == http.MethodHead,
		resp.StatusCode == http.StatusNoContent, resp.StatusCode == http.StatusNotModified,
		resp.StatusCode == http.StatusPartialContent,
		resp.Body == nil, resp.Body == http.NoBody,
		resp.Header.Get("Content-Encoding") != "":
		return
	}
	if kind, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type")); kind != "text/html" {
		return
	}
	resp.Body = struct {
		io.Reader
		io.Closer
	}{io.MultiReader(resp.Body, strings.NewReader(previewPick)), resp.Body}
	resp.ContentLength = -1
	resp.Header.Del("Content-Length")
}
