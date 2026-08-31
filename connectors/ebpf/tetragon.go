// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package ebpf

import (
	"encoding/json"
	"time"
)

// This file models the subset of Tetragon's GetEventsResponse JSON that the
// backstop consumes. The shape mirrors Tetragon's protojson encoding with
// original (snake_case) field names — the format emitted by `tetra getevents -o
// json` and Tetragon's file/FIFO export. Decoding is deliberately tolerant:
// unknown fields are ignored (so a Tetragon version bump that adds fields does not
// break ingestion), optional fields decode to their zero value, and an event is
// classified by the SHAPE of its kprobe arguments rather than by a brittle
// function-name match (see ebpf.go). Minimum Tetragon: v1.0.

// tetragonEnvelope is one line of the Tetragon JSON stream: a top-level oneof of
// process_exec / process_exit / process_kprobe, plus the event time and node.
type tetragonEnvelope struct {
	Time          string          `json:"time"`
	NodeName      string          `json:"node_name"`
	ProcessExec   *tetragonExec   `json:"process_exec"`
	ProcessExit   *tetragonExit   `json:"process_exit"`
	ProcessKprobe *tetragonKprobe `json:"process_kprobe"`
}

// tetragonExec is a process_exec event (a process started).
type tetragonExec struct {
	Process *tetragonProcess `json:"process"`
	Parent  *tetragonProcess `json:"parent"`
}

// tetragonExit is a process_exit event (a process ended).
type tetragonExit struct {
	Process *tetragonProcess `json:"process"`
	Parent  *tetragonProcess `json:"parent"`
	Time    string           `json:"time"`
}

// tetragonKprobe is a process_kprobe event: a kernel hook fired in the context of
// a process. function_name names the hook; args carries its typed arguments.
type tetragonKprobe struct {
	Process      *tetragonProcess `json:"process"`
	Parent       *tetragonProcess `json:"parent"`
	FunctionName string           `json:"function_name"`
	Args         []tetragonArg    `json:"args"`
	Action       string           `json:"action"`
}

// tetragonProcess is the process context Tetragon embeds in every event: the
// stable exec_id, identity (pid/uid), the executable and its arguments, the
// container/pod, and the parent link for ancestry. Only the fields this connector
// reads are modeled.
type tetragonProcess struct {
	ExecID       string       `json:"exec_id"`
	Pid          uint32       `json:"pid"`
	UID          uint32       `json:"uid"`
	Cwd          string       `json:"cwd"`
	Binary       string       `json:"binary"`
	Arguments    string       `json:"arguments"`
	Flags        string       `json:"flags"`
	StartTime    string       `json:"start_time"`
	Docker       string       `json:"docker"`
	ParentExecID string       `json:"parent_exec_id"`
	Pod          *tetragonPod `json:"pod"`
}

// tetragonPod is the Kubernetes context of a process when it runs in a pod.
type tetragonPod struct {
	Namespace string             `json:"namespace"`
	Name      string             `json:"name"`
	Container *tetragonContainer `json:"container"`
}

// tetragonContainer is the container a pod process runs in.
type tetragonContainer struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// tetragonArg is one element of a kprobe's args array. It is a oneof: exactly one
// of the typed fields is set. Only the argument types this connector maps are
// modeled; any other (size_arg, bytes_arg, …) decodes to all-nil and is skipped.
type tetragonArg struct {
	FileArg   *tetragonFileArg `json:"file_arg"`
	IntArg    *int             `json:"int_arg"`
	SockArg   *tetragonSockArg `json:"sock_arg"`
	SkbArg    *tetragonSkbArg  `json:"skb_arg"`
	StringArg *string          `json:"string_arg"`
}

// tetragonFileArg is a struct-file argument (e.g. the file of
// security_file_permission): its path and the access flags/permission.
type tetragonFileArg struct {
	Path       string `json:"path"`
	Flags      string `json:"flags"`
	Permission string `json:"permission"`
}

// tetragonSockArg is a struct-sock argument (e.g. the socket of tcp_connect): the
// connection 5-tuple. Ports are host-order integers in Tetragon's JSON.
type tetragonSockArg struct {
	Family   string `json:"family"`
	Type     string `json:"type"`
	Protocol string `json:"protocol"`
	Saddr    string `json:"saddr"`
	Daddr    string `json:"daddr"`
	Sport    uint32 `json:"sport"`
	Dport    uint32 `json:"dport"`
	State    string `json:"state"`
}

// tetragonSkbArg is a struct-sk_buff argument, an alternative carrier of the
// network 5-tuple for kprobes that take an skb instead of a sock.
type tetragonSkbArg struct {
	Saddr string `json:"saddr"`
	Daddr string `json:"daddr"`
	Sport uint32 `json:"sport"`
	Dport uint32 `json:"dport"`
	Proto string `json:"proto"`
}

// parseEnvelope decodes one JSON line into a tetragonEnvelope. Unknown fields are
// ignored (tolerant decoding); a malformed line returns an error the caller logs
// and skips, never fatal to the stream.
func parseEnvelope(line []byte) (tetragonEnvelope, error) {
	var env tetragonEnvelope
	if err := json.Unmarshal(line, &env); err != nil {
		return tetragonEnvelope{}, err
	}
	return env, nil
}

// eventTime returns the event's timestamp, preferring the top-level time, then a
// process_exit's own time, and falling back to now() when Tetragon supplied none
// (so an edge always carries a usable ObservedAt, the de-duplication key).
func (e tetragonEnvelope) eventTime(now func() time.Time) time.Time {
	if t, ok := parseTetragonTime(e.Time); ok {
		return t
	}
	if e.ProcessExit != nil {
		if t, ok := parseTetragonTime(e.ProcessExit.Time); ok {
			return t
		}
	}
	return now()
}

// parseTetragonTime parses an RFC3339 (nano) timestamp as emitted by Tetragon's
// protojson, returning false for an empty or unparseable value.
func parseTetragonTime(s string) (time.Time, bool) {
	if s == "" {
		return time.Time{}, false
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t.UTC(), true
	}
	return time.Time{}, false
}

// firstFileArg returns the first file argument in args, or nil. Classifying by
// argument shape (not function name) tolerates kprobe naming differences across
// policies/versions.
func firstFileArg(args []tetragonArg) *tetragonFileArg {
	for i := range args {
		if args[i].FileArg != nil {
			return args[i].FileArg
		}
	}
	return nil
}

// firstIntArg returns the first int argument in args and whether one was present
// (so a missing mask is distinguishable from a zero mask).
func firstIntArg(args []tetragonArg) (int, bool) {
	for i := range args {
		if args[i].IntArg != nil {
			return *args[i].IntArg, true
		}
	}
	return 0, false
}

// firstStringArg returns the first string argument in args (used as the optional
// SNI label when a TLS-SNI tracing policy provides one), or "".
func firstStringArg(args []tetragonArg) string {
	for i := range args {
		if args[i].StringArg != nil {
			return *args[i].StringArg
		}
	}
	return ""
}
