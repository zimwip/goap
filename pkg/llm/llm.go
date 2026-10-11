// Package llm defines the provider-agnostic completion contract used by the
// engine and implemented by the model gateway.
package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// Message is a conversation turn.
type Message struct {
	Role    string `json:"role"` // user | assistant
	Content string `json:"content"`
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
