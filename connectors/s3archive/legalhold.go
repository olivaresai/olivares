// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package s3archive

import (
	"bytes"
	"context"
	"crypto/md5" // #nosec G501 -- S3 requires Content-MD5 on the legal-hold PUT (integrity tag, not a security primitive)
	"encoding/base64"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/olivaresai/olivares/connectors/internal/awssig"
	"github.com/olivaresai/olivares/connectors/internal/delivery"
)

// This file adds the POST-HOC object-lock operations a long-horizon legal-hold
// orchestrator needs over an EXISTING archive: enumerate the object versions
// under a prefix, and set/clear the S3 Object Lock LEGAL HOLD on a specific version.
// The write path (Put) already places a hold AT WRITE TIME; these let a matter protect
// segments sealed BEFORE the matter arose, and lift the hold (under the orchestrator's
// OWN dual control) once the matter closes. Both reuse the connector's SigV4 signing,
// fail-closed delivery and secret discipline; neither imports the engine. The
// composition root adapts these onto core/audit's optional ArchiveLister / LegalHoldSetter
// capabilities (the connector never imports /core).

// ObjectVersion is one S3 object version under a prefix — the minimal fields a segment
// enumerator needs. It carries no content.
type ObjectVersion struct {
	Key       string
	VersionID string
	IsLatest  bool
}

// SetObjectLegalHold sets (on=true) or clears (on=false) the S3 Object Lock legal hold
// on one object version via the ?legal-hold subresource. A legal hold is independent of
// the retention mode/date — it preserves the version indefinitely until cleared, and is
// settable even under COMPLIANCE-mode retention (it never shortens retention). With
// verify_lock on, it GETs the version's legal-hold status and confirms it before
// returning (fail-closed: an unconfirmed change is an error, not a receipt). key is the
// object key WITHOUT the connector prefix (the prefix is applied here, mirroring Put).
func (o *Output) SetObjectLegalHold(ctx context.Context, key, versionID string, on bool) (Receipt, error) {
	if !o.opened {
		return Receipt{}, fmt.Errorf("s3archive: connector not opened")
	}
	key = strings.TrimPrefix(key, "/")
	if key == "" {
		return Receipt{}, fmt.Errorf("s3archive: object key is required")
	}
	fullKey := o.prefix + key
	status := "OFF"
	if on {
		status = "ON"
	}
	body := []byte(`<LegalHold xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><Status>` + status + `</Status></LegalHold>`)
	md5sum := md5.Sum(body) // #nosec G401 -- Content-MD5 is mandatory on this PUT (integrity, not confidentiality)
	hdr := map[string]string{
		"Content-Type":   "application/xml",
		"Content-MD5":    base64.StdEncoding.EncodeToString(md5sum[:]),
		hdrContentSHA256: awssig.HexSHA256(body),
	}
	req, err := o.signedRequest(http.MethodPut, o.legalHoldURL(fullKey, versionID), body, hdr)
	if err != nil {
		return Receipt{}, err
	}
	capture := &captureDoer{doer: o.transport()}
	if _, err := delivery.New(capture, o.deliveryOptions()).Send(ctx, req); err != nil {
		return Receipt{}, fmt.Errorf("s3archive: set legal-hold %s: %w", fullKey, err)
	}
	rec := Receipt{Bucket: o.bucket, Key: fullKey, VersionID: versionID}
	if !o.verifyLock {
		return rec, nil
	}
	got, err := o.getObjectLegalHold(ctx, fullKey, versionID)
	if err != nil {
		return Receipt{}, err
	}
	if got != on {
		return Receipt{}, fmt.Errorf("s3archive: legal-hold not confirmed for %s: got ON=%v, want ON=%v", fullKey, got, on)
	}
	rec.LockVerified = true
	return rec, nil
}

// getObjectLegalHold GETs the ?legal-hold subresource and reports whether the hold is ON.
func (o *Output) getObjectLegalHold(ctx context.Context, fullKey, versionID string) (bool, error) {
	req, err := o.signedRequest(http.MethodGet, o.legalHoldURL(fullKey, versionID), nil,
		map[string]string{hdrContentSHA256: awssig.HexSHA256(nil)})
	if err != nil {
		return false, err
	}
	capture := &bodyCaptureDoer{doer: o.transport()}
	if _, err := delivery.New(capture, o.deliveryOptions()).Send(ctx, req); err != nil {
		return false, fmt.Errorf("s3archive: get legal-hold %s: %w", fullKey, err)
	}
	var lh struct {
		Status string `xml:"Status"`
	}
	if err := xml.Unmarshal(capture.body, &lh); err != nil {
		return false, fmt.Errorf("s3archive: get legal-hold %s: parse response: %w", fullKey, err)
	}
	return strings.EqualFold(strings.TrimSpace(lh.Status), "ON"), nil
}

// ListObjectVersions enumerates every object version whose key starts with prefix
// (under the connector's own configured prefix), following pagination to completion.
// The caller filters keys to the segment grammar it owns (core/audit.ParseSegmentKey).
func (o *Output) ListObjectVersions(ctx context.Context, prefix string) ([]ObjectVersion, error) {
	if !o.opened {
		return nil, fmt.Errorf("s3archive: connector not opened")
	}
	fullPrefix := o.prefix + strings.TrimPrefix(prefix, "/")
	var out []ObjectVersion
	keyMarker, versionMarker := "", ""
	for {
		q := "versions=&prefix=" + awssig.URIEncode(fullPrefix, true)
		if keyMarker != "" {
			q += "&key-marker=" + awssig.URIEncode(keyMarker, true)
		}
		if versionMarker != "" {
			q += "&version-id-marker=" + awssig.URIEncode(versionMarker, true)
		}
		req, err := o.signedRequest(http.MethodGet, o.bucketURL()+"?"+q, nil,
			map[string]string{hdrContentSHA256: awssig.HexSHA256(nil)})
		if err != nil {
			return nil, err
		}
		capture := &bodyCaptureDoer{doer: o.transport()}
		if _, err := delivery.New(capture, o.deliveryOptions()).Send(ctx, req); err != nil {
			return nil, fmt.Errorf("s3archive: list versions %s: %w", fullPrefix, err)
		}
		var lv listVersionsResult
		if err := xml.Unmarshal(capture.body, &lv); err != nil {
			return nil, fmt.Errorf("s3archive: list versions %s: parse response: %w", fullPrefix, err)
		}
		for _, v := range lv.Versions {
			// S3 echoes the FULL key including the connector prefix; return CONNECTOR-RELATIVE
			// keys so they round-trip through ParseSegmentKey and SetObjectLegalHold (which
			// re-adds o.prefix). Without this, a non-empty prefix makes the segment-key parse
			// see the prefix as part of the tenant and silently drop every segment.
			out = append(out, ObjectVersion{Key: strings.TrimPrefix(v.Key, o.prefix), VersionID: v.VersionID, IsLatest: v.IsLatest})
		}
		if !lv.IsTruncated {
			break
		}
		keyMarker, versionMarker = lv.NextKeyMarker, lv.NextVersionIDMarker
		if keyMarker == "" && versionMarker == "" {
			break // defensive: truncated but no marker — stop rather than loop forever
		}
	}
	return out, nil
}

// listVersionsResult is the S3 ListObjectVersions XML projection (local-name matched, so
// the response namespace is irrelevant). Only the fields the enumerator needs are bound.
type listVersionsResult struct {
	IsTruncated         bool   `xml:"IsTruncated"`
	NextKeyMarker       string `xml:"NextKeyMarker"`
	NextVersionIDMarker string `xml:"NextVersionIdMarker"`
	Versions            []struct {
		Key       string `xml:"Key"`
		VersionID string `xml:"VersionId"`
		IsLatest  bool   `xml:"IsLatest"`
	} `xml:"Version"`
}

// legalHoldURL builds the ?legal-hold subresource URL for one object version. The
// subresource is written as "legal-hold=" so the wire query and the SigV4 canonical
// query agree byte-for-byte (sign.go's invariant); S3 sorts the params on its side.
func (o *Output) legalHoldURL(fullKey, versionID string) string {
	u := o.objectURL(fullKey) + "?legal-hold="
	if versionID != "" {
		u += "&versionId=" + awssig.URIEncode(versionID, true)
	}
	return u
}

// bucketURL returns the request URL for the bucket itself (no key): virtual-host
// addressing on AWS (empty endpoint), path-style on a custom endpoint (MinIO et al).
func (o *Output) bucketURL() string {
	if o.endpoint == "" {
		return "https://" + o.bucket + ".s3." + o.region + ".amazonaws.com/"
	}
	return o.endpoint + "/" + o.bucket + "/"
}

// bodyCaptureDoer captures the FULL response body (and headers) of the final response,
// re-wrapping res.Body so delivery still reads it for its own diagnostics/retry path.
// (captureDoer keeps only headers; delivery's 2 KiB body excerpt is too small for a
// versions page or a legal-hold document.)
type bodyCaptureDoer struct {
	doer delivery.Doer
	hdr  http.Header
	body []byte
}

func (c *bodyCaptureDoer) Do(req *http.Request) (*http.Response, error) {
	res, err := c.doer.Do(req)
	if err != nil || res == nil || res.Body == nil {
		return res, err
	}
	buf, rerr := io.ReadAll(res.Body)
	_ = res.Body.Close()
	if rerr != nil {
		return res, rerr
	}
	c.hdr = res.Header.Clone()
	c.body = buf
	res.Body = io.NopCloser(bytes.NewReader(buf))
	return res, nil
}
