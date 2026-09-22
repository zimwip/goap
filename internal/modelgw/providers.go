package modelgw

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/anthropics/anthropic-sdk-go/shared/constant"

	"github.com/zimwip/goap/pkg/llm"
)

const jsonInstruction = "\n\nRespond with a single JSON object and nothing else."

// ---- Anthropic (official SDK) ----------------------------------------------

// Anthropic calls the Claude Messages API.
type Anthropic struct {
	client anthropic.Client
}

// NewAnthropic returns a Claude provider.
func NewAnthropic(apiKey, baseURL string) *Anthropic {
	opts := []option.RequestOption{option.WithAPIKey(apiKey)}
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
	system := req.System
	if req.JSON {
		system += jsonInstruction
	}
	params := anthropic.BetaMessageNewParams{
		Model:     anthropic.Model(model),
		MaxTokens: maxTokens,
		Betas:     []anthropic.AnthropicBeta{anthropic.AnthropicBetaServerSideFallback2026_07_01},
		Fallbacks: anthropic.BetaFallbacksParamUnion{OfDefault: constant.ValueOf[constant.Default]()},
	}
	if system != "" {
		params.System = []anthropic.BetaTextBlockParam{{Text: system}}
	}
	for _, m := range req.Messages {
		block := anthropic.NewBetaTextBlock(m.Content)
		if m.Role == "assistant" {
			params.Messages = append(params.Messages, anthropic.BetaMessageParam{Role: anthropic.BetaMessageParamRoleAssistant, Content: []anthropic.BetaContentBlockParamUnion{block}})
		} else {
			params.Messages = append(params.Messages, anthropic.NewBetaUserMessage(block))
		}
	}
	msg, err := a.client.Beta.Messages.New(ctx, params)
	if err != nil {
		return llm.Response{}, err
	}
	if msg.StopReason == anthropic.BetaStopReasonRefusal {
		return llm.Response{}, fmt.Errorf("model refused the request")
	}
	var text strings.Builder
	for _, block := range msg.Content {
		if t, ok := block.AsAny().(anthropic.BetaTextBlock); ok {
			text.WriteString(t.Text)
		}
	}
	return llm.Response{Text: text.String(), Provider: "anthropic", Model: string(msg.Model),
		Usage: llm.Usage{InputTokens: int(msg.Usage.InputTokens), OutputTokens: int(msg.Usage.OutputTokens)}}, nil
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
	type msg struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}
	body := map[string]any{"model": model}
	var msgs []msg
	if req.System != "" {
		msgs = append(msgs, msg{Role: "system", Content: req.System})
	}
	for _, m := range req.Messages {
		msgs = append(msgs, msg{Role: m.Role, Content: m.Content})
	}
	body["messages"] = msgs
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
		return llm.Response{}, fmt.Errorf("chat/completions: %s: %s", resp.Status, raw)
	}
	var out struct {
		Model   string `json:"model"`
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return llm.Response{}, err
	}
	if len(out.Choices) == 0 {
		return llm.Response{}, fmt.Errorf("chat/completions: no choice")
	}
	return llm.Response{Text: out.Choices[0].Message.Content, Model: out.Model,
		Usage: llm.Usage{InputTokens: out.Usage.PromptTokens, OutputTokens: out.Usage.CompletionTokens}}, nil
}

// ---- Fake -------------------------------------------------------------------

// Fake answers without calling any model: an empty item list for JSON
// requests (actions then fail their effects and the planner falls back on
// human actions), an echo otherwise. For dev without API keys.
type Fake struct{}

// Complete implements Provider.
func (Fake) Complete(_ context.Context, model string, req llm.Request) (llm.Response, error) {
	if req.JSON {
		return llm.Response{Text: `{"items":[],"candidates":[]}`, Provider: "fake", Model: model}, nil
	}
	last := ""
	if n := len(req.Messages); n > 0 {
		last = req.Messages[n-1].Content
	}
	return llm.Response{Text: "echo: " + last, Provider: "fake", Model: model}, nil
}
