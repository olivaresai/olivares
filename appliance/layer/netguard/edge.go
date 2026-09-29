// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package netguard

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"unicode/utf8"

	"github.com/olivaresai/olivares/appliance/layer/helpers/helperschema"
)

const MaxEdgeBytes = 16 * 1024
const GuardSocket = "/run/olivares-net-guard-api/guard.sock"

type EdgeRequest struct {
	CandidateDigest string        `json:"candidate_digest,omitempty"`
	Action          string        `json:"action"`
	OperationID     string        `json:"operation_id,omitempty"`
	Change          *Change       `json:"change,omitempty"`
	Confirmation    *Confirmation `json:"confirmation,omitempty"`
}
type EdgeResponse struct {
	Code             string   `json:"code"`
	Status           *Status  `json:"status,omitempty"`
	Token            string   `json:"token,omitempty"`
	OpenWindows      []Status `json:"open_windows,omitempty"`
	BootID           string   `json:"boot_id,omitempty"`
	ObservedBootTime int64    `json:"observed_boottime_ns,omitempty"`
}

func DecodeEdge(r io.Reader) (EdgeRequest, error) {
	var request EdgeRequest
	b, err := io.ReadAll(io.LimitReader(r, MaxEdgeBytes+1))
	if err != nil {
		return request, err
	}
	if len(b) > MaxEdgeBytes || !utf8.Valid(b) {
		return request, errors.New("network_input_refused")
	}
	decoder := json.NewDecoder(bytes.NewReader(b))
	decoder.UseNumber()
	if err := closedValue(decoder, reflect.TypeOf(request), 0); err != nil {
		return request, err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return request, errors.New("network_input_refused")
	}
	decoder = json.NewDecoder(bytes.NewReader(b))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		return request, errors.New("network_input_refused")
	}
	switch request.Action {
	case "status":
		if request.CandidateDigest != "" {
			return request, errors.New("network_input_refused")
		}
		if request.Change != nil || request.Confirmation != nil || request.OperationID != "" && !validID(request.OperationID) {
			return request, errors.New("network_input_refused")
		}
	case "apply":
		if request.Change == nil {
			return request, errors.New("network_input_refused")
		}
		digest, digestErr := request.Change.Digest()
		if digestErr != nil || request.CandidateDigest != digest {
			return request, errors.New("network_candidate_digest_changed")
		}
		if request.Change == nil || request.Confirmation != nil || request.OperationID != "" {
			return request, errors.New("network_input_refused")
		}
		if err := request.Change.Validate(); err != nil {
			return request, err
		}
	case "confirm":
		if request.CandidateDigest != "" {
			return request, errors.New("network_input_refused")
		}
		if request.Confirmation == nil || request.Change != nil || request.OperationID != "" {
			return request, errors.New("network_input_refused")
		}
	case "revert":
		if request.CandidateDigest != "" {
			return request, errors.New("network_input_refused")
		}
		if !validID(request.OperationID) || request.Change != nil || request.Confirmation != nil {
			return request, errors.New("network_input_refused")
		}
	default:
		return request, errors.New("network_input_refused")
	}
	return request, nil
}
func closedValue(d *json.Decoder, t reflect.Type, depth int) error {
	bad := func() error { return errors.New("network_input_refused") }
	if depth > 10 {
		return bad()
	}
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	token, err := d.Token()
	if err != nil || token == nil {
		return bad()
	}
	switch t.Kind() {
	case reflect.Struct:
		if token != json.Delim('{') {
			return bad()
		}
		fields := map[string]reflect.Type{}
		for i := 0; i < t.NumField(); i++ {
			field := t.Field(i)
			name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
			if field.IsExported() {
				fields[name] = field.Type
			}
		}
		seen := map[string]bool{}
		for d.More() {
			key, err := d.Token()
			name, ok := key.(string)
			field, known := fields[name]
			if err != nil || !ok || !known || seen[name] {
				return bad()
			}
			seen[name] = true
			if err := closedValue(d, field, depth+1); err != nil {
				return err
			}
		}
		end, err := d.Token()
		if err != nil || end != json.Delim('}') {
			return bad()
		}
	case reflect.Slice:
		if token != json.Delim('[') {
			return bad()
		}
		n := 0
		for d.More() {
			n++
			if n > 16 {
				return bad()
			}
			if err := closedValue(d, t.Elem(), depth+1); err != nil {
				return err
			}
		}
		end, err := d.Token()
		if err != nil || end != json.Delim(']') {
			return bad()
		}
	case reflect.String:
		value, ok := token.(string)
		if !ok || len(value) > 4096 {
			return bad()
		}
	case reflect.Bool:
		if _, ok := token.(bool); !ok {
			return bad()
		}
	case reflect.Uint32:
		if _, ok := token.(json.Number); !ok {
			return bad()
		}
	default:
		return bad()
	}
	return nil
}
func AdmitEdge(p helperschema.Peer, action string) error {
	if !p.Attested {
		return errors.New("network_peer_unattested")
	}
	portal := p.UID != 0 && p.Account == "olivares-portal" && p.Unit == "olivares-portal.service" && p.TTY == ""
	console := p.UID == 0 && p.Account == "root" && p.Unit == "olivares-repair-console.service" && p.TTY == "/dev/tty1"
	instance, exposure := strings.CutPrefix(p.Unit, "olivares-helper-system@")
	exposure = exposure && strings.HasSuffix(instance, ".service") && len(instance) > len(".service") && p.UID == 0 && p.Account == "root" && p.TTY == ""
	if portal || console || exposure && action == "status" {
		return nil
	}
	return errors.New("network_peer_not_admitted")
}

// Edge has no product authorization authority. Its production mutating composition
// remains closed until lifecycle, caller authorization and probe receipt consumers are
// supplied together. Engine remains the same library used by the guest fixture.
type Edge struct{ Engine *Engine }

func (e Edge) Handle(ctx context.Context, peer helperschema.Peer, request EdgeRequest) EdgeResponse {
	if err := AdmitEdge(peer, request.Action); err != nil {
		return EdgeResponse{Code: err.Error()}
	}
	if e.Engine == nil {
		return EdgeResponse{Code: "network_guard_unavailable"}
	}
	if request.Action != "status" {
		return EdgeResponse{Code: "network_composition_unavailable"}
	}
	if request.OperationID != "" {
		s, err := e.Engine.Status(request.OperationID)
		if err != nil {
			return EdgeResponse{Code: err.Error()}
		}
		return EdgeResponse{Code: "ok", Status: &s}
	}
	boot, now, err := e.Engine.config.Clock.Now()
	if err != nil {
		return EdgeResponse{Code: "network_status_unknown"}
	}
	response := EdgeResponse{Code: "ok", BootID: boot, ObservedBootTime: int64(now)}
	for _, status := range e.Engine.view.Load().(map[string]Status) {
		if status.State != StateConfirmed && status.State != StateRolledBack {
			response.OpenWindows = append(response.OpenWindows, status)
		}
	}
	if len(response.OpenWindows) > 4 {
		return EdgeResponse{Code: "network_status_busy"}
	}
	return response
}
