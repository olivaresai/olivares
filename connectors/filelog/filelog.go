// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package filelog is the Olivares AI output connector that appends notifications,
// one record per line, to a local file or a standard stream. It is the concrete,
// tailable egress artifact behind the supported "drop a Splunk Universal Forwarder
// (or any file-tailing agent) next to the control plane and tail this file" posture
//: rather than implement Splunk's proprietary S2S protocol, the
// product writes the same SIEM wire formats it already produces to a file the
// operator's existing forwarder/agent monitors. It doubles as the docs/SECURITY-HARDENING.md
// immutable-external-copy (WORM) sink when the file lives on append-only/WORM
// storage.
//
// Every record is encoded by connectors/internal/siemfmt (CEF/LEEF/syslog/OTLP/
// OCSF/ASIM) or as a canonical one-line JSON object; this connector only owns the
// append + the newline framing (one record = one line, so a tailer reads record by
// record). Writes are serialized and optionally fsync'd for durability.
//
// Minimal data (docs/SECURITY-HARDENING.md): a Notification already carries only non-sensitive,
// displayable fields; this connector forwards what siemfmt encodes and adds no
// enrichment. It imports only the SDK and the Apache siemfmt helper, never the
// engine.
package filelog

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/siemfmt"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
	"github.com/olivaresai/olivares/sdk/siemwire"
)

// Name is the connector's globally unique dotted identifier.
const Name = "olivares.filelog"

// formatSet is this connector's slice of the sdk/siemwire format catalog: the
// notification-connector subset (json-first default, full dialect roster,
// otlp_envelope as the exact alias of otlp — since the catalog remap the two are
// one format EVERYWHERE, ledger export included). The private per-connector
// const block this replaced was one of six diverged hand copies; the accepted
// set, the default, the operator-facing list and the alias resolution all
// derive from the catalog now via siemfmt.ResolveFormat.
func formatSet() siemwire.FormatSet { return siemwire.NotificationConnectorFormats() }

// Output is the file-log output connector. Open opens (or creates) the append
// target; Notify formats and appends one line; Close closes the file (never a
// shared stdout/stderr). A mutex serializes concurrent writes.
type Output struct {
	path     string
	format   siemwire.FormatToken // canonical encoder key, resolved at Open
	hostname string
	device   siemfmt.Device
	fsync    bool

	mu     sync.Mutex
	w      *os.File
	closer bool // true when w must be closed in Close (a real file, not std stream)
}

// Compile-time proof that Output satisfies the output-connector contract.
var _ sdk.OutputConnector = (*Output)(nil)

// New returns a file-log output connector with default configuration (JSON lines).
func New() *Output { return &Output{format: siemwire.Canonical(formatSet().Default())} }

// Descriptor returns the connector's self-description and declared configuration.
func (o *Output) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:        Name,
		Version:     "0.1.0",
		APIVersion:  sdk.APIVersion,
		Type:        sdk.TypeOutput,
		Title:       "File log",
		Description: "Appends notifications one record per line to a file or stream (stdout/stderr) for a file-tailing forwarder (Splunk UF posture) or a WORM external copy. Formats: " + strings.ReplaceAll(formatSet().List(), "|", "/") + ".",
		ConfigFields: []sdk.ConfigField{
			{Key: "path", Type: sdk.FieldString, Required: true, Description: "Append target: a file path, or 'stdout'/'stderr'/'-' for a standard stream."},
			{Key: "format", Type: sdk.FieldString, Default: string(formatSet().Default()), Description: "Per-line format: " + strings.ReplaceAll(formatSet().List(), "|", " | ") + ". otlp_envelope is an exact alias of otlp (identical bytes)."},
			{Key: "hostname", Type: sdk.FieldString, Description: "Syslog HOSTNAME for the syslog format."},
			{Key: "fsync", Type: sdk.FieldBool, Default: "false", Description: "Flush each record to disk (durability for a WORM copy; slower)."},
			{Key: "vendor", Type: sdk.FieldString, Description: "siemfmt device vendor override."},
			{Key: "product", Type: sdk.FieldString, Description: "siemfmt device product override."},
			{Key: "version", Type: sdk.FieldString, Description: "siemfmt device version override."},
		},
	}
}

// Open validates configuration and opens the append target. A file is opened
// O_APPEND|O_CREATE with 0640 permissions; 'stdout'/'stderr'/'-' wire the standard
// streams (never closed). A missing path or unwritable file is reported here.
func (o *Output) Open(_ context.Context, cfg sdk.Config) error {
	tok, err := siemfmt.ResolveFormat(formatSet(), cfg.Get("format"))
	if err != nil {
		return fmt.Errorf("filelog: %w", err)
	}
	o.format = tok

	o.path = strings.TrimSpace(cfg.Get("path"))
	if o.path == "" {
		return fmt.Errorf("filelog: path is required (a file path, or stdout/stderr/-)")
	}
	o.hostname = strings.TrimSpace(cfg.Get("hostname"))
	o.fsync = cfg.GetBool("fsync", false)

	o.device = siemfmt.DefaultDevice()
	if v := cfg.Get("vendor"); v != "" {
		o.device.Vendor = v
	}
	if v := cfg.Get("product"); v != "" {
		o.device.Product = v
	}
	if v := cfg.Get("version"); v != "" {
		o.device.Version = v
	}

	switch strings.ToLower(o.path) {
	case "stdout", "-":
		o.w, o.closer = os.Stdout, false
	case "stderr":
		o.w, o.closer = os.Stderr, false
	default:
		f, err := os.OpenFile(o.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o640)
		if err != nil {
			return fmt.Errorf("filelog: open append target: %w", err)
		}
		o.w, o.closer = f, true
	}
	return nil
}

// Notify formats n and appends it as one line. The fsync option flushes the record
// to stable storage before returning.
func (o *Output) Notify(_ context.Context, n sdk.Notification) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.w == nil {
		return fmt.Errorf("filelog: connector not opened")
	}
	line, err := o.encode(n)
	if err != nil {
		return err
	}
	if _, err := o.w.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("filelog: append: %w", err)
	}
	if o.fsync && o.closer {
		if err := o.w.Sync(); err != nil {
			return fmt.Errorf("filelog: sync: %w", err)
		}
	}
	return nil
}

// Close closes the file when it is a real file (not a shared standard stream).
func (o *Output) Close(context.Context) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.w != nil && o.closer {
		err := o.w.Close()
		o.w = nil
		return err
	}
	o.w = nil
	return nil
}

// encode renders n as one line in the configured format. A text format (CEF/LEEF/
// syslog) is its single line; OTLP/OCSF/ASIM are single-line JSON documents; json
// is the canonical one-line notification object. siemwire collapses any CR/LF in a
// text record, so one record always stays one line. JSON is an EXPLICIT case and
// an unrecognized stored value is an error: the old default-to-JSON branch could
// silently mislabel output if internal state was ever corrupted (Open validates
// the public path, but deny-closed beats trusting that forever — the sibling siem
// connector already erred here and the four now agree).
func (o *Output) encode(n sdk.Notification) ([]byte, error) {
	switch o.format {
	case siemwire.TokenCEF:
		return []byte(siemfmt.CEF(o.device, n)), nil
	case siemwire.TokenLEEF:
		return []byte(siemfmt.LEEF(o.device, n)), nil
	case siemwire.TokenSyslog:
		return []byte(siemfmt.Syslog5424(o.device, siemfmt.SyslogOptions{Hostname: o.hostname}, n)), nil
	case siemwire.TokenOTLP:
		return siemfmt.OTLPLogJSON(o.device, n)
	case siemwire.TokenOCSF:
		return siemfmt.OCSF(o.device, n)
	case siemwire.TokenASIM:
		return siemfmt.ASIMAgentEvent(o.device, n)
	case siemwire.TokenJSON:
		return json.Marshal(jsonLine(n))
	default:
		return nil, fmt.Errorf("filelog: unrecognized stored format %q", o.format)
	}
}

// notificationView is the canonical one-line JSON shape (the same flat, non-
// sensitive projection the webhook/SIEM connectors ship). omitempty keeps empty
// optional fields out so a record stays compact.
type notificationView struct {
	Type     string            `json:"type,omitempty"`
	Title    string            `json:"title,omitempty"`
	Body     string            `json:"body,omitempty"`
	Severity string            `json:"severity,omitempty"`
	Tenant   string            `json:"tenant,omitempty"`
	Fields   map[string]string `json:"fields,omitempty"`
	Time     string            `json:"time,omitempty"`
}

func jsonLine(n sdk.Notification) notificationView {
	v := notificationView{
		Type:     n.Type,
		Title:    n.Title,
		Body:     n.Body,
		Severity: severityString(n.Severity),
		Tenant:   n.Tenant,
		Fields:   n.Fields,
	}
	if !n.Time.IsZero() {
		v.Time = n.Time.UTC().Format(time.RFC3339)
	}
	return v
}

func severityString(s model.Severity) string {
	switch s {
	case model.SeverityInfo:
		return "info"
	case model.SeverityLow:
		return "low"
	case model.SeverityMedium:
		return "medium"
	case model.SeverityHigh:
		return "high"
	case model.SeverityCritical:
		return "critical"
	default:
		return ""
	}
}
