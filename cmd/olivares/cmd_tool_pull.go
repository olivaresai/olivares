// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

type toolModelPull struct {
	ID        string `json:"id"`
	Model     string `json:"model"`
	State     string `json:"state"`
	Status    string `json:"status,omitempty"`
	Completed int64  `json:"completed"`
	Total     int64  `json:"total"`
	Error     string `json:"error,omitempty"`
}

func newToolPullCmd() *cobra.Command {
	var cfg agentClientConfig
	cmd := &cobra.Command{
		Use:   "pull <model>",
		Short: "Download an Ollama or public Hugging Face GGUF model on the engine's host",
		Long: "Download a model into the engine's running Ollama and follow its progress.\n" +
			"Use an Ollama model name or hf.co/<user>/<repo>[:<quantization-or-filename>].\n" +
			"Start Ollama in AI tools first. Ctrl-C stops waiting; the engine keeps downloading.",
		Example: "  olivares tool pull qwen2.5:0.5b\n" +
			"  olivares tool pull hf.co/bartowski/SmolLM2-135M-Instruct-GGUF:Q4_K_M",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(args[0]) == "" {
				return sentence(exitcode.Usage, "Name an Ollama model or a public Hugging Face GGUF repository.")
			}
			if err := cfg.resolve(); err != nil {
				return err
			}
			ctx, stop := toolLoginSignals(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			status, body, err := cfg.do(ctx, "POST", agentToolsPath+"/ollama/pulls",
				map[string]string{"model": strings.TrimSpace(args[0])}, http.StatusAccepted, http.StatusNotFound, http.StatusForbidden, http.StatusConflict, http.StatusBadRequest)
			if err != nil {
				return err
			}
			if status != http.StatusAccepted {
				return toolHTTPErr(status, body)
			}
			pull, err := cfg.waitModelPull(ctx, body, cmd.ErrOrStderr())
			if errors.Is(err, context.Canceled) {
				return sentence(exitcode.Err, "Stopped waiting. The model download continues on the engine.")
			}
			if err != nil {
				return err
			}
			return renderOut(cmd, func(out io.Writer) error {
				_, err := fmt.Fprintf(out, "Downloaded %s.\n", pull.Model)
				return err
			}, pull)
		},
	}
	cfg.addFlags(cmd)
	return cmd
}

func (c *agentClientConfig) waitModelPull(ctx context.Context, body []byte, progress io.Writer) (toolModelPull, error) {
	var pull toolModelPull
	last := ""
	for {
		// Decode into a fresh value: a later response can omit status or error.
		pull = toolModelPull{}
		if err := json.Unmarshal(body, &pull); err != nil {
			return pull, sentence(exitcode.Err, "The engine returned an invalid model download response.")
		}
		switch pull.State {
		case "succeeded":
			return pull, nil
		case "failed":
			if pull.Error == "" {
				pull.Error = "Ollama did not finish the download."
			}
			return pull, sentence(exitcode.Err, "%s was not downloaded. %s", pull.Model, pull.Error)
		case "running":
			if pull.ID == "" {
				return pull, sentence(exitcode.Err, "The engine returned a model download without an ID.")
			}
		default:
			return pull, sentence(exitcode.Err, "The engine returned an unknown model download state.")
		}
		line := pull.Status
		if pull.Total > 0 {
			line = fmt.Sprintf("%s · %d%%", pull.Status, int(100*float64(pull.Completed)/float64(pull.Total)))
		}
		if line != "" && line != last {
			fmt.Fprintln(progress, line)
			last = line
		}
		select {
		case <-ctx.Done():
			return pull, ctx.Err()
		case <-time.After(time.Second):
		}
		status, next, err := c.do(ctx, "GET", agentToolsPath+"/ollama/pulls/"+url.PathEscape(pull.ID), nil, http.StatusOK, http.StatusNotFound, http.StatusForbidden)
		if err != nil {
			return pull, err
		}
		if status != http.StatusOK {
			return pull, httpErr(status, next)
		}
		body = next
	}
}
