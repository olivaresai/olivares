// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package local

// JSON wire shapes for the local inference servers. Only the fields the connector
// needs (model identifiers) are mapped; the rest of each payload is ignored.

// ollamaTagsResponse is GET {ollama}/api/tags — the installed model list.
type ollamaTagsResponse struct {
	Models []ollamaModel `json:"models"`
}

// ollamaPSResponse is GET {ollama}/api/ps — the models Ollama has LOADED right now,
// which is a different question from /api/tags. Tags answers "what could run"; this
// answers "what is resident", and only this one carries the VRAM split and the
// unload deadline. https://docs.ollama.com/api/ps
type ollamaPSResponse struct {
	Models []ollamaLoadedModel `json:"models"`
}

// ollamaLoadedModel is one RESIDENT model. SizeVRAM is the part on the GPU: when it
// is zero the model is resident on the CPU, and when it is below Size the model is
// SPLIT across both — the case an operator most wants to see, because it is the one
// that silently costs latency.
type ollamaLoadedModel struct {
	Name      string `json:"name"`
	Model     string `json:"model"`
	Size      int64  `json:"size"`
	SizeVRAM  int64  `json:"size_vram"`
	ExpiresAt string `json:"expires_at"`
}

// ollamaModel is one installed Ollama model. Digest is part of the documented
// tag object and is intentionally not mapped: modelprovider.Model has no field
// for it.
type ollamaModel struct {
	Name       string `json:"name"`
	Model      string `json:"model"`
	ModifiedAt string `json:"modified_at"`
}

// ollamaShowResponse is POST {ollama}/api/show. Capabilities is a pointer so a
// missing or null field stays unknown rather than an empty set. ModelInfo is
// the documented metadata object; the show example carries context length as
// "{architecture}.context_length".
// https://docs.ollama.com/api-reference/show-model-details
type ollamaShowResponse struct {
	Capabilities *[]string       `json:"capabilities"`
	Thinking     *ollamaThinking `json:"thinking"`
	ModelInfo    map[string]any  `json:"model_info"`
}

// ollamaThinking is the show "thinking" object. Values are booleans or strings.
// A value list of only false means the model does not support thinking.
type ollamaThinking struct {
	Values []ollamaThinkValue `json:"values"`
}

// vllmModelsResponse is GET {vllm}/v1/models — the OpenAI-compatible model list.
type vllmModelsResponse struct {
	Data []vllmModel `json:"data"`
}

// vllmModel is one model served by vLLM.
type vllmModel struct {
	ID      string `json:"id"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
}
