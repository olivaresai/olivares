// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package localclient speaks the fixed local protocol. It never resends a
// request or reconnects implicitly after an uncertain response.
package localclient

import (
	"encoding/json"
	"errors"
	"net"
	"sync"
	"time"

	"github.com/olivaresai/olivares/appliance/layer/hostops"
	"github.com/olivaresai/olivares/appliance/layer/portal/localframe"
)

// Refused is an authenticated, closed refusal. No input or credential is echoed.
type Refused struct {
	Code          string
	BeforeRequest bool
}

func (e *Refused) Error() string { return e.Code }

// Failure distinguishes a preface failure from a response that cannot be
// verified after sending. Sent forbids a claim that nothing was sent.
type Failure struct {
	Code, Reason string
	Sent         bool
}

func (e *Failure) Error() string { return e.Code }

type wire interface {
	Next() (localframe.Record, error)
	Write(localframe.Record) error
	Close() error
}
type socketWire struct {
	conn *net.UnixConn
	uid  uint32
}

func (w socketWire) Next() (localframe.Record, error) {
	_ = w.conn.SetReadDeadline(time.Now().Add(30 * time.Second))
	return localframe.Next(w.conn, localframe.Expect{UID: w.uid}, nil)
}
func (w socketWire) Write(r localframe.Record) error {
	_ = w.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	return localframe.Write(w.conn, r)
}
func (w socketWire) Close() error { return w.conn.Close() }

// Client serializes request/response pairs on one authenticated connection.
type Client struct {
	mu     sync.Mutex
	wire   wire
	closed bool
}

func connect(w wire) (*Client, error) {
	first, err := w.Next()
	if err != nil {
		_ = w.Close()
		reason := localframe.Reason(err)
		if reason == "eof" || reason == "connection_closed" {
			return nil, &Failure{Code: "local_unavailable", Reason: "connection_closed"}
		}
		return nil, &Failure{Code: "local_protocol_mismatch", Reason: reason}
	}
	if first.Type == "refused" {
		_ = w.Close()
		return nil, &Refused{Code: first.Code, BeforeRequest: true}
	}
	if first.Type != "ready" || first.Version != 1 {
		_ = w.Close()
		return nil, &Failure{Code: "local_protocol_mismatch", Reason: "unexpected_record"}
	}
	return &Client{wire: w}, nil
}
func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	return c.wire.Close()
}

func (c *Client) call(op string, body any, dst any) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return 0, &Failure{Code: "session_lost", Sent: true}
	}
	data, err := json.Marshal(body)
	if err != nil {
		return 0, &Refused{Code: "input_refused"}
	}
	fail := func(reason string) (int, error) {
		c.closed = true
		_ = c.wire.Close()
		return 0, &Failure{Code: "response_unverified", Reason: reason, Sent: true}
	}
	// Even a failed write may have sent a prefix. No retry is safe.
	if err := c.wire.Write(localframe.Record{Type: "request", Op: op, Body: data}); err != nil {
		return fail("connection_closed")
	}
	reply, err := c.wire.Next()
	if err != nil {
		return fail(localframe.Reason(err))
	}
	if reply.Op != op || (reply.Type != "response" && reply.Type != "error") {
		return fail("unexpected_record")
	}
	if reply.Type == "error" {
		return reply.Status, &Refused{Code: reply.Code}
	}
	if hostops.DecodeClosed(reply.Body, dst) != nil {
		return fail("record_malformed")
	}
	return reply.Status, nil
}

// Operation reads an existing identifier. It does not mint or submit an act.
func (c *Client) Operation(id, surface string) (hostops.View, int, error) {
	var view hostops.View
	status, err := c.call("operation.get", struct {
		OperationID string `json:"operation_id"`
		Surface     string `json:"surface"`
	}{id, surface}, &view)
	if err == nil && (view.Record.OperationID != id || (status == 202) != (view.Record.State == hostops.StateRunning)) {
		_ = c.Close()
		return hostops.View{}, 0, &Failure{Code: "response_unverified", Reason: "record_malformed", Sent: true}
	}
	return view, status, err
}

// Plan reads the common descriptor and validated input. It grants no act.
func (c *Client) Plan(module, verb, surface string, inputs json.RawMessage) (hostops.Plan, error) {
	var plan hostops.Plan
	_, err := c.call("task.plan", struct {
		Module  string          `json:"module"`
		Verb    string          `json:"verb"`
		Surface string          `json:"surface"`
		Inputs  json.RawMessage `json:"inputs"`
	}{module, verb, surface, inputs}, &plan)
	return plan, err
}

// Description reads one declared task for the chosen renderer.
func (c *Client) Description(module, verb, surface string) (hostops.Descriptor, error) {
	var descriptor hostops.Descriptor
	_, err := c.call("task.describe", struct {
		Module  string `json:"module"`
		Verb    string `json:"verb"`
		Surface string `json:"surface"`
	}{module, verb, surface}, &descriptor)
	return descriptor, err
}

// Exit classifies a transport error without turning uncertainty into success.
func Exit(err error) int {
	if err == nil {
		return 0
	}
	var refused *Refused
	if errors.As(err, &refused) {
		return 1
	}
	return 2
}

// Descriptors reads the server's validated catalog for terminal navigation.
func (c *Client) Descriptors(surface string) ([]hostops.Descriptor, error) {
	var result struct {
		Tasks []hostops.Descriptor `json:"tasks"`
	}
	_, err := c.call("task.list", struct {
		Surface string `json:"surface"`
	}{surface}, &result)
	return result.Tasks, err
}
