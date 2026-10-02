// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
)

func TestSPA_AssetGzip(t *testing.T) {
	body := []byte(strings.Repeat("console.log('immutable');\n", 100))
	h := newSPAHandler(http.NotFoundHandler(), fstest.MapFS{
		"index.html":         {Data: []byte("index")},
		"assets/app-hash.js": {Data: body},
	})
	r := httptest.NewRequest(http.MethodGet, "/assets/app-hash.js", nil)
	r.Header.Set("Accept-Encoding", "gzip")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK || w.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("status=%d encoding=%q", w.Code, w.Header().Get("Content-Encoding"))
	}
	z, err := gzip.NewReader(bytes.NewReader(w.Body.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	defer z.Close()
	decoded, err := io.ReadAll(z)
	if err != nil || !bytes.Equal(decoded, body) {
		t.Fatalf("decoded bytes differ: %v", err)
	}
	if w.Header().Get("Content-Length") != strconv.Itoa(w.Body.Len()) {
		t.Fatal("wrong encoded length")
	}
	if !strings.Contains(w.Header().Get("Content-Type"), "javascript") {
		t.Fatal("lost JavaScript content type")
	}
	if w.Header().Get("Vary") != "Accept-Encoding" {
		t.Fatal("missing negotiation Vary")
	}
	if !strings.Contains(w.Header().Get("Cache-Control"), "immutable") {
		t.Fatal("lost immutable caching")
	}
}

func TestSPA_AssetEncodingNegotiation(t *testing.T) {
	h := newSPAHandler(sentinelAPI(), testWebFS())
	for _, tc := range []struct {
		header, encoding string
		status           int
	}{
		{"", "", 200},
		{"gzip", "gzip", 200},
		{"GZip ; q=1.000", "gzip", 200},
		{"gzip;q=0", "", 200},
		{"gzip;q=0, *;q=1", "", 200},
		{"identity;q=0, gzip;q=0.2", "gzip", 200},
		{"identity;q=0", "", 406},
		{"*", "gzip", 200},
		{"*;q=0", "", 406},
		{"*;q=0, identity;q=1", "", 200},
		{"*;q=0, gzip;q=1", "gzip", 200},
		{"br", "", 200},
		{"br, identity;q=0", "", 406},
		{"gzip;q=0.3, identity;q=0.8", "", 200},
		{"identity;q=0.3, gzip;q=0.8", "gzip", 200},
		{"gzip;q=0.5, identity;q=0.5", "gzip", 200},
		{"gzip;q=NaN", "", 200},
		{"gzip;q=1.001", "", 200},
		{"gzip;q=-1", "", 200},
		{"gzip;q=0.1234", "", 200},
		{"gzip;q=bogus, identity;q=0", "", 406},
		{"gzip;q=1, gzip;q=0", "", 200},
	} {
		t.Run(tc.header, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/assets/app-abc123.js", nil)
			r.Header.Set("Accept-Encoding", tc.header)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tc.status || w.Header().Get("Content-Encoding") != tc.encoding {
				t.Fatalf("status=%d encoding=%q", w.Code, w.Header().Get("Content-Encoding"))
			}
			if w.Header().Get("Vary") != "Accept-Encoding" {
				t.Fatal("missing Vary")
			}
			if w.Code == 406 && w.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("negotiation error is cacheable")
			}
		})
	}
	// Multiple field lines form one preference list.
	r := httptest.NewRequest(http.MethodGet, "/assets/app-abc123.js", nil)
	r.Header.Add("Accept-Encoding", "identity;q=0")
	r.Header.Add("Accept-Encoding", "gzip")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Header().Get("Content-Encoding") != "gzip" {
		t.Fatal("ignored repeated header")
	}
}

func TestSPA_AssetValidatorsHeadAndRanges(t *testing.T) {
	h := newSPAHandler(sentinelAPI(), testWebFS())
	request := func(method, encoding string, headers map[string]string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "/assets/app-abc123.js", nil)
		r.Header.Set("Accept-Encoding", encoding)
		for key, value := range headers {
			r.Header.Set(key, value)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	identity := request("GET", "identity", nil)
	compressed := request("GET", "gzip", nil)
	if identity.Header().Get("ETag") == compressed.Header().Get("ETag") || identity.Header().Get("ETag") == "" {
		t.Fatal("representations need distinct validators")
	}
	for _, encoding := range []string{"identity", "gzip"} {
		full := request("GET", encoding, nil)
		etag := full.Header().Get("ETag")
		head := request("HEAD", encoding, nil)
		if head.Body.Len() != 0 || head.Header().Get("Content-Length") != full.Header().Get("Content-Length") || head.Header().Get("ETag") != etag {
			t.Fatal("HEAD representation differs from GET")
		}
		if head.Header().Get("Content-Type") != full.Header().Get("Content-Type") || head.Header().Get("Content-Encoding") != full.Header().Get("Content-Encoding") {
			t.Fatal("HEAD metadata differs from GET")
		}
		notModified := request("GET", encoding, map[string]string{"If-None-Match": "W/" + etag})
		if notModified.Code != 304 || notModified.Body.Len() != 0 || notModified.Header().Get("Content-Length") != "" || notModified.Header().Get("ETag") != etag || notModified.Header().Get("Vary") != "Accept-Encoding" || !strings.Contains(notModified.Header().Get("Cache-Control"), "immutable") {
			t.Fatal("conditional response lost representation metadata")
		}
		ranged := request("GET", encoding, map[string]string{"Range": "bytes=0-3", "If-Range": etag})
		if ranged.Code != 206 || !bytes.Equal(ranged.Body.Bytes(), full.Body.Bytes()[:4]) || ranged.Header().Get("Content-Length") != "4" || ranged.Header().Get("Content-Range") != "bytes 0-3/"+strconv.Itoa(full.Body.Len()) {
			t.Fatal("range does not address selected representation")
		}
		for _, representation := range []*httptest.ResponseRecorder{full, head, ranged} {
			if !strings.Contains(representation.Header().Get("Cache-Control"), "immutable") {
				t.Fatal("successful representation lost immutable caching")
			}
		}
		mismatch := request("GET", encoding, map[string]string{"Range": "bytes=0-3", "If-Range": `"other"`})
		if mismatch.Code != 200 || !bytes.Equal(mismatch.Body.Bytes(), full.Body.Bytes()) {
			t.Fatal("If-Range mismatch must send whole representation")
		}
		unsatisfied := request("GET", encoding, map[string]string{"Range": "bytes=999999-"})
		if unsatisfied.Code != 416 || unsatisfied.Header().Get("Content-Encoding") != "" {
			t.Fatal("invalid range mislabeled as encoded asset")
		}
	}
	otherValidator := request("GET", "gzip", map[string]string{"If-None-Match": identity.Header().Get("ETag")})
	if otherValidator.Code != 200 {
		t.Fatal("identity validator matched compressed representation")
	}
	precondition := request("GET", "gzip", map[string]string{"If-Match": identity.Header().Get("ETag")})
	if precondition.Code != 412 {
		t.Fatal("If-Match accepted a different representation")
	}
	multiple := request("GET", "gzip", map[string]string{"Range": "bytes=0-2,5-7"})
	if multiple.Code != 200 || !bytes.Equal(multiple.Body.Bytes(), compressed.Body.Bytes()) {
		t.Fatal("gzip multipart range must fall back to whole stream")
	}
	identityMultiple := request("GET", "identity", map[string]string{"Range": "bytes=0-2,5-7"})
	if identityMultiple.Code != 206 || !strings.HasPrefix(identityMultiple.Header().Get("Content-Type"), "multipart/byteranges") {
		t.Fatal("identity multipart ranges changed")
	}
}

// SR2's normative oracle: RFC 9110 section 14.2 defines Range only for GET.
func TestSPA_AssetHeadRangeUsesWholeRepresentationMetadata(t *testing.T) {
	h := newSPAHandler(http.NotFoundHandler(), fstest.MapFS{
		"index.html":       {Data: []byte("index")},
		"assets/a-hash.js": {Data: []byte(strings.Repeat("let x = 'UTF8-é';\n", 100))},
	})
	for _, tc := range []struct {
		encoding string
		length   int
	}{{"identity", 1900}, {"gzip", 60}} {
		t.Run(tc.encoding, func(t *testing.T) {
			request := func(method, rangeHeader string, headers map[string]string) *httptest.ResponseRecorder {
				r := httptest.NewRequest(method, "/assets/a-hash.js", nil)
				r.Header.Set("Accept-Encoding", tc.encoding)
				r.Header.Set("Range", rangeHeader)
				for key, value := range headers {
					r.Header.Set(key, value)
				}
				w := httptest.NewRecorder()
				h.ServeHTTP(w, r)
				return w
			}
			full := request("GET", "", nil)
			if full.Code != 200 || full.Body.Len() != tc.length {
				t.Fatalf("unexpected full representation: status=%d length=%d", full.Code, full.Body.Len())
			}
			for _, rangeHeader := range []string{"bytes=0-3", "bytes=999999-", "invalid", "bytes=0-2,5-7"} {
				head := request("HEAD", rangeHeader, nil)
				if head.Code != 200 || head.Body.Len() != 0 || head.Header().Get("Content-Length") != strconv.Itoa(tc.length) || head.Header().Get("Content-Range") != "" {
					t.Errorf("HEAD Range %q: status=%d length=%q range=%q body=%d", rangeHeader, head.Code, head.Header().Get("Content-Length"), head.Header().Get("Content-Range"), head.Body.Len())
				}
				for _, header := range []string{"ETag", "Cache-Control", "Content-Type", "Content-Encoding", "Vary"} {
					if head.Header().Get(header) != full.Header().Get(header) {
						t.Errorf("HEAD Range %q changed %s", rangeHeader, header)
					}
				}
			}
			// Ignoring Range must retain the independent request preconditions.
			notModified := request("HEAD", "bytes=0-3", map[string]string{"If-None-Match": full.Header().Get("ETag")})
			if notModified.Code != 304 || notModified.Body.Len() != 0 || notModified.Header().Get("Content-Length") != "" || notModified.Header().Get("ETag") != full.Header().Get("ETag") {
				t.Fatal("HEAD Range bypassed If-None-Match")
			}
			failed := request("HEAD", "bytes=0-3", map[string]string{"If-Match": `"other"`})
			if failed.Code != 412 || failed.Body.Len() != 0 || failed.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("HEAD Range bypassed If-Match")
			}
		})
	}
}

// SR2's independent oracle: a failed precondition has no asset body, so it
// cannot promise the selected asset's length or inherit immutable caching.
func TestSPA_AssetFailedPreconditionFramingAndCache(t *testing.T) {
	h := newSPAHandler(http.NotFoundHandler(), fstest.MapFS{
		"index.html":       {Data: []byte("index")},
		"assets/a-hash.js": {Data: []byte(strings.Repeat("let x = 'UTF8-é';\n", 100))},
	})
	for _, encoding := range []string{"gzip", "identity"} {
		t.Run(encoding, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/assets/a-hash.js", nil)
			r.Header.Set("Accept-Encoding", encoding)
			r.Header.Set("If-Match", `"not-this-representation"`)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != 412 {
				t.Fatalf("wanted 412, got %d", w.Code)
			}
			if length := w.Header().Get("Content-Length"); length != "" && length != strconv.Itoa(w.Body.Len()) {
				t.Errorf("412 promises absent body: Content-Length=%s actual=%d", length, w.Body.Len())
			}
			if strings.Contains(w.Header().Get("Cache-Control"), "immutable") {
				t.Errorf("precondition error gets immutable policy: %s", w.Header().Get("Cache-Control"))
			}
		})
	}
}

func TestSPA_AssetErrorFramingAndCache(t *testing.T) {
	server := httptest.NewServer(newSPAHandler(sentinelAPI(), testWebFS()))
	defer server.Close()
	transport := &http.Transport{DisableCompression: true}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport}
	for _, encoding := range []string{"identity", "gzip"} {
		for _, tc := range []struct {
			name, method, header, value string
			status                      int
		}{
			{"failed_match", "GET", "If-Match", `"other"`, 412},
			{"failed_head_match", "HEAD", "If-Match", `"other"`, 412},
			{"unsatisfied_range", "GET", "Range", "bytes=999999-", 416},
			{"malformed_range", "GET", "Range", "invalid", 416},
			{"unacceptable_encoding", "GET", "Accept-Encoding", "gzip;q=0, identity;q=0", 406},
		} {
			t.Run(encoding+"/"+tc.name, func(t *testing.T) {
				r, err := http.NewRequest(tc.method, server.URL+"/assets/app-abc123.js", nil)
				if err != nil {
					t.Fatal(err)
				}
				r.Header.Set("Accept-Encoding", encoding)
				r.Header.Set(tc.header, tc.value)
				response, err := client.Do(r)
				if err != nil {
					t.Fatal(err)
				}
				defer response.Body.Close()
				body, err := io.ReadAll(response.Body)
				// HEAD may omit Content-Length; any declared length on this
				// bodyless 412 must still describe the actual empty response.
				if err != nil || (response.Header.Get("Content-Length") != "" && response.ContentLength != int64(len(body))) {
					t.Errorf("error body framing: declared=%d actual=%d read=%v", response.ContentLength, len(body), err)
				}
				if response.StatusCode != tc.status || response.Header.Get("Cache-Control") != "no-store" {
					t.Errorf("status=%d cache=%q", response.StatusCode, response.Header.Get("Cache-Control"))
				}
				if response.Header.Get("Content-Encoding") != "" || response.Header.Get("ETag") != "" || response.Header.Get("Vary") != "Accept-Encoding" {
					t.Errorf("error inherited representation metadata: %v", response.Header)
				}
			})
		}
	}
}

func TestSPA_AssetCompressionDoesNotChangeDocumentOrAPI(t *testing.T) {
	h := newSPAHandler(sentinelAPI(), testWebFS())
	for _, target := range []string{"/", "/index.html", "/login"} {
		r := httptest.NewRequest("GET", target, nil)
		r.Header.Set("Accept-Encoding", "gzip")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		nonce := cspNonce(w.Header().Get("Content-Security-Policy"))
		if w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("Content-Encoding") != "" || nonce == "" || !strings.Contains(w.Body.String(), `nonce="`+nonce+`"`) || strings.Contains(w.Body.String(), "__CSP_NONCE__") {
			t.Fatalf("document contract changed at %s", target)
		}
	}
	if w := do(h, "GET", "/v1/server-info"); w.Header().Get("X-Handler") != "api" {
		t.Fatal("API did not reach admission")
	}
	if w := do(h, "GET", "/favicon.svg"); w.Header().Get("Cache-Control") != "no-cache" || w.Header().Get("Vary") != "" {
		t.Fatal("non-immutable asset policy changed")
	}
}

func TestSPA_AssetConcurrentNegotiation(t *testing.T) {
	h := newSPAHandler(sentinelAPI(), testWebFS())
	var group sync.WaitGroup
	for n := range 20 {
		group.Go(func() {
			encoding := "gzip"
			if n%2 == 0 {
				encoding = "identity"
			}
			r := httptest.NewRequest("GET", "/assets/app-abc123.js", nil)
			r.Header.Set("Accept-Encoding", encoding)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			data := w.Body.Bytes()
			if encoding == "gzip" {
				z, err := gzip.NewReader(bytes.NewReader(data))
				if err != nil {
					t.Error(err)
					return
				}
				data, err = io.ReadAll(z)
				_ = z.Close()
				if err != nil {
					t.Error(err)
					return
				}
			}
			if string(data) != `console.log('app')` {
				t.Error("concurrent response corrupted")
			}
		})
	}
	group.Wait()
}
