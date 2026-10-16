// Package llm defines the provider-agnostic completion contract used by the
// engine and implemented by the model gateway.
package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// Message is a conversation turn. An assistant turn may carry the tool calls the model made; the user turn after
// it carries their results (ADR 0098).
type Message struct {
	Role        string       `json:"role"` // user | assistant
	Content     string       `json:"content"`
	ToolCalls   []ToolCall   `json:"toolCalls,omitempty"`
	ToolResults []ToolResult `json:"toolResults,omitempty"`
}

// ToolSpec declares a tool the model may call: its name, what it does and the JSON schema of its arguments. A name
// may hold any character: the gateway gives the provider a name it accepts and maps it back (ADR 0098).
type ToolSpec struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	InputSchema map[string]any `json:"inputSchema,omitempty"`
}

// ToolCall is a call of a tool the model made. ID pairs it with its result.
type ToolCall struct {
	ID        string         `json:"id"`
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments,omitempty"`
}

// ToolResult is the result of a tool call given back to the model: Content is its text (a JSON document, usually),
// IsError says the call failed and Content is why.
type ToolResult struct {
	CallID  string `json:"callId"`
	Name    string `json:"name"`
	Content string `json:"content"`
	IsError bool   `json:"isError,omitempty"`
}

// Transcript renders the turn as text: its content, then its tool calls or results (the exchange of a call keeps
// it so, ADR 0059, 0089).
func (m Message) Transcript() string {
	out := m.Content
	add := func(label string, v any) {
		b, _ := json.Marshal(v)
		if out != "" {
			out += "\n"
		}
		out += label + " " + string(b)
	}
	if len(m.ToolCalls) > 0 {
		add("[tool calls]", m.ToolCalls)
	}
	if len(m.ToolResults) > 0 {
		add("[tool results]", m.ToolResults)
	}
	return out
}

// Request is a completion request. Model is an alias resolved by the gateway
// ("default", "fast", …) or an explicit "provider/model".
type Request struct {
	Model     string    `json:"model"`
	System    string    `json:"system,omitempty"`
	Messages  []Message `json:"messages"`
	MaxTokens int       `json:"maxTokens,omitempty"`
	// JSON asks the provider for a single JSON object answer.
	JSON bool `json:"json,omitempty"`
	// Tools are the tools the model may call (ADR 0098): a provider with native tool calling gets them as such, any
	// other through a text protocol the gateway runs for it. The calls come back in Response.ToolCalls.
	Tools []ToolSpec `json:"tools,omitempty"`
	// Cache says the caller will send the same prefix again (a tool loop, a conversation): a provider that caches
	// prompts only on request marks the system text and the messages as a cache prefix. Providers that cache on their
	// own ignore it. Writing a cache entry costs more than a plain read, so a single-shot call leaves it unset.
	Cache bool `json:"cache,omitempty"`
}

// Usage reports token consumption. InputTokens is the whole prompt, cached or not; CacheReadTokens and
// CacheWriteTokens are the parts of it read from and written to the provider's prompt cache.
type Usage struct {
	InputTokens      int `json:"inputTokens"`
	OutputTokens     int `json:"outputTokens"`
	CacheReadTokens  int `json:"cacheReadTokens,omitempty"`
	CacheWriteTokens int `json:"cacheWriteTokens,omitempty"`
}

// Response is a completion result.
type Response struct {
	Text     string `json:"text"`
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Usage    Usage  `json:"usage"`
	// ToolCalls are the tools the model asks to call (Request.Tools); Text is then what it said with them, if anything.
	ToolCalls []ToolCall `json:"toolCalls,omitempty"`
	// StopReason is why the model stopped, normalised: StopEnd, StopMaxTokens, or the provider's own word.
	StopReason string `json:"stopReason,omitempty"`
	// Behaviors names the global behaviours the gateway added to the instructions of the call (ADR 0093; a name with a
	// leading "!" was dropped by the cap), and BehaviorTokens estimates the tokens they added. The caller's request is
	// never changed: the gateway reports what it added here.
	Behaviors      []string `json:"behaviors,omitempty"`
	BehaviorTokens int      `json:"behaviorTokens,omitempty"`
	// BehaviorsEstimated says BehaviorTokens is the byte estimate, not a cost measured on the model.
	BehaviorsEstimated bool `json:"behaviorsEstimated,omitempty"`
}

// Normalised stop reasons of Response.StopReason.
const (
	StopEnd       = "end"
	StopMaxTokens = "max_tokens"
)

// StopToolUse is the stop reason of an answer that calls tools.
const StopToolUse = "tool_use"

// Transcript renders the answer as text: what the model said, then the tools it calls.
func (r Response) Transcript() string {
	return Message{Role: "assistant", Content: r.Text, ToolCalls: r.ToolCalls}.Transcript()
}

// Truncated says the answer was cut by the output token limit.
func (r Response) Truncated() bool { return r.StopReason == StopMaxTokens }

// Client completes prompts.
type Client interface {
	Complete(ctx context.Context, req Request) (Response, error)
}

// ClientFunc adapts a function to Client.
type ClientFunc func(ctx context.Context, req Request) (Response, error)

// Complete implements Client.
func (f ClientFunc) Complete(ctx context.Context, req Request) (Response, error) { return f(ctx, req) }

// DecodeJSON extracts the first JSON object of a model answer (tolerating
// markdown fences and surrounding prose) and decodes it into v.
func DecodeJSON(text string, v any) error {
	start := strings.IndexAny(text, "{[")
	if start < 0 {
		return fmt.Errorf("no JSON in model answer")
	}
	dec := json.NewDecoder(strings.NewReader(text[start:]))
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("decode model answer: %w", err)
	}
	return nil
}

// EmbedRequest asks for the embedding of texts. Model is an alias ("embed" when empty) or "provider/model".
type EmbedRequest struct {
	Model string   `json:"model,omitempty"`
	Texts []string `json:"texts"`
}

// EmbedResponse holds one vector per text, in order.
type EmbedResponse struct {
	Vectors  [][]float32 `json:"vectors"`
	Provider string      `json:"provider"`
	Model    string      `json:"model"`
	Tokens   int         `json:"tokens,omitempty"`
}

// Embedder embeds texts (implemented by the model gateway).
type Embedder interface {
	Embed(ctx context.Context, req EmbedRequest) (EmbedResponse, error)
}

// EmbedAlias is the alias of the embedding model of the platform.
const EmbedAlias = "embed"
