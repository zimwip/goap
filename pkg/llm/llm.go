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
}

// Usage reports token consumption.
type Usage struct {
	InputTokens  int `json:"inputTokens"`
	OutputTokens int `json:"outputTokens"`
}

// Response is a completion result.
type Response struct {
	Text     string `json:"text"`
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Usage    Usage  `json:"usage"`
	// Behaviors names the global behaviours the gateway added to the instructions of the call (ADR 0093; a name with a
	// leading "!" was dropped by the cap), and BehaviorTokens estimates the tokens they added. The caller's request is
	// never changed: the gateway reports what it added here.
	Behaviors      []string `json:"behaviors,omitempty"`
	BehaviorTokens int      `json:"behaviorTokens,omitempty"`
}

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
