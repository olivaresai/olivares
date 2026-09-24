// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"runtime/debug"
)

// recordSchema names the record format supervisor.py reads.
const recordSchema = "cedar-measure/record/v1"

// envInfo records the build and runtime pins of this process.
type envInfo struct {
	GoVersion  string `json:"go_version"`
	GOOS       string `json:"goos"`
	GOARCH     string `json:"goarch"`
	GOMAXPROCS int    `json:"gomaxprocs"`
	NumCPU     int    `json:"num_cpu"`
	GODEBUG    string `json:"godebug"`
	GOGC       string `json:"gogc"`
	GOMEMLIMIT string `json:"gomemlimit"`
	CedarGo    string `json:"cedar_go"`
	CedarGoSum string `json:"cedar_go_sum"`
	XExp       string `json:"x_exp"`
}

func currentEnv() envInfo {
	e := envInfo{
		GoVersion:  runtime.Version(),
		GOOS:       runtime.GOOS,
		GOARCH:     runtime.GOARCH,
		GOMAXPROCS: runtime.GOMAXPROCS(0),
		NumCPU:     runtime.NumCPU(),
		GODEBUG:    os.Getenv("GODEBUG"),
		GOGC:       os.Getenv("GOGC"),
		GOMEMLIMIT: os.Getenv("GOMEMLIMIT"),
	}
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, d := range info.Deps {
			switch d.Path {
			case "github.com/cedar-policy/cedar-go":
				e.CedarGo, e.CedarGoSum = d.Version, d.Sum
			case "golang.org/x/exp":
				e.XExp = d.Version
			}
		}
	}
	return e
}

// result is the semantic outcome of the operation, read after every
// measurement. The supervisor compares it with the matrix independently.
type result struct {
	Ran           bool     `json:"ran"`
	Steps         int      `json:"steps,omitempty"`
	Depth         int      `json:"depth,omitempty"`
	TypeLen       int      `json:"type_len,omitempty"`
	Annotations   int      `json:"annotations,omitempty"`
	Nodes         int      `json:"nodes,omitempty"`
	Compiled      bool     `json:"compiled,omitempty"`
	Reasons       []string `json:"reasons,omitempty"`
	Errors        int      `json:"errors"`
	Discriminates bool     `json:"discriminates,omitempty"`
}

// record is the single line a process prints.
type record struct {
	Schema      string       `json:"schema"`
	Mode        string       `json:"mode"`
	Token       string       `json:"token"`
	ID          string       `json:"id,omitempty"`
	Cell        string       `json:"cell,omitempty"`
	Op          string       `json:"op,omitempty"`
	Point       int          `json:"point,omitempty"`
	Control     string       `json:"control"`
	InputBytes  int          `json:"input_bytes,omitempty"`
	InputSHA256 string       `json:"input_sha256,omitempty"`
	SetupNS     int64        `json:"setup_ns,omitempty"`
	Measurement *measurement `json:"measurement,omitempty"`
	Result      *result      `json:"result,omitempty"`
	Detail      any          `json:"detail,omitempty"`
	Env         envInfo      `json:"env"`
	Outcome     string       `json:"outcome"`
	Reason      string       `json:"reason,omitempty"`
}

// phase writes a fixed phase code to standard error, always outside the
// measured interval, so the supervisor can tell where a child stopped when no
// record arrives.
func phase(name string) {
	fmt.Fprintln(os.Stderr, "cedar-measure phase: "+name)
}

// emit prints r with the outcome of code and returns code. Reasons are fixed
// codes; no policy text is ever printed.
func emit(r *record, code int, reason string) int {
	phase("emit")
	switch code {
	case exitObserved:
		r.Outcome = "observed"
	case exitMismatch:
		r.Outcome = "mismatch"
	default:
		r.Outcome = "inability"
		code = exitInability
	}
	r.Reason = reason
	b, err := json.Marshal(r)
	if err != nil {
		fmt.Fprintln(os.Stderr, "cedar-measure: the record could not be encoded")
		return exitInability
	}
	b = append(b, '\n')
	if _, err := os.Stdout.Write(b); err != nil {
		return exitInability
	}
	return code
}

// failure is a setup or check failure with a fixed reason code.
type failure struct {
	code   int
	reason string
}

func mismatch(reason string) *failure  { return &failure{code: exitMismatch, reason: reason} }
func inability(reason string) *failure { return &failure{code: exitInability, reason: reason} }
