// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package gateway

// Matrix is the driver × protocol × transport honesty table for this slice.
// Status values are implemented, tested, or design-only. No vendor latency or
// throughput numbers belong here.
func Matrix() []Cell {
	return []Cell{
		{DriverOpenAICompat, ProtocolOpenAICompat, TransportHTTP, StatusTested,
			"POST /v1/chat/completions JSON. LiteLLM and Bifrost OpenAI-compatible destinations use this cell."},
		{DriverOpenAICompat, ProtocolOpenAICompat, TransportSSE, StatusTested,
			"Same path with stream=true and stream_options.include_usage."},
		{DriverOpenAICompat, ProtocolOpenAICompat, TransportWS, StatusDesignOnly,
			"OpenAI-compatible chat completions are not invoked over WebSocket here."},

		{DriverAnthropic, ProtocolAnthropic, TransportHTTP, StatusTested,
			"POST /v1/messages JSON. The live Claude PEP still uses claude-api; this driver is the contract adapter."},
		{DriverAnthropic, ProtocolAnthropic, TransportSSE, StatusTested,
			"Same path with stream=true (message_start / content_block_delta / message_stop)."},
		{DriverAnthropic, ProtocolAnthropic, TransportWS, StatusDesignOnly,
			"Anthropic Messages is HTTP and SSE, not WebSocket."},

		{DriverOllama, ProtocolOllama, TransportHTTP, StatusTested,
			"POST /api/chat. Streaming uses HTTP chunked NDJSON on this same path, not SSE."},
		{DriverOllama, ProtocolOllama, TransportSSE, StatusDesignOnly,
			"Ollama /api/chat does not speak text/event-stream. Native stream is NDJSON over HTTP."},
		{DriverOllama, ProtocolOllama, TransportWS, StatusDesignOnly,
			"Ollama chat is not invoked over WebSocket here."},
	}
}
