// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package s3content

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/contentsource"
	"github.com/olivaresai/olivares/connectors/internal/awssig"
	"github.com/olivaresai/olivares/connectors/internal/content"
	"github.com/olivaresai/olivares/sdk"
)

var _ contentsource.LiveSource = (*Source)(nil)

const (
	defaultRegion  = "us-east-1"
	defaultTimeout = 30 * time.Second

	envAccessKeyID     = "AWS_ACCESS_KEY_ID"
	envSecretAccessKey = "AWS_SECRET_ACCESS_KEY"
	envSessionToken    = "AWS_SESSION_TOKEN"

	s3SigningService    = "s3"
	s3DeltaCursorPrefix = "s3delta:"
)

type liveClient struct {
	http      *http.Client
	endpoint  string
	bucket    string
	prefix    string
	region    string
	pathStyle bool
	creds     awssig.Creds
	now       func() time.Time
}

func newLiveClient(cfg sdk.Config) (*liveClient, error) {
	bucket := strings.TrimSpace(cfg.Get("bucket"))
	if bucket == "" {
		return nil, errors.New("s3content: bucket is required for live mode")
	}
	region := strings.TrimSpace(cfg.Get("region"))
	if region == "" {
		region = defaultRegion
	}
	endpoint := strings.TrimSpace(cfg.Get("endpoint"))
	endpointSet := endpoint != ""
	if endpoint == "" {
		endpoint = fmt.Sprintf("https://s3.%s.amazonaws.com", region)
	}
	akid := firstNonEmpty(strings.TrimSpace(cfg.Get("access_key_id")), os.Getenv(envAccessKeyID))
	secret := firstNonEmpty(strings.TrimSpace(cfg.Get("secret_access_key")), os.Getenv(envSecretAccessKey))
	token := firstNonEmpty(strings.TrimSpace(cfg.Get("session_token")), os.Getenv(envSessionToken))
	return &liveClient{
		http:      &http.Client{Timeout: cfg.GetDuration("timeout", defaultTimeout)},
		endpoint:  strings.TrimRight(endpoint, "/"),
		bucket:    bucket,
		prefix:    strings.TrimSpace(cfg.Get("prefix")),
		region:    region,
		pathStyle: endpointSet || cfg.GetBool("path_style", false),
		creds:     awssig.Creds{AKID: akid, Secret: secret, Token: token},
		now:       time.Now,
	}, nil
}

// DeltaList walks ListObjectsV2 and reports objects modified after sinceToken.
//
// ┌─────────────────────────────────────────────────────────────────────────────┐
// │ KNOWN LIMITATION — S3 LISTOBJECTSV2 CANNOT REPORT OBJECT DELETIONS         │
// │                                                                             │
// │ ListObjectsV2 only surfaces objects that currently exist. DeltaList never  │
// │ emits ChangeDeleted; deleted objects are detected by full-list             │
// │ reconciliation, comparing List's live object set with the indexed set.     │
// └─────────────────────────────────────────────────────────────────────────────┘
func (s *Source) DeltaList(ctx context.Context, sinceToken string) (contentsource.DeltaPage, error) {
	if s.live == nil {
		return contentsource.DeltaPage{}, errors.New("s3content: DeltaList requires live mode")
	}
	sinceTime, continuation, maxSeen, expired, err := decodeDeltaCursor(sinceToken)
	if err != nil {
		return contentsource.DeltaPage{}, err
	}
	if expired {
		return contentsource.DeltaPage{Expired: true}, nil
	}

	result, err := s.live.listObjects(ctx, continuation)
	if err != nil {
		return contentsource.DeltaPage{}, err
	}
	page := contentsource.DeltaPage{}
	for _, obj := range result.Contents {
		key := strings.TrimSpace(obj.Key)
		if key == "" {
			continue
		}
		modifiedAt := parseTime(obj.LastModified)
		if !sinceTime.IsZero() && !modifiedAt.After(sinceTime) {
			continue
		}
		if modifiedAt.After(maxSeen) {
			maxSeen = modifiedAt
		}
		page.Changes = append(page.Changes, contentsource.DeltaEntry{
			DocRef: contentsource.DocRef{
				DocID:       content.Truncate(key, content.MaxRefLen),
				Title:       content.Truncate(path.Base(key), content.MaxTitleLen),
				ContentType: contentTypeForKey(key),
				ModifiedAt:  modifiedAt,
			},
			ChangeKind: contentsource.ChangeContent,
		})
	}
	if next := strings.TrimSpace(result.NextContinuationToken); result.IsTruncated && next != "" {
		page.NextToken = encodeDeltaCursor(sinceTime, next, maxSeen)
		return page, nil
	}
	if !maxSeen.IsZero() {
		page.ResumeToken = maxSeen.UTC().Format(time.RFC3339)
	}
	return page, nil
}

func (s *Source) listLive(ctx context.Context, cursor string) ([]contentsource.DocRef, string, error) {
	if s.live == nil {
		return nil, "", errors.New("s3content: List requires live mode")
	}
	result, err := s.live.listObjects(ctx, strings.TrimSpace(cursor))
	if err != nil {
		return nil, "", err
	}
	refs := make([]contentsource.DocRef, 0, len(result.Contents))
	for _, obj := range result.Contents {
		key := strings.TrimSpace(obj.Key)
		if key == "" {
			continue
		}
		refs = append(refs, contentsource.DocRef{
			DocID:       content.Truncate(key, content.MaxRefLen),
			Title:       content.Truncate(path.Base(key), content.MaxTitleLen),
			ContentType: contentTypeForKey(key),
			ModifiedAt:  parseTime(obj.LastModified),
		})
	}
	next := ""
	if result.IsTruncated {
		next = strings.TrimSpace(result.NextContinuationToken)
	}
	return refs, next, nil
}

func (s *Source) fetchLive(ctx context.Context, key string) (contentsource.Document, error) {
	if s.live == nil {
		return contentsource.Document{}, errors.New("s3content: Fetch requires live mode")
	}
	body, contentType, err := s.live.getObject(ctx, key)
	if err != nil {
		return contentsource.Document{}, err
	}
	acl, classification, err := s.live.fetchACLAndClassification(ctx, key)
	if err != nil {
		return contentsource.Document{}, err
	}
	return contentsource.Document{
		Source:         contentsource.SourceS3,
		DocID:          content.Truncate(key, content.MaxRefLen),
		Title:          content.Truncate(path.Base(key), content.MaxTitleLen),
		Body:           string(body),
		ContentType:    contentType,
		ACL:            content.CleanACL(acl),
		Classification: classification,
		SpaceRef:       "s3:" + s.live.bucket,
		Attributes:     map[string]string{"key": key},
	}, nil
}

func (s *Source) FetchACL(ctx context.Context, key string) (contentsource.ACLResult, error) {
	if s.live == nil {
		return contentsource.ACLResult{}, errors.New("s3content: FetchACL requires live mode")
	}
	acl, classification, err := s.live.fetchACLAndClassification(ctx, key)
	if err != nil {
		return contentsource.ACLResult{}, err
	}
	return contentsource.ACLResult{
		ACL:            content.CleanACL(acl),
		Classification: classification,
	}, nil
}

func (lc *liveClient) listObjects(ctx context.Context, continuation string) (s3ListBucketResult, error) {
	values := url.Values{}
	values.Set("list-type", "2")
	if lc.prefix != "" {
		values.Set("prefix", lc.prefix)
	}
	if continuation != "" {
		values.Set("continuation-token", continuation)
	}
	reqURL := lc.s3URL("", values.Encode())
	body, _, err := lc.doGET(ctx, reqURL, content.MaxBodyBytes*16)
	if err != nil {
		return s3ListBucketResult{}, err
	}
	var result s3ListBucketResult
	if err := xml.Unmarshal(body, &result); err != nil {
		return s3ListBucketResult{}, fmt.Errorf("s3content: parse ListObjectsV2 response: %w", err)
	}
	return result, nil
}

func (lc *liveClient) getObject(ctx context.Context, key string) ([]byte, string, error) {
	body, resp, err := lc.doGET(ctx, lc.s3URL(key, ""), content.MaxBodyBytes)
	if err != nil {
		return nil, "", err
	}
	contentType := strings.TrimSpace(resp.Header.Get("Content-Type"))
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	return body, contentType, nil
}

func (lc *liveClient) fetchACLAndClassification(ctx context.Context, key string) ([]string, string, error) {
	aclBody, _, err := lc.doGET(ctx, lc.s3URL(key, "acl"), content.MaxBodyBytes)
	if err != nil {
		return nil, "", err
	}
	var policy s3AccessControlPolicy
	if err := xml.Unmarshal(aclBody, &policy); err != nil {
		return nil, "", fmt.Errorf("s3content: parse GetObjectAcl response: %w", err)
	}
	tagBody, _, err := lc.doGET(ctx, lc.s3URL(key, "tagging"), content.MaxBodyBytes)
	if err != nil {
		return nil, "", err
	}
	var tagging s3Tagging
	if err := xml.Unmarshal(tagBody, &tagging); err != nil {
		return nil, "", fmt.Errorf("s3content: parse GetObjectTagging response: %w", err)
	}
	return aclFromPolicy(policy), classificationFromTags(tagging), nil
}

func (lc *liveClient) doGET(ctx context.Context, reqURL string, limit int64) ([]byte, *http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("s3content: build request: %w", err)
	}
	awssig.Sign(req, nil, s3SigningService, lc.region, lc.creds, lc.now())
	resp, err := lc.http.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("s3content: request: %w", err)
	}
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, limit))
	closeErr := resp.Body.Close()
	if readErr != nil {
		return nil, nil, fmt.Errorf("s3content: read response body: %w", readErr)
	}
	if closeErr != nil {
		return nil, nil, fmt.Errorf("s3content: close response body: %w", closeErr)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, nil, fmt.Errorf("s3content: request returned HTTP %d", resp.StatusCode)
	}
	return body, resp, nil
}

func (lc *liveClient) s3URL(key, rawQuery string) string {
	base, _ := url.Parse(lc.endpoint)
	u := *base
	// The object key is URI-encoded with the AWS SigV4 rules (slashes kept as
	// segment separators) and mirrored into RawPath so the wire path equals the
	// canonical path awssig signs from EscapedPath(). Go's laxer default path
	// escaping leaves '+', '=' or '$' raw, which S3 re-canonicalizes strictly on
	// its side — a key with those bytes would fail with SignatureDoesNotMatch.
	escapedBucket := awssig.URIEncode(lc.bucket, false)
	escapedKey := awssig.URIEncode(key, false)
	if lc.pathStyle {
		if key == "" {
			u.Path, u.RawPath = "/"+lc.bucket, "/"+escapedBucket
		} else {
			u.Path, u.RawPath = "/"+lc.bucket+"/"+key, "/"+escapedBucket+"/"+escapedKey
		}
	} else {
		u.Host = lc.bucket + "." + u.Host
		if key == "" {
			u.Path, u.RawPath = "/", "/"
		} else {
			u.Path, u.RawPath = "/"+key, "/"+escapedKey
		}
	}
	u.RawQuery = rawQuery
	return u.String()
}

type s3ListBucketResult struct {
	Contents              []s3ListedObject `xml:"Contents"`
	IsTruncated           bool             `xml:"IsTruncated"`
	NextContinuationToken string           `xml:"NextContinuationToken"`
}

type s3ListedObject struct {
	Key          string `xml:"Key"`
	LastModified string `xml:"LastModified"`
}

type s3AccessControlPolicy struct {
	Grants []s3Grant `xml:"AccessControlList>Grant"`
}

type s3Grant struct {
	Grantee s3Grantee `xml:"Grantee"`
}

type s3Grantee struct {
	Type         string `xml:"type,attr"`
	XSIType      string `xml:"http://www.w3.org/2001/XMLSchema-instance type,attr"`
	ID           string `xml:"ID"`
	DisplayName  string `xml:"DisplayName"`
	URI          string `xml:"URI"`
	EmailAddress string `xml:"EmailAddress"`
}

type s3Tagging struct {
	Tags []s3Tag `xml:"TagSet>Tag"`
}

type s3Tag struct {
	Key   string `xml:"Key"`
	Value string `xml:"Value"`
}

func aclFromPolicy(policy s3AccessControlPolicy) []string {
	var acl []string
	for _, grant := range policy.Grants {
		g := grant.Grantee
		switch strings.ToLower(firstNonEmpty(g.XSIType, g.Type)) {
		case "canonicaluser":
			if id := strings.TrimSpace(g.ID); id != "" {
				acl = append(acl, "user:"+id)
			} else if displayName := strings.TrimSpace(g.DisplayName); displayName != "" {
				acl = append(acl, "user:"+displayName)
			}
		case "group":
			if uri := strings.TrimSpace(g.URI); uri != "" {
				acl = append(acl, "group:"+lastSegment(uri))
			}
		case "amazoncustomerbyemail", "email":
			if email := strings.TrimSpace(g.EmailAddress); email != "" {
				acl = append(acl, "email:"+email)
			}
		}
	}
	return acl
}

func classificationFromTags(tagging s3Tagging) string {
	for _, tag := range tagging.Tags {
		if strings.EqualFold(strings.TrimSpace(tag.Key), "classification") {
			return strings.TrimSpace(tag.Value)
		}
	}
	return ""
}

func contentTypeForKey(key string) string {
	if ctype := mime.TypeByExtension(path.Ext(key)); ctype != "" {
		return ctype
	}
	return "application/octet-stream"
}

type s3DeltaCursor struct {
	Since        string `json:"since,omitempty"`
	Continuation string `json:"continuation"`
	Max          string `json:"max,omitempty"`
}

func decodeDeltaCursor(token string) (time.Time, string, time.Time, bool, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return time.Time{}, "", time.Time{}, false, nil
	}
	if strings.HasPrefix(token, s3DeltaCursorPrefix) {
		raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(token, s3DeltaCursorPrefix))
		if err != nil {
			return time.Time{}, "", time.Time{}, true, nil
		}
		var cur s3DeltaCursor
		if err := json.Unmarshal(raw, &cur); err != nil {
			return time.Time{}, "", time.Time{}, true, nil
		}
		var sinceTime, maxTime time.Time
		if cur.Since != "" {
			t, err := time.Parse(time.RFC3339, cur.Since)
			if err != nil {
				return time.Time{}, "", time.Time{}, true, nil
			}
			sinceTime = t.UTC()
		}
		if cur.Max != "" {
			t, err := time.Parse(time.RFC3339, cur.Max)
			if err != nil {
				return time.Time{}, "", time.Time{}, true, nil
			}
			maxTime = t.UTC()
		}
		return sinceTime, cur.Continuation, maxTime, false, nil
	}
	t, err := time.Parse(time.RFC3339, token)
	if err != nil {
		return time.Time{}, "", time.Time{}, true, nil
	}
	return t.UTC(), "", time.Time{}, false, nil
}

func encodeDeltaCursor(since time.Time, continuation string, maxSeen time.Time) string {
	cur := s3DeltaCursor{Continuation: continuation}
	if !since.IsZero() {
		cur.Since = since.UTC().Format(time.RFC3339)
	}
	if !maxSeen.IsZero() {
		cur.Max = maxSeen.UTC().Format(time.RFC3339)
	}
	data, _ := json.Marshal(cur)
	return s3DeltaCursorPrefix + base64.RawURLEncoding.EncodeToString(data)
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
