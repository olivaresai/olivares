// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package voice is a provider-agnostic, governed backend for realtime voice
// agents. It mints short-lived, policy-fixed sessions for four realtime voice
// providers behind one Provider interface and normalizes their divergent event
// streams into a single cross-provider taxonomy (most importantly, a single
// barge-in event regardless of provider).
//
// # Claude-as-think
//
// Anthropic exposes NO native realtime/voice API. There is no Claude WebRTC
// session, no streaming audio endpoint, and no "/v1/audio/stream" — any artifact
// claiming such an endpoint is fabricated and must not be trusted. The canonical,
// verified way to put Claude into a live voice loop is the STT -> LLM -> TTS
// pattern: a realtime voice provider owns listening (speech-to-text), speaking
// (text-to-speech) and turn-taking (VAD / end-of-turn), and calls Claude's
// Messages API (https://api.anthropic.com/v1/messages) for the reasoning turn.
// Deepgram Voice Agent is the canonical carrier of this pattern here: its
// agent.think block points at Anthropic with a server-held BYO key (see
// deepgram.go and ThinkConfig). The other three adapters (OpenAI, ElevenLabs,
// Gemini) use the provider's own native speech model.
//
// # Minimal data (docs/SECURITY-HARDENING.md)
//
// This package NEVER returns, logs, or persists the provider master API key, raw
// audio, or raw transcript text. A minted Session carries ONLY a short-lived
// EPHEMERAL credential plus the coordinates needed to connect; the provider
// master key and (for the Claude-as-think path) the BYO Anthropic key stay
// server-side and are applied to the provider but never handed to the client.
// Normalized transcript events are metadata deltas only (a short, non-sensitive
// label such as a role), never the spoken text.
//
// # Call plane (OpenAI Realtime SIP)
//
// OpenAI Realtime SIP support was added against the public wire shape verified
// on 2026-07-05. The call plane includes incoming-call webhook intake types,
// Standard-Webhooks verification (webhook-id, webhook-timestamp and
// webhook-signature), REST call control for accept / reject / refer / hangup,
// and a sideband client for live-call observation and governed session.update
// guardrail injection. The sideband uses connectors/internal/wsclient, a
// stdlib-only RFC 6455 client, and extracts per-modality usage counters from
// response.done without mapping those events into the legacy Provider taxonomy.
//
// # Governance
//
// Every Session is configured from the operator's approved SessionPolicy:
// model, voice, instructions, tools, and turn-detection are server-fixed. The
// client supplies none of them, so it cannot upgrade the model, rewrite the
// instructions, widen the tool surface, or relax turn-detection. A pseudonymous
// principal id (a hash, never PII) is forwarded to each provider as its audit /
// safety identifier where one is supported.
//
// # Verified provider endpoints
//
//   - OpenAI Realtime (GA): POST https://api.openai.com/v1/realtime/client_secrets
//     mints a creation-only client secret (default TTL 600s, range 10-7200s); the
//     SDP/answer exchange happens at https://api.openai.com/v1/realtime/calls.
//     SIP call control uses /v1/realtime/calls/{call_id}/{accept|reject|refer|hangup}
//     and sideband attaches to wss://api.openai.com/v1/realtime?call_id=...
//   - ElevenLabs Conversational: GET
//     https://api.elevenlabs.io/v1/convai/conversation/get-signed-url?agent_id=...
//     returns a signed WSS URL valid ~15 minutes to START a session.
//   - Deepgram Voice Agent: POST https://api.deepgram.com/v1/auth/grant mints a
//     short-lived token; the agent WebSocket is wss://agent.deepgram.com/v1/agent/converse.
//   - Google Gemini Live (v1alpha): POST
//     https://generativelanguage.googleapis.com/v1alpha/auth_tokens mints an
//     ephemeral token (~30 min expiry); the live WebSocket is the BidiGenerateContent
//     endpoint. Session resumption is mandatory for sessions longer than ~10 min.
//
// # Telephony seam
//
// OpenAI SIP is present as a call plane alongside the minted WebRTC session
// flow. Twilio ConversationRelay with Claude-as-think remains a documented
// FUTURE adapter of this SAME Provider interface (it would mint a SIP credential
// and set Session.Transport = "sip"). The interface is shaped to admit it
// without change.
//
// # Boundary
//
// Apache-2.0. This package imports only the standard library plus the
// stdlib-only connectors/internal/wsclient helper, and (in tests) testify. It
// NEVER imports /core, /modules, or the /sdk. All HTTP flows through an
// injectable Transport so the package is fully offline-testable.
package voice
