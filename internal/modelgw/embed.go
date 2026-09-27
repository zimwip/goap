package modelgw

import (
	"context"
	"fmt"
	"hash/fnv"
	"net/http"
	"net/url"
	"strings"

	"github.com/zimwip/goap/pkg/llm"
)

// Embedder is implemented by the providers that can embed texts (ADR 0026). The anthropic protocol has none.
type Embedder interface {
	Embed(ctx context.Context, model string, texts []string) (vectors [][]float32, tokens int, err error)
}

// Embed implements the embedding call of a provider on the model that alias or "provider/model" designates.
func (r *Router) Embed(ctx context.Context, req llm.EmbedRequest) (llm.EmbedResponse, error) {
	if req.Model == "" {
		req.Model = llm.EmbedAlias
	}
	t, p, err := r.Resolve(req.Model)
	if err != nil {
		return llm.EmbedResponse{}, err
	}
	e, ok := p.(Embedder)
	if !ok {
		return llm.EmbedResponse{}, fmt.Errorf("%s/%s: the provider cannot embed", t.Provider, t.Model)
	}
	vecs, tokens, err := e.Embed(ctx, t.Model, req.Texts)
	if err != nil {
		return llm.EmbedResponse{}, fmt.Errorf("%s/%s: %w", t.Provider, t.Model, err)
	}
	if len(vecs) != len(req.Texts) {
		return llm.EmbedResponse{}, fmt.Errorf("%s/%s: %d vectors for %d texts", t.Provider, t.Model, len(vecs), len(req.Texts))
	}
	return llm.EmbedResponse{Vectors: vecs, Provider: t.Provider, Model: t.Model, Tokens: tokens}, nil
}

// Embed implements Embedder on an OpenAI-compatible /embeddings endpoint.
func (o *OpenAICompatible) Embed(ctx context.Context, model string, texts []string) ([][]float32, int, error) {
	var out struct {
		Data []struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
		Usage struct {
			TotalTokens int `json:"total_tokens"`
		} `json:"usage"`
	}
	headers := map[string]string{}
	if o.APIKey != "" {
		headers["Authorization"] = "Bearer " + o.APIKey
	}
	u := strings.TrimRight(o.BaseURL, "/") + "/embeddings"
	if err := httpJSON(ctx, o.HTTP, http.MethodPost, u, headers, map[string]any{"model": model, "input": texts}, &out); err != nil {
		return nil, 0, fmt.Errorf("embeddings: %w", err)
	}
	vecs := make([][]float32, len(texts))
	for _, d := range out.Data {
		if d.Index < 0 || d.Index >= len(vecs) {
			return nil, 0, fmt.Errorf("embeddings: index %d out of range", d.Index)
		}
		vecs[d.Index] = d.Embedding
	}
	return vecs, out.Usage.TotalTokens, nil
}

// Embed implements Embedder with the Gemini batchEmbedContents method.
func (g *Gemini) Embed(ctx context.Context, model string, texts []string) ([][]float32, int, error) {
	type part struct {
		Text string `json:"text"`
	}
	type item struct {
		Model   string `json:"model"`
		Content struct {
			Parts []part `json:"parts"`
		} `json:"content"`
	}
	reqs := make([]item, len(texts))
	for i, t := range texts {
		reqs[i].Model = "models/" + model
		reqs[i].Content.Parts = []part{{Text: t}}
	}
	var out struct {
		Embeddings []struct {
			Values []float32 `json:"values"`
		} `json:"embeddings"`
	}
	u := trimBase(g.BaseURL, geminiBase) + "/models/" + url.PathEscape(model) + ":batchEmbedContents"
	if err := httpJSON(ctx, g.HTTP, http.MethodPost, u, map[string]string{"x-goog-api-key": g.APIKey}, map[string]any{"requests": reqs}, &out); err != nil {
		return nil, 0, fmt.Errorf("batchEmbedContents: %w", err)
	}
	vecs := make([][]float32, len(out.Embeddings))
	for i, e := range out.Embeddings {
		vecs[i] = e.Values
	}
	return vecs, 0, nil
}

// FakeEmbedDim is the dimension of the fake embeddings.
const FakeEmbedDim = 64

// Embed implements Embedder without a model: a bag of hashed words, so that texts sharing words are near
// (dev without API keys and tests).
func (Fake) Embed(_ context.Context, _ string, texts []string) ([][]float32, int, error) {
	vecs := make([][]float32, len(texts))
	tokens := 0
	for i, t := range texts {
		v := make([]float32, FakeEmbedDim)
		for _, w := range strings.FieldsFunc(strings.ToLower(t), func(r rune) bool { return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r > 127) }) {
			h := fnv.New32a()
			_, _ = h.Write([]byte(w))
			v[h.Sum32()%FakeEmbedDim]++
			tokens++
		}
		vecs[i] = v
	}
	return vecs, tokens, nil
}
