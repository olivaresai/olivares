// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package documents

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const OCRDisabled = "this page has no text layer; OCR is off (Settings > Modules)"

// OCRConfig selects an administrator-declared model; conversion never downloads weights.
type OCRConfig struct {
	Endpoint string `json:"endpoint,omitempty"`
	Model    string `json:"model,omitempty"`
}

// Validate accepts only local services and requires a declared digest-qualified model alias.
func (c OCRConfig) Validate() error {
	if c.Endpoint == "" {
		return nil
	}
	u, err := url.Parse(c.Endpoint)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("OCR endpoint must be an HTTP(S) URL without credentials, query or fragment")
	}
	ip := net.ParseIP(u.Hostname())
	if u.Hostname() == "localhost" {
		ip = net.ParseIP("127.0.0.1")
	}
	local := ip != nil && ip.IsLoopback()
	if ip != nil && !local {
		addresses, err := net.InterfaceAddrs()
		if err != nil {
			return errors.New("cannot verify OCR endpoint locality")
		}
		for _, address := range addresses {
			addr, _, err := net.ParseCIDR(address.String())
			if err == nil && addr.Equal(ip) {
				local = true
			}
		}
	}
	if !local {
		return errors.New("OCR endpoint must use loopback or this host's literal IP")
	}
	if !regexp.MustCompile(`^Unlimited-OCR-2@sha256:[0-9a-f]{64}$`).MatchString(c.Model) {
		return errors.New("OCR model must be the declared served alias Unlimited-OCR-2@sha256:<64 lowercase hex digits>")
	}
	return nil
}

type documentPage struct {
	Text  string `json:"text"`
	Image string `json:"image"`
}

func (c OCRConfig) recognize(ctx context.Context, image string) (string, error) {
	if err := c.Validate(); err != nil {
		return "", err
	}
	body, err := json.Marshal(map[string]any{"model": c.Model, "max_tokens": 8192, "messages": []any{map[string]any{"role": "user", "content": []any{map[string]any{"type": "image_url", "image_url": map[string]string{"url": image}}, map[string]string{"type": "text", "text": "<PAGE><image>Multi page parsing."}}}}})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(c.Endpoint, "/")+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", errors.New("invalid OCR request")
	}
	req.Header.Set("Content-Type", "application/json")
	// No environment proxy, DNS rebinding or redirect can change the approved destination.
	transport := &http.Transport{DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		if host == "localhost" {
			address = net.JoinHostPort("127.0.0.1", port)
		}
		return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, network, address)
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 60 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return "", errors.New("OCR endpoint request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("OCR endpoint returned HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, MaxBytes+1))
	if err != nil || len(data) > MaxBytes {
		return "", errors.New("OCR response unreadable or exceeds size limit")
	}
	var result struct {
		Choices []struct {
			FinishReason string `json:"finish_reason"`
			Message      struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if json.Unmarshal(data, &result) != nil || len(result.Choices) != 1 || strings.TrimSpace(result.Choices[0].Message.Content) == "" {
		return "", errors.New("OCR endpoint produced no text")
	}
	if reason := result.Choices[0].FinishReason; reason == "length" || reason == "content_filter" {
		return "", errors.New("OCR endpoint did not complete the page")
	}
	return result.Choices[0].Message.Content, nil
}

// IsImage reports image inputs accepted by the packaged converter.
func IsImage(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".png", ".jpg", ".jpeg", ".webp", ".gif":
		return true
	}
	return false
}
