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

// ollamaModel is one installed Ollama model.
type ollamaModel struct {
	Name       string `json:"name"`
	Model      string `json:"model"`
	ModifiedAt string `json:"modified_at"`
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
