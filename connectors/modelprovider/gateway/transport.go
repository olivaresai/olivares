// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/olivaresai/olivares/connectors/modelprovider"
)

type httpDriver struct {
	name     DriverName
	protocol Protocol
	path     string
	base     string
	scheme   modelprovider.AuthScheme
	cred     string
	doer     modelprovider.Doer
	client   *modelprovider.InferenceClient
	hook     CostHook
	extra    map[string]string
	encode   func(req MessageRequest, stream bool) (any, error)
	decode   func(raw json.RawMessage) (MessageResponse, error)
	parse    streamParser
}

func newHTTPDriver(cfg Config, name DriverName, protocol Protocol, path string, extra map[string]string) (*httpDriver, error) {
	base := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if base == "" {
		return nil, &Error{Code: CodeBadRequest, HTTPStatus: 400, Message: "base URL is required"}
	}
	scheme := modelprovider.AuthNone
	switch name {
	case DriverOpenAICompat:
		scheme = modelprovider.AuthBearer
	case DriverAnthropic:
		scheme = modelprovider.AuthAnthropicKey
	}
	doer := cfg.Doer
	if doer == nil {
		doer = http.DefaultClient
	}
	return &httpDriver{
		name:     name,
		protocol: protocol,
		path:     path,
		base:     base,
		scheme:   scheme,
		cred:     cfg.Credential,
		doer:     doer,
		client:   modelprovider.NewInferenceClient(base, cfg.Doer, scheme, cfg.Credential, extra),
		hook:     hookOrNop(cfg.CostHook),
		extra:    extra,
	}, nil
}

func (d *httpDriver) Descriptor() Descriptor {
	transports := []Transport{TransportHTTP}
	if d.protocol == ProtocolOllama {
		// Native Ollama stream is NDJSON on HTTP, not SSE.
		return Descriptor{Driver: d.name, Protocol: d.protocol, Transports: transports}
	}
	return Descriptor{Driver: d.name, Protocol: d.protocol, Transports: []Transport{TransportHTTP, TransportSSE}}
}

func (d *httpDriver) CreateMessage(ctx context.Context, req MessageRequest) (MessageResponse, error) {
	if err := ValidateRequest(req); err != nil {
		return MessageResponse{}, err
	}
	res, err := reserveOrDeny(ctx, d.hook, req)
	if err != nil {
		return MessageResponse{}, err
	}
	body, err := d.encode(req, false)
	if err != nil {
		_ = d.hook.Release(ctx, res)
		return MessageResponse{}, err
	}
	var raw json.RawMessage
	if err := d.client.PostJSON(ctx, d.path, body, &raw, d.extra); err != nil {
		_ = d.hook.Release(ctx, res)
		return MessageResponse{}, mapTransportError(err)
	}
	out, err := d.decode(raw)
	if err != nil {
		_ = d.hook.Release(ctx, res)
		return MessageResponse{}, err
	}
	if err := d.hook.Commit(ctx, res, out.Usage); err != nil {
		return out, err
	}
	return out, nil
}

func (d *httpDriver) StreamMessage(ctx context.Context, req MessageRequest) (Stream, error) {
	if err := ValidateRequest(req); err != nil {
		return nil, err
	}
	res, err := reserveOrDeny(ctx, d.hook, req)
	if err != nil {
		return nil, err
	}
	body, err := d.encode(req, true)
	if err != nil {
		_ = d.hook.Release(ctx, res)
		return nil, err
	}
	rc, err := d.openStream(ctx, body)
	if err != nil {
		_ = d.hook.Release(ctx, res)
		return nil, mapTransportError(err)
	}
	return newPullStream(ctx, rc, d.parse, d.hook, res), nil
}

func (d *httpDriver) openStream(ctx context.Context, body any) (io.ReadCloser, error) {
	if d.protocol != ProtocolOllama {
		return d.client.PostStream(ctx, d.path, body, d.extra)
	}
	buf, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.base+d.path, bytes.NewReader(buf))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/x-ndjson")
	for k, v := range d.extra {
		req.Header.Set(k, v)
	}
	resp, err := d.doer.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		excerpt, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		_ = resp.Body.Close()
		return nil, &modelprovider.APIError{Method: http.MethodPost, Path: d.path, Status: resp.StatusCode, Body: strings.TrimSpace(string(excerpt))}
	}
	return resp.Body, nil
}

func decodeJSON[T any](raw json.RawMessage, out *T) error {
	if err := json.Unmarshal(raw, out); err != nil {
		return &Error{Code: CodeInternal, HTTPStatus: 502, Message: "invalid upstream response"}
	}
	return nil
}
