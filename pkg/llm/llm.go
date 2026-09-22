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
