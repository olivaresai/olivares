// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/olivaresai/olivares/core/api"
	obstrace "github.com/olivaresai/olivares/core/observability/trace"
	"github.com/spf13/cobra"
)

func newTracingCmd() *cobra.Command {
	flags := &authClientFlags{}
	root := &cobra.Command{Use: "tracing", Short: "Read or change saved tracing settings without restarting the engine", Long: "Manage the same tracing settings as the console. Published environment inputs override saved choices and are listed in the result.", Example: "  olivares config tracing get\n  olivares config tracing set --endpoint https://collector:4318 --protocol http/protobuf --enabled"}
	flags.addPersistent(root)
	client := bootstrapClient{flags: flags, surface: "tracing"}
	call := func(cmd *cobra.Command, method string, body any) error {
		raw, err := client.expect(cmd, method, "/v1/system/tracing", body, http.StatusOK)
		if err != nil {
			return err
		}
		var state api.TracingStatus
		if err = decodeBootstrapJSON("tracing", raw, &state); err != nil {
			return err
		}
		return renderOut(cmd, func(out io.Writer) error {
			_, err := fmt.Fprintf(out, "Enabled: %t\nCollector: %s\nProtocol: %s\nSample ratio: %g\nEnvironment overrides: %s\n", state.Effective.Enabled, termSafe(state.Effective.Endpoint), state.Effective.Protocol, state.Effective.SampleRatio, strings.Join(state.Overrides, ", "))
			return err
		}, state)
	}
	root.AddCommand(&cobra.Command{Use: "get", Short: "Read saved and effective tracing choices and environment overrides", Long: "Read tracing settings from the running engine, including saved choices, effective settings, and the environment inputs that override them. The default text output summarizes effective settings; use -o json to inspect saved choices alongside them.", Example: "  olivares config tracing get -o json", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error { return call(cmd, http.MethodGet, nil) }})
	choice := obstrace.DefaultSettings()
	protocol := string(choice.Protocol)
	set := &cobra.Command{Use: "set", Short: "Save tracing choices and apply them now", Long: "Set only the named choices, keeping other saved values. The collector belongs to you; tracing is off by default. Plaintext requires --collector-insecure and a trusted network.", Example: "  olivares config tracing set --enabled=false", Args: cobra.NoArgs}
	set.Flags().BoolVar(&choice.Enabled, "enabled", false, "record and export tracing")
	set.Flags().StringVar(&choice.Endpoint, "endpoint", "", "OTLP collector host or HTTP(S) URL (no credentials)")
	set.Flags().StringVar(&protocol, "protocol", protocol, "grpc or http/protobuf")
	set.Flags().BoolVar(&choice.Insecure, "collector-insecure", false, "allow a plaintext collector on a trusted network")
	set.Flags().Float64Var(&choice.SampleRatio, "sample-ratio", 1, "root trace sampling ratio from 0 to 1")
	set.Flags().StringVar(&choice.ServiceName, "service-name", choice.ServiceName, "service name reported to the collector")
	set.Flags().BoolVar(&choice.GenAICompat, "genai-compat", false, "also emit legacy GenAI attributes")
	set.RunE = func(cmd *cobra.Command, _ []string) error {
		changed := false
		for _, name := range []string{"enabled", "endpoint", "protocol", "collector-insecure", "sample-ratio", "service-name", "genai-compat"} {
			changed = changed || cmd.Flags().Changed(name)
		}
		if !changed {
			return fmt.Errorf("choose at least one tracing setting")
		}
		raw, err := client.expect(cmd, http.MethodGet, "/v1/system/tracing", nil, http.StatusOK)
		if err != nil {
			return err
		}
		var current api.TracingStatus
		if err = decodeBootstrapJSON("tracing", raw, &current); err != nil {
			return err
		}
		want := current.Settings
		if cmd.Flags().Changed("enabled") {
			want.Enabled = choice.Enabled
		}
		if cmd.Flags().Changed("endpoint") {
			want.Endpoint = choice.Endpoint
		}
		if cmd.Flags().Changed("protocol") {
			want.Protocol = obstrace.Protocol(protocol)
		}
		if cmd.Flags().Changed("collector-insecure") {
			want.Insecure = choice.Insecure
		}
		if cmd.Flags().Changed("sample-ratio") {
			want.SampleRatio = choice.SampleRatio
		}
		if cmd.Flags().Changed("service-name") {
			want.ServiceName = choice.ServiceName
		}
		if cmd.Flags().Changed("genai-compat") {
			want.GenAICompat = choice.GenAICompat
		}
		if err = want.Validate(); err != nil {
			return err
		}
		return call(cmd, http.MethodPut, want)
	}
	root.AddCommand(set)
	return root
}

func doctorTracingOverridesCheck() doctorCheck {
	_, overrides := obstrace.DefaultSettings().Resolve(version)
	check := doctorCheck{Name: "tracing-overrides", Status: "pass", Detail: "No tracing environment overrides in this process."}
	if len(overrides) != 0 {
		check.Status = "warn"
		check.Detail = "Tracing environment overrides in this process: " + strings.Join(overrides, ", ")
		check.Remediation = "Remove these overrides from the engine environment to use the saved tracing choices."
	}
	return check
}
