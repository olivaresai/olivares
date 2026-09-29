// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package localsession

import (
	"encoding/json"
	"errors"
	"net"
	"sync"
	"time"

	"github.com/olivaresai/olivares/appliance/layer/hostops"
	"github.com/olivaresai/olivares/appliance/layer/portal/localframe"
)

const SocketPath = "/run/olivares-portal-api/local.sock"
const LifecycleDirectory = "/run/olivares-lifecycle"
const OperationDirectory = "/var/lib/olivares-portal/operations"

// Server has a read model and descriptors, deliberately no effect or grant issuer.
// Kernel is fixed to Linux by the production caller; it is not request input.
// modules is the portal's module read model, which module.read answers from.
type Server struct {
	service     *hostops.Service
	catalog     *hostops.Catalog
	kernel      Kernel
	modules     ModuleReader
	mu          sync.Mutex
	owners      map[int]*owner
	connections map[*net.UnixConn]bool
}

func NewServer(reader hostops.Reader, catalog *hostops.Catalog, kernel Kernel) *Server {
	return &Server{service: hostops.NewService(reader, hostops.ReadOnlyAccess{}), catalog: catalog, kernel: kernel, owners: map[int]*owner{}, connections: map[*net.UnixConn]bool{}}
}

// Serve accepts at most 32 simultaneous local connections. Closing the listener
// also closes its active sessions, whose pidfds are released by their handlers.
func (s *Server) Serve(listener net.Listener) error {
	defer func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		for c := range s.connections {
			_ = c.Close()
		}
	}()
	for {
		conn, err := listener.Accept()
		if err != nil {
			return err
		}
		c, ok := conn.(*net.UnixConn)
		if !ok {
			_ = conn.Close()
			continue
		}
		s.mu.Lock()
		full := len(s.connections) >= 32
		if !full {
			s.connections[c] = true
		}
		s.mu.Unlock()
		if full {
			_ = c.SetWriteDeadline(time.Now().Add(time.Second))
			if s.empty(c) {
				_ = localframe.Write(c, localframe.Record{Type: "refused", Code: "local_capacity"})
			}
			_ = c.Close()
			continue
		}
		go func() { defer func() { s.mu.Lock(); delete(s.connections, c); s.mu.Unlock() }(); s.serveConnection(c) }()
	}
}

func (s *Server) empty(c *net.UnixConn) bool {
	if s.kernel == nil {
		return false
	}
	raw, err := c.SyscallConn()
	if err != nil {
		return false
	}
	n := -1
	var checkErr error
	err = raw.Control(func(fd uintptr) { n, checkErr = s.kernel.Unread(int(fd)) })
	return err == nil && checkErr == nil && n == 0
}

func (s *Server) serveConnection(c *net.UnixConn) {
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(10 * time.Minute))
	raw, err := c.SyscallConn()
	if err != nil {
		return
	}
	var own *owner
	code := "local_peer_refused"
	if err := raw.Control(func(fd uintptr) { own, code = admit(int(fd), s.kernel) }); err != nil {
		return
	}
	if own != nil {
		defer own.Close()
	}
	// No untrusted queued byte is read or acknowledged before the preface.
	if !s.empty(c) {
		return
	}
	if code != "" {
		_ = localframe.Write(c, localframe.Record{Type: "refused", Code: code})
		return
	}
	s.mu.Lock()
	existing := s.owners[own.pid]
	if existing == nil {
		s.owners[own.pid] = own
	}
	s.mu.Unlock()
	if existing != nil {
		_ = localframe.Write(c, localframe.Record{Type: "refused", Code: "local_capacity"})
		return
	}
	defer func() { s.mu.Lock(); delete(s.owners, own.pid); s.mu.Unlock() }()
	stop := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		ticker := time.NewTicker(50 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				if own.Alive() != nil {
					_ = c.Close()
					return
				}
			}
		}
	}()
	defer func() { close(stop); <-stopped }()
	if own.Check() != nil || !s.empty(c) {
		return
	}
	if localframe.Write(c, localframe.Record{Type: "ready", Version: 1}) != nil {
		return
	}
	for {
		_ = c.SetDeadline(time.Now().Add(10 * time.Minute))
		request, err := localframe.Next(c, localframe.Expect{UID: own.uid, PID: int32(own.pid)}, own.Alive)
		if err != nil || request.Type != "request" || own.Check() != nil {
			return
		}
		// Keep local reads behind the shared lifecycle boundary. No helper, package
		// manager or subprocess runs for a request: module.read answers from the
		// portal's last completed helper reads, which the portal takes outside requests.
		lock, err := hostops.OpenLifecycle(LifecycleDirectory, hostops.RolePortal)
		if err != nil {
			if s.reply(c, own, refusal(request.Op, 503, "consumer_unavailable")) != nil {
				return
			}
			continue
		}
		if err := lock.TryShared(); err != nil {
			_ = lock.Close()
			if s.reply(c, own, refusal(request.Op, 503, "consumer_unavailable")) != nil {
				return
			}
			continue
		}
		if own.Check() != nil {
			_ = lock.Close()
			return
		}
		response := s.handle(request)
		_ = lock.Close()
		if s.reply(c, own, response) != nil {
			return
		}
	}
}
func (s *Server) reply(c *net.UnixConn, own *owner, r localframe.Record) error {
	if own.Check() != nil || !s.empty(c) {
		return errors.New("protocol_violation")
	}
	_ = c.SetWriteDeadline(time.Now().Add(5 * time.Second))
	return localframe.Write(c, r)
}

type operationRequest struct {
	OperationID string `json:"operation_id"`
	Surface     string `json:"surface"`
}
type taskRequest struct {
	Module  string          `json:"module"`
	Verb    string          `json:"verb"`
	Surface string          `json:"surface"`
	Inputs  json.RawMessage `json:"inputs,omitempty"`
}

func (s *Server) handle(request localframe.Record) localframe.Record {
	if request.Type != "request" {
		return refusal(request.Op, 422, "input_refused")
	}
	switch request.Op {
	case "task.list":
		var req struct {
			Surface string `json:"surface"`
		}
		if hostops.DecodeClosed(request.Body, &req) != nil || !localSurface(req.Surface) {
			return refusal(request.Op, 422, "input_refused")
		}
		return response(request.Op, 200, struct {
			Tasks []hostops.Descriptor `json:"tasks"`
		}{s.catalog.Descriptors()})

	case "handoff.register":
		var empty struct{}
		if hostops.DecodeClosed(request.Body, &empty) != nil {
			return refusal(request.Op, 422, "input_refused")
		}
		// Process qualification and the product-side handoff issuer are not installed.
		return refusal(request.Op, 503, "pidfd_unproven")
	case "task.apply":
		var empty struct{}
		if hostops.DecodeClosed(request.Body, &empty) != nil {
			return refusal(request.Op, 422, "input_refused")
		}
		return refusal(request.Op, 503, "act_not_adopted")
	case "operation.get":
		var req operationRequest
		if hostops.DecodeClosed(request.Body, &req) != nil || !localSurface(req.Surface) {
			return refusal(request.Op, 422, "input_refused")
		}
		view, status, err := s.service.Operation(req.OperationID, req.Surface)
		if err != nil {
			if status == 422 {
				return refusal(request.Op, 422, "input_refused")
			}
			return refusal(request.Op, 503, "consumer_unavailable")
		}
		return response(request.Op, status, view)
	case "module.read":
		return s.moduleRead(request.Op, request.Body)
	case "task.describe", "task.plan":
		var req taskRequest
		if hostops.DecodeClosed(request.Body, &req) != nil || !localSurface(req.Surface) {
			return refusal(request.Op, 422, "input_refused")
		}
		descriptor, err := s.catalog.Describe(req.Module, req.Verb, req.Surface)
		if err != nil {
			return refusal(request.Op, 422, "input_refused")
		}
		if request.Op == "task.describe" {
			if len(req.Inputs) != 0 {
				return refusal(request.Op, 422, "input_refused")
			}
			return response(request.Op, 200, descriptor)
		}
		if descriptor.ValidateInput(req.Inputs) != nil {
			return refusal(request.Op, 422, "input_refused")
		}
		return response(request.Op, 200, hostops.Plan{Descriptor: descriptor, Inputs: req.Inputs, Permissions: (hostops.ReadOnlyAccess{}).Permissions(hostops.Record{})})
	default:
		return refusal(request.Op, 422, "input_refused")
	}
}
func localSurface(s string) bool { return s == "cli" || s == "tui" }
func refusal(op string, status int, code string) localframe.Record {
	return localframe.Record{Type: "error", Op: op, Status: status, Code: code}
}
func response(op string, status int, value any) localframe.Record {
	body, err := json.Marshal(value)
	if err != nil {
		return refusal(op, 503, "consumer_unavailable")
	}
	return localframe.Record{Type: "response", Op: op, Status: status, Body: body}
}
