package modelgw

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"

	"github.com/zimwip/goap/contracts/authz"
	"github.com/zimwip/goap/contracts/llm"
	"github.com/zimwip/goap/lib/modeltoken"
)

// AnthropicPath is the prefix of the gateway's Anthropic Messages endpoint (ADR 0123 §6): a client that calls the
// model by itself (Claude Code through the Claude Agent SDK) takes <gateway>/anthropic as its ANTHROPIC_BASE_URL and
// the token of its call as its API key.
const AnthropicPath = "/anthropic/"

// maxProxyBody caps a request to the Anthropic endpoint (a long Claude Code conversation with its tools).
const maxProxyBody = 32 << 20

// AnthropicProxy serves the Anthropic Messages API to a tool that calls the model by itself, so that each of its calls
// is a row of the ledger (ADR 0089) attributed to the person, the change and the process the tool ran for (ADR 0114),
// under the catalog policy of the gateway (enabled model, roles, quota). The request is passed to the provider as it
// is (streaming, tools, caching): only the model is resolved and the provider's key put in; no global behaviour is
// added, the tool's own instructions are its own. A model is a model of the platform catalog served by a provider of
// the anthropic protocol, named by an alias, provider/model, or its name.
type AnthropicProxy struct {
	Service *Service
	// Secret checks the tokens (lib/modeltoken), shared with the MCP hub that mints them (GOAP_MODEL_PROXY_SECRET).
	Secret []byte
	// HTTP calls the provider; nil: a client with no timeout (a stream lasts as long as the answer).
	HTTP *http.Client
}

// Route returns the path and the handler to mount.
func (p *AnthropicProxy) Route() (string, http.Handler) { return AnthropicPath, p }

// ServeHTTP implements http.Handler.
func (p *AnthropicProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	op := strings.TrimPrefix(r.URL.Path, strings.TrimSuffix(AnthropicPath, "/"))
	if r.Method != http.MethodPost || (op != "/v1/messages" && op != "/v1/messages/count_tokens") {
		anthropicError(w, http.StatusNotFound, "not_found_error", "the gateway serves POST /v1/messages and /v1/messages/count_tokens only")
		return
	}
	token := r.Header.Get("x-api-key")
	if token == "" {
		token, _ = strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	}
	claims, err := modeltoken.Verify(p.Secret, token, p.Service.now())
	if err != nil {
		anthropicError(w, http.StatusUnauthorized, "authentication_error", err.Error())
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxProxyBody+1))
	if err != nil || len(body) > maxProxyBody {
		anthropicError(w, http.StatusRequestEntityTooLarge, "request_too_large", "request too large or unreadable")
		return
	}
	var req map[string]json.RawMessage
	if err := json.Unmarshal(body, &req); err != nil {
		anthropicError(w, http.StatusBadRequest, "invalid_request_error", "the body is not a JSON object")
		return
	}
	var model string
	_ = json.Unmarshal(req["model"], &model)
	ctx := claimsContext(r.Context(), claims)
	if op == "/v1/messages/count_tokens" { // no generation: no row, the model resolved all the same
		t, rec, err := p.Service.anthropicTarget(ctx, model)
		if err != nil {
			anthropicError(w, statusOf(err), "invalid_request_error", err.Error())
			return
		}
		p.forward(ctx, w, r, rec, op, retarget(req, t.Model), nil)
		return
	}
	p.messages(ctx, w, r, req, model)
}

// claimsContext returns ctx acting as the token says: a system subject acting for the end user, the source and the
// correlation ids of the ledger.
func claimsContext(ctx context.Context, c modeltoken.Claims) context.Context {
	ctx = authz.With(ctx, authz.Principal{Subject: c.Subject, Org: c.Org, Project: c.Project}.For(c.EndUser))
	return llm.WithMeta(ctx, llm.CallMeta{Source: c.Source, ProcessID: c.Process, ChangeID: c.Change, Step: llm.NoStep, Call: llm.NoStep})
}

// messages serves one generation: admitted, recorded, passed on, its usage read from the answer.
func (p *AnthropicProxy) messages(ctx context.Context, w http.ResponseWriter, r *http.Request, req map[string]json.RawMessage, model string) {
	s := p.Service
	rec := s.begin(ctx, KindComplete, model)
	rec.request(rawSystem(req["system"]), rawMessages(req["messages"]))
	t, prov, err := s.anthropicTarget(ctx, model)
	var m ModelEntry
	var period string
	if err == nil {
		m, period, err = s.admitModel(ctx, t, endUserOf(ctx), false)
	}
	if err != nil {
		rec.refuse(ctx, t.Provider, t.Model, err)
		anthropicError(w, statusOf(err), "permission_error", err.Error())
		return
	}
	u := &usageReader{}
	status, ferr := p.forward(ctx, w, r, prov, "/v1/messages", retarget(req, t.Model), u)
	if ferr == nil && status >= 400 {
		ferr = fmt.Errorf("provider %s answered %d: %s", t.Provider, status, strings.TrimSpace(u.errorText()))
	}
	rec.answer(u.text.String())
	s.recordUsage(ctx, t, m, period, int64(u.in+u.out))
	rec.finish(ctx, t.Provider, t.Model, u.in, u.out, ferr)
}

// forward passes body to the provider and copies its answer to w as it comes, through u when given. It returns the
// provider's status; an error is a failure to reach it or to read its answer.
func (p *AnthropicProxy) forward(ctx context.Context, w http.ResponseWriter, r *http.Request, prov ProviderRecord, op string, body []byte, u *usageReader) (int, error) {
	key, err := p.Service.key(ctx, prov)
	if err != nil {
		anthropicError(w, http.StatusBadGateway, "api_error", "API key of provider "+prov.Name+": "+err.Error())
		return 0, err
	}
	def := "https://api.anthropic.com"
	if k, ok := LookupKind(prov.Kind); ok && k.DefaultBaseURL != "" {
		def = k.DefaultBaseURL
	}
	up, err := http.NewRequestWithContext(ctx, http.MethodPost, trimBase(prov.BaseURL, def)+op, bytes.NewReader(body))
	if err != nil {
		anthropicError(w, http.StatusBadGateway, "api_error", err.Error())
		return 0, err
	}
	for _, h := range []string{"anthropic-version", "anthropic-beta", "Accept"} {
		if v := r.Header.Values(h); len(v) > 0 {
			up.Header[http.CanonicalHeaderKey(h)] = v
		}
	}
	if up.Header.Get("anthropic-version") == "" {
		up.Header.Set("anthropic-version", "2023-06-01")
	}
	up.Header.Set("Content-Type", "application/json")
	up.Header.Set("x-api-key", key)
	hc := p.HTTP
	if hc == nil {
		hc = &http.Client{}
	}
	resp, err := hc.Do(up)
	if err != nil {
		anthropicError(w, http.StatusBadGateway, "api_error", "provider "+prov.Name+" unreachable")
		return 0, fmt.Errorf("provider %s: %w", prov.Name, err)
	}
	defer resp.Body.Close()
	for _, h := range []string{"Content-Type", "Request-Id", "Retry-After", "Anthropic-Ratelimit-Requests-Remaining", "Anthropic-Ratelimit-Tokens-Remaining"} {
		if v := resp.Header.Get(h); v != "" {
			w.Header().Set(h, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	stream := strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream")
	flusher, _ := w.(http.Flusher)
	var src io.Reader = resp.Body
	if u != nil {
		u.stream = stream
		src = io.TeeReader(resp.Body, u)
	}
	buf := make([]byte, 32<<10)
	for {
		n, rerr := src.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return resp.StatusCode, fmt.Errorf("client gone: %w", werr)
			}
			if stream && flusher != nil {
				flusher.Flush()
			}
		}
		if errors.Is(rerr, io.EOF) {
			break
		}
		if rerr != nil {
			return resp.StatusCode, fmt.Errorf("provider %s: %w", prov.Name, rerr)
		}
	}
	if u != nil {
		u.close()
	}
	return resp.StatusCode, nil
}

// anthropicTarget resolves the model a tool asks for to a model of the catalog served by a loaded provider of the
// anthropic protocol: an alias or provider/model first, else a catalog model of that name (the first provider by name).
func (s *Service) anthropicTarget(ctx context.Context, model string) (Target, ProviderRecord, error) {
	snap, err := s.sync(ctx)
	if err != nil {
		return Target{}, ProviderRecord{}, err
	}
	provider := func(name string) (ProviderRecord, bool) {
		for _, p := range snap.Providers {
			if p.Name == name && p.Protocol == "anthropic" && p.Enabled {
				return p, true
			}
		}
		return ProviderRecord{}, false
	}
	if t, _, err := s.Router.Resolve(model); err == nil && model != "" {
		if p, ok := provider(t.Provider); ok {
			return t, p, nil
		}
		return t, ProviderRecord{}, fmt.Errorf("%w: %s resolves to %s/%s, not a provider of the Anthropic protocol", ErrInvalid, model, t.Provider, t.Model)
	}
	var names []string
	for _, m := range snap.Models {
		if m.Model == model {
			if _, ok := provider(m.Provider); ok {
				names = append(names, m.Provider)
			}
		}
	}
	sort.Strings(names)
	for _, n := range names {
		if _, _, err := s.Router.Resolve(n + "/" + model); err == nil { // the provider is loaded
			p, _ := provider(n)
			return Target{Provider: n, Model: model}, p, nil
		}
	}
	return Target{}, ProviderRecord{}, fmt.Errorf("%w: %q is not a model of the platform catalog on an Anthropic provider", ErrModelDisabled, model)
}

// endUserOf is who the roles of a model are checked for: the person a service acts for, else the caller.
func endUserOf(ctx context.Context) authz.Principal {
	p := authz.From(ctx)
	if p.System() && p.OnBehalfOf != "" {
		return authz.Principal{Subject: p.OnBehalfOf, Org: p.Org, Project: p.Project}
	}
	return p
}

// statusOf maps an error of the policy to the HTTP status of the Anthropic API.
func statusOf(err error) int {
	switch {
	case errors.Is(err, ErrForbidden):
		return http.StatusForbidden
	case errors.Is(err, ErrQuotaExceeded):
		return http.StatusTooManyRequests
	case errors.Is(err, ErrModelDisabled), errors.Is(err, ErrInvalid):
		return http.StatusBadRequest
	}
	return http.StatusBadGateway
}

func anthropicError(w http.ResponseWriter, status int, typ, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"type": "error", "error": map[string]any{"type": typ, "message": msg}})
}

// retarget returns the request with the model of the provider.
func retarget(req map[string]json.RawMessage, model string) []byte {
	out := make(map[string]json.RawMessage, len(req))
	for k, v := range req {
		out[k] = v
	}
	out["model"], _ = json.Marshal(model)
	b, _ := json.Marshal(out)
	return b
}

// rawSystem is the system text of a request (a string or text blocks), for the exchange.
func rawSystem(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var blocks []struct {
		Text string `json:"text"`
	}
	_ = json.Unmarshal(raw, &blocks)
	parts := make([]string, 0, len(blocks))
	for _, b := range blocks {
		parts = append(parts, b.Text)
	}
	return strings.Join(parts, "\n\n")
}

// rawMessages are the messages of a request, for the exchange: the text of each, the other blocks as JSON.
func rawMessages(raw json.RawMessage) []llm.Message {
	var msgs []struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	_ = json.Unmarshal(raw, &msgs)
	out := make([]llm.Message, 0, len(msgs))
	for _, m := range msgs {
		var text string
		if json.Unmarshal(m.Content, &text) != nil {
			text = blocksText(m.Content)
		}
		out = append(out, llm.Message{Role: m.Role, Content: text})
	}
	return out
}

func blocksText(raw json.RawMessage) string {
	var blocks []map[string]json.RawMessage
	if json.Unmarshal(raw, &blocks) != nil {
		return string(raw)
	}
	parts := make([]string, 0, len(blocks))
	for _, b := range blocks {
		var typ, text string
		_ = json.Unmarshal(b["type"], &typ)
		if typ == "text" && json.Unmarshal(b["text"], &text) == nil {
			parts = append(parts, text)
			continue
		}
		j, _ := json.Marshal(b)
		parts = append(parts, string(j))
	}
	return strings.Join(parts, "\n")
}

// usageReader reads the answer of the provider as it passes: the tokens it counts and the text it gives, from one JSON
// message or from the events of a stream.
type usageReader struct {
	stream  bool
	inTool  bool
	in, out int
	text    strings.Builder
	body    bytes.Buffer // a JSON answer (capped), or the pending line of a stream
	errBody bytes.Buffer
}

func (u *usageReader) Write(b []byte) (int, error) {
	if !u.stream {
		if u.body.Len() < maxProxyBody {
			u.body.Write(b)
		}
		return len(b), nil
	}
	u.body.Write(b)
	for {
		line, err := u.body.ReadString('\n')
		if err != nil { // incomplete line: keep it for the next write
			rest := line
			u.body.Reset()
			u.body.WriteString(rest)
			break
		}
		u.event(strings.TrimRight(line, "\r\n"))
	}
	return len(b), nil
}

// close reads what is left: the JSON answer, or the last line of a stream.
func (u *usageReader) close() {
	if u.stream {
		sc := bufio.NewScanner(&u.body)
		for sc.Scan() {
			u.event(sc.Text())
		}
		return
	}
	var msg struct {
		Usage   anthropicUsage `json:"usage"`
		Content []struct {
			Type string          `json:"type"`
			Text string          `json:"text"`
			Name string          `json:"name"`
			In   json.RawMessage `json:"input"`
		} `json:"content"`
	}
	if json.Unmarshal(u.body.Bytes(), &msg) != nil {
		u.errBody.Write(u.body.Bytes())
		return
	}
	u.in, u.out = msg.Usage.input(), msg.Usage.OutputTokens
	for _, c := range msg.Content {
		switch c.Type {
		case "text":
			u.text.WriteString(c.Text)
		case "tool_use":
			fmt.Fprintf(&u.text, "\n[tool_use %s %s]", c.Name, c.In)
		}
	}
	if u.in == 0 && u.out == 0 {
		u.errBody.Write(u.body.Bytes())
	}
}

type anthropicUsage struct {
	InputTokens   int `json:"input_tokens"`
	CacheCreation int `json:"cache_creation_input_tokens"`
	CacheRead     int `json:"cache_read_input_tokens"`
	OutputTokens  int `json:"output_tokens"`
}

// input counts every input token the model read, cached or not.
func (a anthropicUsage) input() int { return a.InputTokens + a.CacheCreation + a.CacheRead }

// event reads one line of a stream (only its data lines carry something).
func (u *usageReader) event(line string) {
	data, ok := strings.CutPrefix(line, "data:")
	if !ok {
		return
	}
	var ev struct {
		Type    string `json:"type"`
		Message struct {
			Usage anthropicUsage `json:"usage"`
		} `json:"message"`
		Usage        anthropicUsage `json:"usage"`
		ContentBlock struct {
			Type string `json:"type"`
			Name string `json:"name"`
		} `json:"content_block"`
		Delta struct {
			Type        string `json:"type"`
			Text        string `json:"text"`
			PartialJSON string `json:"partial_json"`
		} `json:"delta"`
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal([]byte(strings.TrimSpace(data)), &ev) != nil {
		return
	}
	switch ev.Type {
	case "message_start":
		u.in, u.out = ev.Message.Usage.input(), ev.Message.Usage.OutputTokens
	case "message_delta": // the counts so far; the output of the whole answer comes last
		if n := ev.Usage.input(); n > 0 {
			u.in = n
		}
		if ev.Usage.OutputTokens > 0 {
			u.out = ev.Usage.OutputTokens
		}
	case "content_block_start":
		if ev.ContentBlock.Type == "tool_use" {
			fmt.Fprintf(&u.text, "\n[tool_use %s ", ev.ContentBlock.Name)
			u.inTool = true
		}
	case "content_block_stop":
		if u.inTool {
			u.text.WriteString("]")
			u.inTool = false
		}
	case "content_block_delta":
		switch ev.Delta.Type {
		case "text_delta":
			u.text.WriteString(ev.Delta.Text)
		case "input_json_delta":
			u.text.WriteString(ev.Delta.PartialJSON)
		}
	case "error":
		u.errBody.WriteString(ev.Error.Message)
	}
}

// errorText is what the provider said when it refused.
func (u *usageReader) errorText() string {
	if u.errBody.Len() > 0 {
		return capText(u.errBody.String(), MaxCallError)
	}
	return capText(u.body.String(), MaxCallError)
}

func capText(s string, n int) string {
	if len(s) > n {
		return strings.ToValidUTF8(s[:n], "")
	}
	return s
}
