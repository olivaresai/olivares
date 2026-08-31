// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package modelprovider

import (
	"context"
	"fmt"
)

// EmbeddingsClient is a provider-neutral, model-backed text embedder over the
// OpenAI-compatible POST /v1/embeddings shape ({"model","input":[...]} ->
// {"data":[{"index","embedding":[...]}]}). It exists because Anthropic exposes NO
// first-party embeddings endpoint: the Messages API generates, it does not embed.
// The knowledge module's Embedder seam is explicitly the "Model-backed"
// seam — provider-neutral — so this targets the operator's configured embeddings
// provider. Voyage AI (Anthropic's documented embeddings recommendation) is
// OpenAI-compatible at this shape, as are OpenAI and most gateways.
//
// It is an egressing embedder by construction (it sends text to a hosted provider),
// so the composition-root adapter that wraps it MUST report AllowsEgress()=true; the
// knowledge module then refuses to ingest a local_only / residency-locked KB with it
// (the docs/SECURITY-HARDENING.md red line). It never logs the input text or the credential.
type EmbeddingsClient struct {
	client *InferenceClient
	model  string
	dim    int
}

// EmbeddingsConfig configures the embedder. Scheme defaults to AuthBearer (OpenAI /
// Voyage). Dim is the embedding dimension the chosen model produces (the operator
// declares it; the client validates every returned vector against it).
type EmbeddingsConfig struct {
	BaseURL string
	APIKey  string
	Model   string
	Dim     int
	Scheme  AuthScheme
	Doer    Doer
}

// NewEmbeddingsClient builds an embeddings client from cfg.
func NewEmbeddingsClient(cfg EmbeddingsConfig) *EmbeddingsClient {
	scheme := cfg.Scheme
	if scheme == "" {
		scheme = AuthBearer
	}
	return &EmbeddingsClient{
		client: NewInferenceClient(cfg.BaseURL, cfg.Doer, scheme, cfg.APIKey, nil),
		model:  cfg.Model,
		dim:    cfg.Dim,
	}
}

// Model returns the configured embedding model reference (recorded on the KB so a
// reader knows which embedder produced the vectors).
func (e *EmbeddingsClient) Model() string { return e.model }

// Dim returns the configured embedding dimension.
func (e *EmbeddingsClient) Dim() int { return e.dim }

// embeddingsResponse is the OpenAI/Voyage embeddings response shape.
type embeddingsResponse struct {
	Data []struct {
		Index     int       `json:"index"`
		Embedding []float64 `json:"embedding"`
	} `json:"data"`
	Model string `json:"model"`
}

// Embed returns one vector per input text, in input order. It fails (never returns a
// silent empty/short vector) if the provider returns the wrong count or a vector
// whose length disagrees with the configured Dim.
func (e *EmbeddingsClient) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if e.client == nil {
		return nil, fmt.Errorf("modelprovider: embeddings client not configured")
	}
	if len(texts) == 0 {
		return nil, nil
	}
	body := map[string]any{"model": e.model, "input": texts}
	var resp embeddingsResponse
	if err := e.client.PostJSON(ctx, "/v1/embeddings", body, &resp, nil); err != nil {
		return nil, err
	}
	if len(resp.Data) != len(texts) {
		return nil, fmt.Errorf("modelprovider: embeddings returned %d vectors for %d inputs", len(resp.Data), len(texts))
	}
	// Place each vector at its DECLARED index (the provider may return out of order),
	// validating index integrity: every index must be in range and unique. A count
	// match alone is not enough — a duplicate/gap (e.g. [0,0,2] for 3 inputs, from a
	// 1-based or partially-retried gateway) would otherwise silently misalign a chunk
	// to the wrong embedding. Fail closed rather than corrupt retrieval (ports.go: "in
	// input order", ARCHITECTURE.md).
	out := make([][]float32, len(texts))
	seen := make([]bool, len(texts))
	for _, d := range resp.Data {
		if d.Index < 0 || d.Index >= len(texts) {
			return nil, fmt.Errorf("modelprovider: embedding index %d out of range [0,%d) (model %s)", d.Index, len(texts), e.model)
		}
		if seen[d.Index] {
			return nil, fmt.Errorf("modelprovider: duplicate embedding index %d (model %s)", d.Index, e.model)
		}
		seen[d.Index] = true
		if e.dim > 0 && len(d.Embedding) != e.dim {
			return nil, fmt.Errorf("modelprovider: embedding dim %d != configured %d (model %s)", len(d.Embedding), e.dim, e.model)
		}
		v := make([]float32, len(d.Embedding))
		for k, f := range d.Embedding {
			v[k] = float32(f)
		}
		out[d.Index] = v
	}
	return out, nil
}
