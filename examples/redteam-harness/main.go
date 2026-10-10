// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// An operator-provisioned sandboxrt guest harness for JSON-over-HTTP targets.
// Build statically and place it at /sandbox-harness in the read-only rootfs.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"time"
)

type job struct {
	Target      string `json:"target"`
	ProxyURL    string `json:"proxy_url"`
	ProxySocket string `json:"proxy_socket"`
	TimeoutMS   int64  `json:"timeout_ms"`
	Probe       *probe `json:"probe"`
	Steps       []step `json:"steps"`
	Mocks       []mock `json:"mocks"`
}
type probe struct {
	ID      string `json:"id"`
	Surface string `json:"surface"`
	Payload string `json:"payload"`
}
type step struct {
	Key   string `json:"key"`
	Input string `json:"input"`
}
type mock struct {
	Resource string `json:"resource"`
	Response string `json:"response"`
}
type stepOutput struct {
	Key     string `json:"key"`
	Output  string `json:"output"`
	MockHit bool   `json:"mock_hit"`
}
type result struct {
	Steps    []stepOutput `json:"steps"`
	Response string       `json:"response,omitempty"`
	Reached  bool         `json:"reached,omitempty"`
	Error    string       `json:"error,omitempty"`
}

func execute(input io.Reader, output io.Writer) error {
	var j job
	if err := json.NewDecoder(io.LimitReader(input, 1<<20)).Decode(&j); err != nil {
		return fmt.Errorf("invalid job JSON")
	}
	r := result{Steps: []stepOutput{}}
	resolve := make(map[string]string, len(j.Mocks))
	for _, m := range j.Mocks {
		resolve[m.Resource] = m.Response
	}
	for _, s := range j.Steps {
		o := stepOutput{Key: s.Key, Output: "[[mock-miss:" + s.Input + "]]"}
		if response, ok := resolve[s.Input]; ok {
			o.Output, o.MockHit = response, true
		}
		r.Steps = append(r.Steps, o)
	}
	if j.Probe != nil {
		proxy, err := url.Parse(j.ProxyURL)
		if err != nil || proxy.Scheme != "http" || proxy.Host == "" {
			return fmt.Errorf("an explicit HTTP egress proxy is required")
		}
		transport := &http.Transport{Proxy: http.ProxyURL(proxy), DisableKeepAlives: true}
		if j.ProxySocket != "" {
			transport.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", j.ProxySocket)
			}
		}
		defer transport.CloseIdleConnections()
		timeout := 10 * time.Second
		if j.TimeoutMS > 0 && j.TimeoutMS < timeout.Milliseconds() {
			timeout = time.Duration(j.TimeoutMS) * time.Millisecond
		}
		client := &http.Client{Transport: transport, Timeout: timeout,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		payload, err := json.Marshal(j.Probe)
		if err != nil {
			return err
		}
		req, err := http.NewRequest("POST", j.Target, bytes.NewReader(payload))
		if err != nil {
			return fmt.Errorf("invalid target URL")
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			return fmt.Errorf("target not reached through proxy")
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("target or proxy returned HTTP %d", resp.StatusCode)
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
		if err != nil || len(body) > 1<<20 {
			return fmt.Errorf("target response unreadable or too large")
		}
		r.Response, r.Reached = string(body), true
	}
	return json.NewEncoder(output).Encode(r)
}

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: sandbox-harness <job.json>")
		os.Exit(2)
	}
	input, err := os.Open(os.Args[1])
	if err == nil {
		defer input.Close()
		err = execute(input, os.Stdout)
	}
	if err != nil {
		_ = json.NewEncoder(os.Stdout).Encode(result{Error: err.Error()})
		os.Exit(1)
	}
}
