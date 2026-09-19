// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package gateway

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"sync"
)

type streamParser func(r *bufio.Reader) (Event, error)

type pullStream struct {
	mu      sync.Mutex
	body    io.ReadCloser
	br      *bufio.Reader
	parse   streamParser
	hook    CostHook
	res     Reservation
	usage   Usage
	ctx     context.Context
	quit    chan struct{}
	closed  bool
	eof     bool
	settled bool
}

func newPullStream(ctx context.Context, body io.ReadCloser, parse streamParser, hook CostHook, res Reservation) *pullStream {
	s := &pullStream{
		body:  body,
		br:    bufio.NewReader(body),
		parse: parse,
		hook:  hook,
		res:   res,
		ctx:   ctx,
		quit:  make(chan struct{}),
	}
	go func() {
		select {
		case <-ctx.Done():
			_ = body.Close()
		case <-s.quit:
		}
	}()
	return s
}

func (s *pullStream) Recv() (Event, error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return Event{}, &Error{Code: CodeCanceled, HTTPStatus: 499, Message: "stream closed"}
	}
	if s.eof {
		s.mu.Unlock()
		return Event{}, io.EOF
	}
	if err := s.ctx.Err(); err != nil {
		s.releaseLocked()
		s.mu.Unlock()
		return Event{}, mapTransportError(err)
	}
	br := s.br
	s.mu.Unlock()

	ev, err := s.parse(br)

	s.mu.Lock()
	defer s.mu.Unlock()
	if err == io.EOF {
		s.commitLocked()
		return Event{}, io.EOF
	}
	if err != nil {
		s.releaseLocked()
		if s.ctx.Err() != nil {
			return Event{}, mapTransportError(s.ctx.Err())
		}
		return Event{}, mapTransportError(err)
	}
	if ev.Usage != nil {
		if ev.Usage.InputTokens > 0 {
			s.usage.InputTokens = ev.Usage.InputTokens
		}
		if ev.Usage.OutputTokens > 0 {
			s.usage.OutputTokens = ev.Usage.OutputTokens
		}
	}
	if ev.Type == EventStop {
		s.commitLocked()
	}
	return ev, nil
}

func (s *pullStream) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	select {
	case <-s.quit:
	default:
		close(s.quit)
	}
	s.releaseLocked()
	body := s.body
	s.mu.Unlock()
	if body != nil {
		return body.Close()
	}
	return nil
}

func (s *pullStream) Usage() Usage {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.usage
}

func (s *pullStream) commitLocked() {
	if s.settled {
		s.eof = true
		return
	}
	s.settled = true
	s.eof = true
	_ = s.hook.Commit(s.ctx, s.res, s.usage)
}

func (s *pullStream) releaseLocked() {
	if s.settled {
		return
	}
	s.settled = true
	s.eof = true
	_ = s.hook.Release(s.ctx, s.res)
}

func readSSE(r *bufio.Reader) (eventName, data string, err error) {
	var dataBuf strings.Builder
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return "", "", err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			if eventName == "" && dataBuf.Len() == 0 {
				continue
			}
			return eventName, dataBuf.String(), nil
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		if strings.HasPrefix(line, "event:") {
			eventName = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			continue
		}
		if strings.HasPrefix(line, "data:") {
			payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if dataBuf.Len() > 0 {
				dataBuf.WriteByte('\n')
			}
			dataBuf.WriteString(payload)
		}
	}
}

func parseOpenAISSE(r *bufio.Reader) (Event, error) {
	_, data, err := readSSE(r)
	if err != nil {
		return Event{}, err
	}
	if data == "[DONE]" {
		return Event{}, io.EOF
	}
	var chunk openaiChatResponse
	if err := json.Unmarshal([]byte(data), &chunk); err != nil {
		return Event{}, &Error{Code: CodeInternal, HTTPStatus: 502, Message: "invalid upstream response"}
	}
	ev := Event{Type: EventTextDelta}
	if len(chunk.Choices) > 0 {
		ev.Text = chunk.Choices[0].Delta.Content
		if chunk.Choices[0].FinishReason != "" && ev.Text == "" {
			ev.Type = EventStop
		}
	}
	if chunk.Usage != nil {
		u := Usage{InputTokens: chunk.Usage.PromptTokens, OutputTokens: chunk.Usage.CompletionTokens}
		ev.Usage = &u
		if ev.Text == "" && ev.Type != EventStop {
			ev.Type = EventUsage
		}
	}
	return ev, nil
}

func parseAnthropicSSE(r *bufio.Reader) (Event, error) {
	var name, data string
	var err error
	for {
		name, data, err = readSSE(r)
		if err != nil {
			return Event{}, err
		}
		if name != "ping" {
			break
		}
	}
	switch name {
	case "message_stop":
		return Event{}, io.EOF
	case "error":
		return Event{}, &Error{Code: CodeUnavailable, HTTPStatus: 502, Message: "upstream stream error"}
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(data), &raw); err != nil {
		return Event{}, &Error{Code: CodeInternal, HTTPStatus: 502, Message: "invalid upstream response"}
	}
	ev := Event{Type: EventTextDelta}
	if delta, ok := raw["delta"]; ok {
		var d struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		_ = json.Unmarshal(delta, &d)
		ev.Text = d.Text
	}
	if usage, ok := raw["usage"]; ok {
		var u struct {
			InputTokens  int64 `json:"input_tokens"`
			OutputTokens int64 `json:"output_tokens"`
		}
		if json.Unmarshal(usage, &u) == nil {
			got := Usage{InputTokens: u.InputTokens, OutputTokens: u.OutputTokens}
			ev.Usage = &got
		}
	}
	if msg, ok := raw["message"]; ok {
		var m struct {
			Usage struct {
				InputTokens  int64 `json:"input_tokens"`
				OutputTokens int64 `json:"output_tokens"`
			} `json:"usage"`
		}
		if json.Unmarshal(msg, &m) == nil {
			got := Usage{InputTokens: m.Usage.InputTokens, OutputTokens: m.Usage.OutputTokens}
			ev.Usage = &got
		}
	}
	if name == "message_delta" && ev.Text == "" {
		ev.Type = EventUsage
	}
	return ev, nil
}

func parseOllamaNDJSON(r *bufio.Reader) (Event, error) {
	var (
		line    []byte
		err     error
		trimmed []byte
	)
	for {
		line, err = r.ReadBytes('\n')
		trimmed = bytes.TrimSpace(line)
		if len(trimmed) > 0 {
			break
		}
		if err != nil {
			return Event{}, err
		}
	}
	var chunk ollamaChatResponse
	if jerr := json.Unmarshal(trimmed, &chunk); jerr != nil {
		return Event{}, &Error{Code: CodeInternal, HTTPStatus: 502, Message: "invalid upstream response"}
	}
	ev := Event{Type: EventTextDelta, Text: chunk.Message.Content}
	if chunk.Done {
		u := Usage{InputTokens: chunk.PromptEvalCount, OutputTokens: chunk.EvalCount}
		ev.Usage = &u
		ev.Type = EventStop
	}
	return ev, nil
}
