// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package bedrock

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/olivaresai/olivares/sdk"
)

func TestDescriptor(t *testing.T) {
	d := New().Descriptor()
	if d.Name != Name {
		t.Errorf("name = %q, want %q", d.Name, Name)
	}
	if d.Type != sdk.TypeSource {
		t.Errorf("type = %q, want source", d.Type)
	}
	if d.APIVersion != sdk.APIVersion {
		t.Errorf("api version = %q, want %q", d.APIVersion, sdk.APIVersion)
	}
	if len(d.ConfigFields) == 0 {
		t.Error("descriptor must declare config fields")
	}
}

// TestOpen_PathOnlyNeedsNoCreds proves a usage_log_path-only configuration is valid
// without credentials (it is local I/O).
func TestOpen_PathOnlyNeedsNoCreds(t *testing.T) {
	s := New()
	if err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{
		cfgUsageLogPath: t.TempDir(),
	}}); err != nil {
		t.Fatalf("path-only Open must succeed without creds: %v", err)
	}
}

// TestOpen_SignedSourcesRequireCreds proves each source that makes a live signed AWS call
// requires credentials, while a fully-disabled connector is a valid no-op.
func TestOpen_SignedSourcesRequireCreds(t *testing.T) {
	cases := map[string]map[string]string{
		"cloudwatch usage": {cfgUsageLogGroup: "/aws/bedrock/modelinvocations"},
		"cost":             {cfgEnableCost: "true"},
		"guardrails":       {cfgEnableGuardrails: "true"},
	}
	for name, settings := range cases {
		if err := New().Open(context.Background(), sdk.Config{Settings: settings}); err == nil {
			t.Errorf("%s without creds must fail Open", name)
		}
	}

	// Fully disabled ⇒ valid no-op without creds.
	s := New()
	if err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{}}); err != nil {
		t.Fatalf("fully-disabled Open must succeed: %v", err)
	}
	sink := &fakeSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather on a no-op connector: %v", err)
	}
	if len(sink.obs) != 0 {
		t.Fatalf("a no-op connector must emit nothing, got %+v", sink.obs)
	}
}

// TestUsageFiles_ReadFailure proves a usage_log_path that cannot be read yields one
// health finding (SubjectKind=bedrock.usage), never a fabricated usage sample.
func TestUsageFiles_ReadFailure(t *testing.T) {
	s := New()
	if err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{
		cfgUsageLogPath: filepath.Join(t.TempDir(), "does-not-exist"),
	}}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	sink := &fakeSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	if len(sink.costs()) != 0 {
		t.Fatal("a file read failure must not fabricate a usage sample")
	}
	fs := sink.findings()
	if len(fs) != 1 || fs[0].Kind != "health" || fs[0].SubjectKind != subjectUsage {
		t.Fatalf("expected one usage health finding, got %+v", fs)
	}
}

// TestGather_SourceIndependence proves a failing source emits a health finding and the
// other sources still run: usage files (working) emit samples even though guardrails
// (pointed at a 403 endpoint) fails.
func TestGather_SourceIndependence(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "log.json"),
		invocationLogJSON("amazon.titan-text-v1", "arn:aws:sts::123456789012:assumed-role/AppRole/sess", 10, 5))

	deny := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer deny.Close()

	s := New()
	settings := map[string]string{
		cfgUsageLogPath:     dir,
		cfgEnableGuardrails: "true",
		cfgBedrockEndpoint:  deny.URL,
		cfgRegion:           "us-east-1",
		cfgAccountID:        "123456789012",
	}
	for k, v := range testCreds {
		settings[k] = v
	}
	if err := s.Open(context.Background(), sdk.Config{Settings: settings}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	sink := &fakeSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}

	if len(sink.costs()) != 1 {
		t.Fatalf("usage source must still emit despite the guardrails failure, got %d samples", len(sink.costs()))
	}
	gh, ok := findingBySubject(sink.findings(), subjectGuardrails)
	if !ok || gh.Kind != "health" {
		t.Fatalf("a failing guardrails source must emit a health finding, got %+v", sink.findings())
	}
}
