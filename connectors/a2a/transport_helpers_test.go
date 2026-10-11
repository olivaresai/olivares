// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package a2a

import (
	"bytes"
	"io"
	"net/http"
)

type stubDoer struct {
	cardBytes  []byte
	cardStatus int
	rpcBytes   []byte
	rpcStatus  int

	getReq    *http.Request
	postReq   *http.Request
	postBody  []byte
	postCount int
	getCount  int
}

func (s *stubDoer) Do(req *http.Request) (*http.Response, error) {
	switch req.Method {
	case http.MethodGet:
		s.getCount++
		s.getReq = req
		return mkResp(orDefault(s.cardStatus, 200), s.cardBytes), nil
	case http.MethodPost:
		s.postCount++
		s.postReq = req
		if req.Body != nil {
			s.postBody, _ = io.ReadAll(req.Body)
		}
		return mkResp(orDefault(s.rpcStatus, 200), s.rpcBytes), nil
	default:
		return mkResp(405, nil), nil
	}
}

func orDefault(v, d int) int {
	if v == 0 {
		return d
	}
	return v
}
func mkResp(status int, body []byte) *http.Response {
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(bytes.NewReader(body)),
		Header:     make(http.Header),
	}
}
