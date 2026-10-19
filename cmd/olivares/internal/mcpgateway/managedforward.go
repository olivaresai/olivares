// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
package mcpgateway

import (
	"bufio"
	"encoding/json"
	"errors"
	mcpc "github.com/olivaresai/olivares/connectors/mcp"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
)

// managedSessionLimit bounds the upstream sessions one managed server keeps in memory. An entry
// measures about 105 bytes of heap, so 4096 of them are 0.4 MiB: the bound is a memory fence far above
// the 600 clients the product shows, not a client limit. At the limit the principal idle longest is
// dropped (a scan of 44 µs) and re-initializes on its next call.
const managedSessionLimit = 4096

// managedMCPSession is one principal's upstream session. mu guards id and version and is held
// across that principal's upstream calls; lastUse is guarded by the forwarder's table lock.
type managedMCPSession struct {
	mu          sync.Mutex
	id, version string
	lastUse     uint64
}

var errManagedInitializeRequired = errors.New("mcp gateway: initialize required for this client")

// EnableManagedForwarding makes f a store-owned forwarder: each client must initialize
// its own upstream MCP session, which f keeps and replays on every later call.
func EnableManagedForwarding(f *UpstreamForwarder) {
	f.managed = true
	f.sessions = map[string]*managedMCPSession{}
}

// managedSession returns the session of the request's principal, creating it for an initialize
// (dropping the least recently used when the table is full). Any other method needs an existing one.
// An evicted principal whose call is still in flight finishes on its dropped session and initializes
// again on its next call. ponytail: eviction is by last request start, so it needs 4096 other
// principals to initialize inside one upstream round trip; upgrade when an operator sees it.
func (f *UpstreamForwarder) managedSession(req mcpc.UpstreamRequest) (*managedMCPSession, error) {
	key := managedMCPSessionKey(req)
	f.mu.Lock()
	defer f.mu.Unlock()
	session := f.sessions[key]
	if session == nil {
		if req.Method != "initialize" {
			return nil, errManagedInitializeRequired
		}
		if len(f.sessions) >= managedSessionLimit {
			f.dropLeastRecentlyUsed()
		}
		session = &managedMCPSession{}
		f.sessions[key] = session
	}
	f.tick++
	session.lastUse = f.tick
	return session, nil
}

func (f *UpstreamForwarder) dropLeastRecentlyUsed() {
	var oldest string
	var oldestUse uint64
	for key, session := range f.sessions {
		if oldest == "" || session.lastUse < oldestUse {
			oldest, oldestUse = key, session.lastUse
		}
	}
	delete(f.sessions, oldest)
}

// The RS has one explicit issuer/resource. Client, subject and granted scopes
// namespace its upstream handshake; a foreign principal cannot reuse it.
func managedMCPSessionKey(req mcpc.UpstreamRequest) string {
	scopes := append([]string(nil), req.Scopes...)
	sort.Strings(scopes)
	raw, _ := json.Marshal([]any{req.Subject, req.ClientID, scopes})
	return string(raw)
}

func validManagedMCPSessionID(id string) bool {
	return len(id) <= 256 && !strings.ContainsFunc(id, func(r rune) bool { return r < 0x21 || r > 0x7e })
}

// Each observed response still passes strict JSON-RPC correlation before it can
// settle completed. Interleaved server requests are never executed or answered.
func readManagedMCPResponse(resp *http.Response, id int64) ([]byte, error) {
	reader := io.LimitReader(resp.Body, mcpForwardMaxResponse+1)
	if !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		raw, err := io.ReadAll(reader)
		if errors.Is(err, mcpc.ErrUpstreamCredentialDisclosure) {
			return nil, err
		}
		if err != nil || len(raw) > mcpForwardMaxResponse {
			return nil, errors.New("mcp gateway: unreadable bounded response")
		}
		return raw, nil
	}
	scan := bufio.NewScanner(reader)
	scan.Buffer(make([]byte, 0, 64<<10), mcpForwardMaxResponse)
	var data strings.Builder
	total := 0
	flush := func() ([]byte, error) {
		if data.Len() == 0 {
			return nil, nil
		}
		raw := []byte(data.String())
		data.Reset()
		var envelope struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if json.Unmarshal(raw, &envelope) != nil {
			return nil, errors.New("mcp gateway: invalid SSE message")
		}
		if len(envelope.ID) == 0 && strings.HasPrefix(envelope.Method, "notifications/") {
			return nil, nil
		}
		if _, _, err := mcpc.ParseStrictJSONRPCResponse(raw, id); err != nil {
			return nil, errors.New("mcp gateway: uncorrelated or unsupported SSE message")
		}
		return raw, nil
	}
	for scan.Scan() {
		line := scan.Text()
		total += len(line) + 1
		if total > mcpForwardMaxResponse {
			return nil, errors.New("mcp gateway: response exceeds bounded size")
		}
		if line == "" {
			if raw, err := flush(); raw != nil || err != nil {
				return raw, err
			}
		}
		if strings.HasPrefix(line, "data:") {
			if data.Len() > 0 {
				data.WriteByte('\n')
			}
			data.WriteString(strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		}
	}
	if errors.Is(scan.Err(), mcpc.ErrUpstreamCredentialDisclosure) {
		return nil, scan.Err()
	}
	if scan.Err() != nil {
		return nil, errors.New("mcp gateway: unreadable SSE response")
	}
	if raw, err := flush(); raw != nil || err != nil {
		return raw, err
	}
	return nil, errors.New("mcp gateway: no correlated SSE response")
}
