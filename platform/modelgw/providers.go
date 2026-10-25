package modelgw

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/anthropics/anthropic-sdk-go/shared/constant"

	"github.com/zimwip/goap/contracts/llm"
)

const jsonInstruction = "\n\nRespond with a single JSON object and nothing else."

// ---- Anthropic (official SDK) ----------------------------------------------

// Anthropic calls the Claude Messages API.
type Anthropic struct {
	client anthropic.Client
}

// anthropicRetries is the retries of the SDK on a transient failure (it honours Retry-After), DefaultRetries + 1
// like the router's own (retry.go), which never retries an SDK error.
const anthropicRetries = DefaultRetries + 1

// NewAnthropic returns a Claude provider.
func NewAnthropic(apiKey, baseURL string) *Anthropic {
	opts := []option.RequestOption{option.WithAPIKey(apiKey), option.WithMaxRetries(anthropicRetries)}
	if baseURL != "" {
		opts = append(opts, option.WithBaseURL(baseURL))
	}
	return &Anthropic{client: anthropic.NewClient(opts...)}
}

// Complete implements Provider. Refusals are re-served by the server-side
// fallback model when the primary model declines.
func (a *Anthropic) Complete(ctx context.Context, model string, req llm.Request) (llm.Response, error) {
	maxTokens := int64(req.MaxTokens)
	if maxTokens <= 0 {
		maxTokens = 16000
	}
	system := req.System + jsonInstructionOf(req)
	params := anthropic.BetaMessageNewParams{
		Model:     anthropic.Model(model),
		MaxTokens: maxTokens,
		Betas:     []anthropic.AnthropicBeta{anthropic.AnthropicBetaServerSideFallback2026_07_01},
		Fallbacks: anthropic.BetaFallbacksParamUnion{OfDefault: constant.ValueOf[constant.Default]()},
	}
	if system != "" {
		params.System = []anthropic.BetaTextBlockParam{{Text: system}}
		if req.Cache {
			params.System[0].CacheControl = anthropic.NewBetaCacheControlEphemeralParam()
		}
	}
	for _, t := range req.Tools {
		params.Tools = append(params.Tools, anthropic.BetaToolUnionParam{OfTool: anthropicTool(t)})
	}
	for i, m := range req.Messages {
		blocks := anthropicBlocks(m)
		if req.Cache && i == len(req.Messages)-1 && len(blocks) > 0 {
			// two breakpoints: the system text (shared by every round and turn) and the last block of the
			// conversation (the prefix the next round repeats)
			cacheBlock(blocks[len(blocks)-1])
		}
		if m.Role == "assistant" {
			params.Messages = append(params.Messages, anthropic.BetaMessageParam{Role: anthropic.BetaMessageParamRoleAssistant, Content: blocks})
		} else {
			params.Messages = append(params.Messages, anthropic.BetaMessageParam{Role: anthropic.BetaMessageParamRoleUser, Content: blocks})
		}
	}
	msg, err := a.client.Beta.Messages.New(ctx, params)
	if err != nil {
		var apiErr *anthropic.Error
		if errors.As(err, &apiErr) {
			err = &sdkError{code: apiErr.StatusCode, err: err}
		}
		return llm.Response{}, err
	}
	if msg.StopReason == anthropic.BetaStopReasonRefusal {
		return llm.Response{}, llm.ErrRefusal
	}
	var text strings.Builder
	var calls []llm.ToolCall
	for _, block := range msg.Content {
		switch b := block.AsAny().(type) {
		case anthropic.BetaTextBlock:
			text.WriteString(b.Text)
		case anthropic.BetaToolUseBlock:
			calls = append(calls, llm.ToolCall{ID: b.ID, Name: b.Name, Arguments: argsOf(b.Input)})
		}
	}
	u := msg.Usage
	stop := stopReason(string(msg.StopReason), "end_turn", "max_tokens")
	if stop == string(anthropic.BetaStopReasonToolUse) {
		stop = llm.StopToolUse
	}
	return llm.Response{Text: text.String(), ToolCalls: calls, Provider: "anthropic", Model: string(msg.Model),
		StopReason: stop,
		Usage: llm.Usage{InputTokens: int(u.InputTokens + u.CacheReadInputTokens + u.CacheCreationInputTokens), OutputTokens: int(u.OutputTokens),
			CacheReadTokens: int(u.CacheReadInputTokens), CacheWriteTokens: int(u.CacheCreationInputTokens)}}, nil
}

// NativeTools implements NativeTools: Claude calls tools through its API.
func (a *Anthropic) NativeTools() bool { return true }

// anthropicTool is a tool of the Messages API.
func anthropicTool(t llm.ToolSpec) *anthropic.BetaToolParam {
	schema := anthropic.BetaToolInputSchemaParam{ExtraFields: map[string]any{}}
	for k, v := range t.InputSchema {
		switch k {
		case "type":
		case "properties":
			schema.Properties = v
		case "required":
			for _, r := range anySlice(v) {
				if s, ok := r.(string); ok {
					schema.Required = append(schema.Required, s)
				}
			}
		default:
			schema.ExtraFields[k] = v
		}
	}
	if schema.Properties == nil {
		schema.Properties = map[string]any{}
	}
	tool := &anthropic.BetaToolParam{Name: t.Name, InputSchema: schema}
	if t.Description != "" {
		tool.Description = anthropic.String(t.Description)
	}
	return tool
}

// anthropicBlocks are the content blocks of a turn: the tool results first (the API wants them at the start of the
// user turn that follows the calls), the text, then the tool calls.
func anthropicBlocks(m llm.Message) []anthropic.BetaContentBlockParamUnion {
	var blocks []anthropic.BetaContentBlockParamUnion
	for _, r := range m.ToolResults {
		blocks = append(blocks, anthropic.NewBetaToolResultBlock(r.CallID, r.Content, r.IsError))
	}
	if m.Content != "" || (len(m.ToolCalls) == 0 && len(m.ToolResults) == 0) {
		blocks = append(blocks, anthropic.NewBetaTextBlock(m.Content))
	}
	for _, c := range m.ToolCalls {
		args := c.Arguments
		if args == nil {
			args = map[string]any{}
		}
		blocks = append(blocks, anthropic.NewBetaToolUseBlock(c.ID, args, c.Name))
	}
	return blocks
}

// cacheBlock marks a block as a cache breakpoint.
func cacheBlock(b anthropic.BetaContentBlockParamUnion) {
	cc := anthropic.NewBetaCacheControlEphemeralParam()
	switch {
	case b.OfText != nil:
		b.OfText.CacheControl = cc
	case b.OfToolResult != nil:
		b.OfToolResult.CacheControl = cc
	case b.OfToolUse != nil:
		b.OfToolUse.CacheControl = cc
	}
}

// argsOf reads the arguments of a call (a decoded JSON object, or its raw text).
func argsOf(v any) map[string]any {
	var raw []byte
	switch x := v.(type) {
	case map[string]any:
		return x
	case json.RawMessage:
		raw = x
	case string:
		raw = []byte(x)
	default:
		raw, _ = json.Marshal(x)
	}
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil || m == nil {
		m = map[string]any{}
	}
	return m
}

func anySlice(v any) []any {
	switch x := v.(type) {
	case []any:
		return x
	case []string:
		out := make([]any, len(x))
		for i, s := range x {
			out[i] = s
		}
		return out
	}
	return nil
}

// jsonInstructionOf is the instruction asking for a JSON answer, worded for a call that may also call tools.
func jsonInstructionOf(req llm.Request) string {
	switch {
	case !req.JSON:
		return ""
	case len(req.Tools) > 0:
		return "\n\nWhen you answer without calling a tool, respond with a single JSON object and nothing else."
	}
	return jsonInstruction
}

// ---- OpenAI-compatible (Ollama, vLLM, Mistral, OpenAI…) -------------------

// OpenAICompatible calls a /chat/completions endpoint.
type OpenAICompatible struct {
	BaseURL string
	APIKey  string
	HTTP    *http.Client
}

// Complete implements Provider.
func (o *OpenAICompatible) Complete(ctx context.Context, model string, req llm.Request) (llm.Response, error) {
	body := map[string]any{"model": model}
	var msgs []map[string]any
	if req.System != "" {
		msgs = append(msgs, map[string]any{"role": "system", "content": req.System})
	}
	for _, m := range req.Messages {
		msgs = append(msgs, openAIMessages(m)...)
	}
	body["messages"] = msgs
	if len(req.Tools) > 0 {
		tools := make([]map[string]any, len(req.Tools))
		for i, t := range req.Tools {
			params := t.InputSchema
			if params == nil {
				params = map[string]any{"type": "object", "properties": map[string]any{}}
			}
			tools[i] = map[string]any{"type": "function", "function": map[string]any{"name": t.Name, "description": t.Description, "parameters": params}}
		}
		body["tools"] = tools
	}
	if req.MaxTokens > 0 {
		body["max_tokens"] = req.MaxTokens
	}
	if req.JSON {
		body["response_format"] = map[string]string{"type": "json_object"}
	}
	data, _ := json.Marshal(body)
	hr, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(o.BaseURL, "/")+"/chat/completions", bytes.NewReader(data))
	if err != nil {
		return llm.Response{}, err
	}
	hr.Header.Set("Content-Type", "application/json")
	if o.APIKey != "" {
		hr.Header.Set("Authorization", "Bearer "+o.APIKey)
	}
	hc := o.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 10 * time.Minute}
	}
	resp, err := hc.Do(hr)
	if err != nil {
		return llm.Response{}, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		msg := strings.TrimSpace(string(raw))
		if len(msg) > 400 {
			msg = msg[:400] + "…"
		}
		return llm.Response{}, fmt.Errorf("chat/completions: %w", statusErrorOf(resp, msg))
	}
	var out struct {
		Model   string `json:"model"`
		Choices []struct {
			Message struct {
				Content   string `json:"content"`
				ToolCalls []struct {
					ID       string `json:"id"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage struct {
			PromptTokens        int `json:"prompt_tokens"`
			CompletionTokens    int `json:"completion_tokens"`
			PromptTokensDetails struct {
				CachedTokens int `json:"cached_tokens"`
			} `json:"prompt_tokens_details"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return llm.Response{}, err
	}
	if len(out.Choices) == 0 {
		return llm.Response{}, fmt.Errorf("chat/completions: no choice")
	}
	choice := out.Choices[0]
	r := llm.Response{Text: choice.Message.Content, Model: out.Model,
		StopReason: stopReason(choice.FinishReason, "stop", "length"),
		Usage: llm.Usage{InputTokens: out.Usage.PromptTokens, OutputTokens: out.Usage.CompletionTokens,
			CacheReadTokens: out.Usage.PromptTokensDetails.CachedTokens}}
	for i, c := range choice.Message.ToolCalls {
		id := c.ID
		if id == "" {
			id = fmt.Sprintf("call_%d", i+1)
		}
		r.ToolCalls = append(r.ToolCalls, llm.ToolCall{ID: id, Name: c.Function.Name, Arguments: argsOf(c.Function.Arguments)})
	}
	if r.StopReason == "tool_calls" || len(r.ToolCalls) > 0 {
		r.StopReason = llm.StopToolUse
	}
	return r, nil
}

// NativeTools implements NativeTools: /chat/completions takes "tools". A server whose model has none refuses them,
// and the gateway then falls back on its text protocol (tools.go).
func (o *OpenAICompatible) NativeTools() bool { return true }

// openAIMessages are the chat messages of a turn: an assistant turn with its tool_calls, the results of a user turn
// as "tool" messages, then its text.
func openAIMessages(m llm.Message) []map[string]any {
	var out []map[string]any
	if m.Role == "assistant" && len(m.ToolCalls) > 0 {
		calls := make([]map[string]any, len(m.ToolCalls))
		for i, c := range m.ToolCalls {
			args, _ := json.Marshal(c.Arguments)
			if c.Arguments == nil {
				args = []byte("{}")
			}
			calls[i] = map[string]any{"id": c.ID, "type": "function", "function": map[string]any{"name": c.Name, "arguments": string(args)}}
		}
		msg := map[string]any{"role": "assistant", "tool_calls": calls, "content": nil}
		if m.Content != "" {
			msg["content"] = m.Content
		}
		return append(out, msg)
	}
	for _, r := range m.ToolResults {
		content := r.Content
		if r.IsError {
			content = "error: " + content
		}
		out = append(out, map[string]any{"role": "tool", "tool_call_id": r.CallID, "content": content})
	}
	if m.Content != "" || len(m.ToolResults) == 0 {
		out = append(out, map[string]any{"role": m.Role, "content": m.Content})
	}
	return out
}

// stopReason normalises a provider's stop reason: its words for a natural end and for the output token limit
// become llm.StopEnd and llm.StopMaxTokens, any other is kept as is.
func stopReason(got, end, maxTokens string) string {
	switch got {
	case end:
		return llm.StopEnd
	case maxTokens:
		return llm.StopMaxTokens
	}
	return got
}

// ---- Fake -------------------------------------------------------------------

// Fake answers without calling any model: an empty item list for JSON
// requests (actions then fail their effects and the planner falls back on
// human actions), an echo otherwise. For dev without API keys.
type Fake struct{}

// Complete implements Provider.
func (Fake) Complete(_ context.Context, model string, req llm.Request) (llm.Response, error) {
	inputTokens := estimateTokens(req.System)
	for _, m := range req.Messages {
		inputTokens += estimateTokens(m.Content)
	}
	if req.JSON {
		text := `{"items":[],"candidates":[]}`
		return llm.Response{Text: text, Provider: "fake", Model: model,
			Usage: llm.Usage{InputTokens: inputTokens, OutputTokens: estimateTokens(text)}}, nil
	}
	last := ""
	if n := len(req.Messages); n > 0 {
		last = req.Messages[n-1].Content
	}
	text := "echo: " + last
	return llm.Response{Text: text, Provider: "fake", Model: model,
		Usage: llm.Usage{InputTokens: inputTokens, OutputTokens: estimateTokens(text)}}, nil
}

// estimateTokens is a rough (~4 chars/token) size for the Fake provider, whose whole point is to
// run without a real model: it lets a dev running the local stack see the actual context size pushed
// to the agent (system + messages) without paying for or needing a real completion.
func estimateTokens(s string) int {
	return (len(s) + 3) / 4
}
