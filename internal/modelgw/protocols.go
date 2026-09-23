package modelgw

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/zimwip/goap/pkg/llm"
)

// ProviderSpec is the runtime configuration of one provider.
type ProviderSpec struct {
	Name     string
	Kind     string
	Protocol string
	BaseURL  string
	APIKey   string
}

// ModelInfo is a model offered by a provider.
type ModelInfo struct {
	ID          string
	DisplayName string
}

// Protocol is a wire protocol to talk to a provider. Protocols are the
// plug-in point of the gateway: register one with RegisterProtocol and every
// provider kind (or custom provider) can use it.
type Protocol struct {
	ID    string
	Label string
	// New builds the completion client of a provider.
	New func(spec ProviderSpec) (Provider, error)
	// List asks the provider for its models.
	List func(ctx context.Context, hc *http.Client, spec ProviderSpec) ([]ModelInfo, error)
}

// Kind is a provider preset: a protocol with its usual endpoint.
type Kind struct {
	ID             string
	Label          string
	Protocol       string
	DefaultBaseURL string
	KeyRequired    bool
	Description    string
}

var (
	regMu     sync.RWMutex
	protocols = map[string]Protocol{}
	kinds     = map[string]Kind{}
)

// RegisterProtocol adds (or replaces) a protocol.
func RegisterProtocol(p Protocol) {
	regMu.Lock()
	defer regMu.Unlock()
	protocols[p.ID] = p
}

// RegisterKind adds (or replaces) a provider preset.
func RegisterKind(k Kind) {
	regMu.Lock()
	defer regMu.Unlock()
	kinds[k.ID] = k
}

// LookupProtocol returns a registered protocol.
func LookupProtocol(id string) (Protocol, bool) {
	regMu.RLock()
	defer regMu.RUnlock()
	p, ok := protocols[id]
	return p, ok
}

// LookupKind returns a registered provider preset.
func LookupKind(id string) (Kind, bool) {
	regMu.RLock()
	defer regMu.RUnlock()
	k, ok := kinds[id]
	return k, ok
}

// Protocols lists the registered protocols, sorted by id.
func Protocols() []Protocol {
	regMu.RLock()
	defer regMu.RUnlock()
	out := make([]Protocol, 0, len(protocols))
	for _, p := range protocols {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Kinds lists the registered provider presets, sorted by label.
func Kinds() []Kind {
	regMu.RLock()
	defer regMu.RUnlock()
	out := make([]Kind, 0, len(kinds))
	for _, k := range kinds {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Label < out[j].Label })
	return out
}

func init() {
	RegisterProtocol(Protocol{ID: "anthropic", Label: "Anthropic Messages API",
		New: func(s ProviderSpec) (Provider, error) {
			if s.APIKey == "" {
				return nil, fmt.Errorf("an API key is required")
			}
			return NewAnthropic(s.APIKey, s.BaseURL), nil
		},
		List: listAnthropic})
	RegisterProtocol(Protocol{ID: "openai", Label: "OpenAI-compatible (/chat/completions)",
		New: func(s ProviderSpec) (Provider, error) {
			if s.BaseURL == "" {
				return nil, fmt.Errorf("a base URL is required")
			}
			return &OpenAICompatible{BaseURL: s.BaseURL, APIKey: s.APIKey}, nil
		},
		List: listOpenAI})
	RegisterProtocol(Protocol{ID: "gemini", Label: "Google Gemini (generateContent)",
		New: func(s ProviderSpec) (Provider, error) {
			if s.APIKey == "" {
				return nil, fmt.Errorf("an API key is required")
			}
			return &Gemini{BaseURL: s.BaseURL, APIKey: s.APIKey}, nil
		},
		List: listGemini})
	RegisterProtocol(Protocol{ID: "fake", Label: "Fake (echo, no network)",
		New: func(ProviderSpec) (Provider, error) { return Fake{}, nil },
		List: func(context.Context, *http.Client, ProviderSpec) ([]ModelInfo, error) {
			return []ModelInfo{{ID: "echo", DisplayName: "Echo"}}, nil
		}})

	RegisterKind(Kind{ID: "anthropic", Label: "Anthropic", Protocol: "anthropic", DefaultBaseURL: "https://api.anthropic.com", KeyRequired: true,
		Description: "Claude models through the Messages API."})
	RegisterKind(Kind{ID: "mistral", Label: "Mistral AI", Protocol: "openai", DefaultBaseURL: "https://api.mistral.ai/v1", KeyRequired: true,
		Description: "Mistral models through their OpenAI-compatible API."})
	RegisterKind(Kind{ID: "google", Label: "Google Gemini", Protocol: "gemini", DefaultBaseURL: "https://generativelanguage.googleapis.com/v1beta", KeyRequired: true,
		Description: "Gemini models through the Generative Language API (Google AI Studio key)."})
	RegisterKind(Kind{ID: "openai", Label: "OpenAI", Protocol: "openai", DefaultBaseURL: "https://api.openai.com/v1", KeyRequired: true,
		Description: "OpenAI models."})
	RegisterKind(Kind{ID: "openai-compatible", Label: "OpenAI-compatible (custom)", Protocol: "openai",
		Description: "Ollama, vLLM, LiteLLM or any server exposing /chat/completions. The key is optional."})
	RegisterKind(Kind{ID: "fake", Label: "Fake (echo)", Protocol: "fake", Description: "Answers without calling a model. For development."})
}

// ---- HTTP helper ----------------------------------------------------------

const maxBody = 8 << 20

func httpJSON(ctx context.Context, hc *http.Client, method, u string, headers map[string]string, in, out any) error {
	var body io.Reader
	if in != nil {
		data, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		if v != "" {
			req.Header.Set(k, v)
		}
	}
	if hc == nil {
		hc = &http.Client{Timeout: 10 * time.Minute}
	}
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if resp.StatusCode != http.StatusOK {
		msg := strings.TrimSpace(string(raw))
		if len(msg) > 400 {
			msg = msg[:400] + "…"
		}
		return fmt.Errorf("%s: %s", resp.Status, msg)
	}
	return json.Unmarshal(raw, out)
}

func trimBase(base, def string) string {
	if base == "" {
		base = def
	}
	return strings.TrimRight(base, "/")
}

// ---- model listing --------------------------------------------------------

func listAnthropic(ctx context.Context, hc *http.Client, s ProviderSpec) ([]ModelInfo, error) {
	var out struct {
		Data []struct {
			ID          string `json:"id"`
			DisplayName string `json:"display_name"`
		} `json:"data"`
	}
	err := httpJSON(ctx, hc, http.MethodGet, trimBase(s.BaseURL, "https://api.anthropic.com")+"/v1/models?limit=1000",
		map[string]string{"x-api-key": s.APIKey, "anthropic-version": "2023-06-01"}, nil, &out)
	if err != nil {
		return nil, err
	}
	var models []ModelInfo
	for _, m := range out.Data {
		models = append(models, ModelInfo{ID: m.ID, DisplayName: m.DisplayName})
	}
	return models, nil
}

func listOpenAI(ctx context.Context, hc *http.Client, s ProviderSpec) ([]ModelInfo, error) {
	if s.BaseURL == "" {
		return nil, fmt.Errorf("a base URL is required")
	}
	var out struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	hdr := map[string]string{}
	if s.APIKey != "" {
		hdr["Authorization"] = "Bearer " + s.APIKey
	}
	if err := httpJSON(ctx, hc, http.MethodGet, trimBase(s.BaseURL, "")+"/models", hdr, nil, &out); err != nil {
		return nil, err
	}
	var models []ModelInfo
	for _, m := range out.Data {
		models = append(models, ModelInfo{ID: m.ID})
	}
	return models, nil
}

func listGemini(ctx context.Context, hc *http.Client, s ProviderSpec) ([]ModelInfo, error) {
	var models []ModelInfo
	token := ""
	for page := 0; page < 10; page++ {
		u := trimBase(s.BaseURL, geminiBase) + "/models?pageSize=200"
		if token != "" {
			u += "&pageToken=" + url.QueryEscape(token)
		}
		var out struct {
			Models []struct {
				Name        string   `json:"name"`
				DisplayName string   `json:"displayName"`
				Methods     []string `json:"supportedGenerationMethods"`
			} `json:"models"`
			NextPageToken string `json:"nextPageToken"`
		}
		if err := httpJSON(ctx, hc, http.MethodGet, u, map[string]string{"x-goog-api-key": s.APIKey}, nil, &out); err != nil {
			return nil, err
		}
		for _, m := range out.Models {
			chat := false
			for _, method := range m.Methods {
				chat = chat || method == "generateContent"
			}
			if chat {
				models = append(models, ModelInfo{ID: strings.TrimPrefix(m.Name, "models/"), DisplayName: m.DisplayName})
			}
		}
		if token = out.NextPageToken; token == "" {
			break
		}
	}
	return models, nil
}

// ---- Google Gemini ---------------------------------------------------------

const geminiBase = "https://generativelanguage.googleapis.com/v1beta"

// Gemini calls the Generative Language API (generateContent).
type Gemini struct {
	BaseURL string
	APIKey  string
	HTTP    *http.Client
}

// Complete implements Provider.
func (g *Gemini) Complete(ctx context.Context, model string, req llm.Request) (llm.Response, error) {
	type part struct {
		Text string `json:"text"`
	}
	type content struct {
		Role  string `json:"role,omitempty"`
		Parts []part `json:"parts"`
	}
	body := map[string]any{}
	system := req.System
	if req.JSON {
		system += jsonInstruction
	}
	if system != "" {
		body["systemInstruction"] = content{Parts: []part{{Text: system}}}
	}
	var contents []content
	for _, m := range req.Messages {
		role := "user"
		if m.Role == "assistant" {
			role = "model"
		}
		contents = append(contents, content{Role: role, Parts: []part{{Text: m.Content}}})
	}
	body["contents"] = contents
	cfg := map[string]any{}
	if req.MaxTokens > 0 {
		cfg["maxOutputTokens"] = req.MaxTokens
	}
	if req.JSON {
		cfg["responseMimeType"] = "application/json"
	}
	if len(cfg) > 0 {
		body["generationConfig"] = cfg
	}
	var out struct {
		Candidates []struct {
			Content struct {
				Parts []part `json:"parts"`
			} `json:"content"`
			FinishReason string `json:"finishReason"`
		} `json:"candidates"`
		UsageMetadata struct {
			PromptTokenCount     int `json:"promptTokenCount"`
			CandidatesTokenCount int `json:"candidatesTokenCount"`
		} `json:"usageMetadata"`
		ModelVersion string `json:"modelVersion"`
	}
	u := trimBase(g.BaseURL, geminiBase) + "/models/" + url.PathEscape(model) + ":generateContent"
	if err := httpJSON(ctx, g.HTTP, http.MethodPost, u, map[string]string{"x-goog-api-key": g.APIKey}, body, &out); err != nil {
		return llm.Response{}, fmt.Errorf("generateContent: %w", err)
	}
	if len(out.Candidates) == 0 {
		return llm.Response{}, fmt.Errorf("generateContent: no candidate")
	}
	var text strings.Builder
	for _, p := range out.Candidates[0].Content.Parts {
		text.WriteString(p.Text)
	}
	return llm.Response{Text: text.String(), Model: out.ModelVersion,
		Usage: llm.Usage{InputTokens: out.UsageMetadata.PromptTokenCount, OutputTokens: out.UsageMetadata.CandidatesTokenCount}}, nil
}
