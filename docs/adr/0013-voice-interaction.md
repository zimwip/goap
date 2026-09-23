# ADR 0013 — Voice interaction with the assistant

**Status**: accepted (phase 1) · **Date**: 2026-09

## Context

Users want to talk to the assistant instead of typing. Two families of solutions exist:

- **Local**: speech-to-text (and optionally text-to-speech) runs in the browser; only text
  reaches the backend.
- **Live**: audio is streamed to a server or a realtime model provider, which transcribes,
  reasons and answers by voice with low latency.

Constraints at the time of the decision:

- The assistant sends a text intent to `EngineService.StartProcess` and follows the run
  through `WatchEvents`. The browser never calls `modelgw`.
- `modelgw` is unary only (`Complete`, `ListModels`): no streaming, no tool-use API, no
  authorization check.
- The gateway is a reverse proxy with h2c and streaming flush, but has no WebSocket support
  and authenticates through the `Authorization` header only.
- Audio must stay strictly on the user's device for now; the UI must support French and
  English.

## Decision

**Phase 1 (implemented): local push-to-talk.**

- The microphone is captured with `MediaRecorder` while a button is held, then decoded to
  16 kHz mono samples (`web/src/lib/voice/recorder.ts`). Audio lives in memory only.
- Transcription runs in a Web Worker with a multilingual Whisper model (`tiny` or `base`,
  quantized) through transformers.js: WebGPU when available, WASM otherwise
  (`stt.worker.ts`, `stt.svelte.ts`). The language is auto-detected, with a French / English
  override.
- The transcript is appended to the draft of the assistant input; the user reviews it and
  sends it through the existing text path. **No backend change, no new API.**
- Models are downloaded on first use and cached by the browser. The host is configurable
  (`VITE_VOICE_MODEL_HOST`) so air-gapped deployments can self-host the files.
- The cloud-backed Web Speech API (`SpeechRecognition`) is deliberately not used.

**Phases 2 and 3 are documented, not built.**

### Phase 2 — server transcription (quality, weak devices)

A stateless unary RPC (`Transcribe`: short utterance, 30 s at most, to text) on `modelgw` or
a dedicated `speech` service, with a provider interface mirroring
`internal/modelgw/router.go` (Fake, OpenAI-compatible, faster-whisper on a GPU pool). It
scales horizontally like any unary call. It requires an authorization check, telemetry
spans following the GenAI attribute conventions (architecture §3.7), and no audio
persistence by default. Opt-in per deployment, since it breaks the "audio never leaves the
browser" guarantee.

### Phase 3 — live conversation (barge-in, speech-to-speech)

Needs a long-lived channel. Preferred option: WebRTC directly between the browser and a
realtime provider, with an **ephemeral token minted by our backend** (authenticated,
authorized, quota-checked); audio then bypasses our infrastructure and scaling is the
provider's concern. Alternative: WebSocket through the gateway, which requires:

- upgrade pass-through and tests in the gateway, plus browser-compatible authentication (a
  short-lived ticket in the query string or subprotocol, since browsers cannot set
  `Authorization` on a WebSocket);
- scaling on **concurrent sessions** instead of requests per second, sticky routing or a
  session store, connection draining on deploy, and idle-timeout alignment on every
  load balancer;
- per-tenant quotas and backpressure;
- streaming and tool-use RPCs on `modelgw`.

## Consequences

- No new server-side scaling concern in phase 1: voice is a UI modality over the existing
  text path.
- First use downloads roughly 40 to 75 MB per model; low-end devices transcribe slowly and
  accuracy is lower than large server models.
- Push-to-talk only: no voice activity detection and no hands-free mode yet.
- Phase 3 must not start before an ADR settles direct-to-provider versus gateway WebSocket.
