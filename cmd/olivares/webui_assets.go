// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"fmt"
	"mime"
	"net/http"
	"path"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

type spaAsset struct {
	data        []byte
	contentType string
	etag        string
	gzipOnce    sync.Once
	gzipData    []byte
	gzipETag    string
}

func newSPAAsset(name string, data []byte) *spaAsset {
	contentType := mime.TypeByExtension(path.Ext(name))
	if contentType == "" {
		contentType = http.DetectContentType(data)
	}
	return &spaAsset{data: data, contentType: contentType, etag: assetETag(data)}
}

func assetETag(data []byte) string { return fmt.Sprintf(`"%x"`, sha256.Sum256(data)) }

func (a *spaAsset) serve(w http.ResponseWriter, r *http.Request, name string) {
	setSecurityHeaders(w)
	w.Header().Add("Vary", "Accept-Encoding")
	encoding, acceptable := assetEncoding(strings.Join(r.Header.Values("Accept-Encoding"), ","))
	if !acceptable {
		w.Header().Set("Cache-Control", "no-store")
		http.Error(w, "no acceptable asset encoding", http.StatusNotAcceptable)
		return
	}
	data, etag := a.data, a.etag
	if encoding == "gzip" {
		a.gzipOnce.Do(func() {
			var b bytes.Buffer
			z := gzip.NewWriter(&b)
			// bytes.Buffer cannot fail a write; compression is derived from the
			// immutable source without modifying the embedded bundle or its stamp.
			_, _ = z.Write(a.data)
			_ = z.Close()
			a.gzipData = b.Bytes()
			a.gzipETag = assetETag(a.gzipData)
		})
		data, etag = a.gzipData, a.gzipETag
		w.Header().Set("Content-Encoding", "gzip")
		// A multipart body is not itself a gzip stream. Ignore multiple ranges
		// for gzip rather than label multipart framing as compressed content.
		if strings.Contains(r.Header.Get("Range"), ",") {
			r = r.Clone(r.Context())
			r.Header.Del("Range")
		}
	}
	w.Header().Set("Content-Type", a.contentType)
	w.Header().Set("ETag", etag)
	// RFC 9110 section 14.2 defines Range only for GET; HEAD describes the
	// whole selected representation while retaining request preconditions.
	if r.Method == http.MethodHead && r.Header.Get("Range") != "" {
		r = r.Clone(r.Context())
		r.Header.Del("Range")
	}
	// Ranges address bytes of the selected representation. ServeContent also
	// handles If-Range, If-Match and If-None-Match against its representation ETag.
	http.ServeContent(spaAssetResponseWriter{w, len(data)}, r, name, time.Time{}, bytes.NewReader(data))
}

// ServeContent can reject a precondition before clearing representation headers.
// Apply length and cache policy only once its final response status is known.
type spaAssetResponseWriter struct {
	http.ResponseWriter
	length int
}

func (w spaAssetResponseWriter) WriteHeader(status int) {
	switch status {
	case http.StatusOK:
		// ServeContent omits the full length when Content-Encoding is set. Our
		// bytes are already encoded, so the length is known (including HEAD).
		w.Header().Set("Content-Length", strconv.Itoa(w.length))
		fallthrough
	case http.StatusPartialContent, http.StatusNotModified:
		// Preserve ServeContent's range length and bodyless 304 metadata.
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	default:
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Del("Content-Length")
		w.Header().Del("Content-Encoding")
		w.Header().Del("ETag")
	}
	w.ResponseWriter.WriteHeader(status)
}

var assetQuality = regexp.MustCompile(`^(0(\.[0-9]{0,3})?|1(\.0{0,3})?)$`)

// assetEncoding follows RFC 9110 section 12.5.3. Without a preference, keep
// identity; otherwise prefer gzip unless an explicit identity weight is higher.
func assetEncoding(header string) (string, bool) {
	quality := map[string]float64{}
	for item := range strings.SplitSeq(header, ",") {
		parts := strings.Split(item, ";")
		coding := strings.ToLower(strings.TrimSpace(parts[0]))
		if coding != "gzip" && coding != "identity" && coding != "*" {
			continue
		}
		q := 1.0
		if len(parts) > 1 {
			key, value, found := strings.Cut(strings.TrimSpace(parts[1]), "=")
			value = strings.TrimSpace(value)
			if len(parts) != 2 || !found || !strings.EqualFold(strings.TrimSpace(key), "q") || !assetQuality.MatchString(value) {
				q = 0
			} else {
				q, _ = strconv.ParseFloat(value, 64)
			}
		}
		// Conflicting duplicate entries retain the stricter preference.
		if previous, exists := quality[coding]; !exists || q < previous {
			quality[coding] = q
		}
	}
	gzipQ, gzipExplicit := quality["gzip"]
	if !gzipExplicit {
		gzipQ = quality["*"]
	}
	identityQ, identityExplicit := quality["identity"]
	if !identityExplicit {
		identityQ = 1
		if wildcard, exists := quality["*"]; exists && wildcard == 0 {
			identityQ = 0
		}
	}
	if gzipQ > 0 && (!identityExplicit || gzipQ >= identityQ) {
		return "gzip", true
	}
	return "", identityQ > 0
}
