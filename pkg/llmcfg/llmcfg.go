// Package llmcfg holds the configuration of the model gateway as graph data (platform namespace):
// providers, the models offered on the platform with their quota and access level, and the aliases.
// API keys are never in the graph, only a reference to the secret. Token usage is not configuration and
// stays in the gateway's own table, keyed by the key of the model node.
package llmcfg

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/graphsnap"
)

// Types and namespace of the graph objects.
const (
	NamespacePlatform = domain.NamespacePlatform
	NodeTypeProvider  = "platform@LlmProvider"
	NodeTypeModel     = "platform@LlmModel"
	NodeTypeAlias     = "platform@LlmAlias"
	// StateRetired is the state of an entry taken out of the configuration (lifecycle config of the platform domain,
	// ADR 0076: a node is never deleted): the snapshot leaves it out.
	StateRetired = "retired"
)

// Quota periods.
const (
	PeriodDay   = "day"
	PeriodMonth = "month"
	PeriodTotal = "total"
)

var nameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,39}$`)

// ValidName reports whether s can name a provider or an alias.
func ValidName(s string) bool { return nameRe.MatchString(s) }

// Provider is an LLM provider: a kind (preset) on a wire protocol. APIKeyRef references the API key
// ("env:<VAR>" or "<vault path>#<field>", alternatives separated by "|"); the key itself is never stored.
type Provider struct {
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	Protocol  string `json:"protocol"`
	BaseURL   string `json:"baseURL,omitempty"`
	Enabled   bool   `json:"enabled"`
	APIKeyRef string `json:"apiKeyRef,omitempty"`
}

// Model is a model offered on the platform.
type Model struct {
	Provider    string `json:"provider"`
	Model       string `json:"model"`
	DisplayName string `json:"displayName,omitempty"`
	Enabled     bool   `json:"enabled"`
	// QuotaTokens is the global token budget per QuotaPeriod (0: unlimited).
	QuotaTokens int64  `json:"quotaTokens,omitempty"`
	QuotaPeriod string `json:"quotaPeriod,omitempty"` // day | month | total
	// Roles may call the model (empty: every authenticated caller).
	Roles []string `json:"roles,omitempty"`
}

// The protected aliases of the platform (ADR 0084): internal names the platform itself resolves, so they exist on every
// install and cannot be retired or renamed, only retargeted.
const (
	// AssistantAlias is the model of the conversational assistant.
	AssistantAlias = "assistant"
	// HelperAlias is the model of the contextual helper that fills fields.
	HelperAlias = "helper"
)

// ProtectedAliases lists the aliases the platform keeps on every install.
func ProtectedAliases() []string { return []string{AssistantAlias, HelperAlias} }

// Alias maps a name ("default", "fast") to "provider/model".
type Alias struct {
	Alias string `json:"alias"`
	// Target is "provider/model"; empty only for a protected alias nothing is configured for yet (it then resolves to
	// nothing, so it is not available).
	Target string `json:"target"`
	// Protected marks an alias the platform itself uses: it may not be retired nor renamed (ADR 0084).
	Protected bool `json:"protected,omitempty"`
}

// ProviderKey is the key of the node of a provider.
func ProviderKey(name string) string { return "LLP:" + name }

// ModelKey is the key of the node of a model; it also keys the token usage of the model.
func ModelKey(provider, model string) string { return "LLM:" + provider + "/" + model }

// AliasKey is the key of the node of an alias.
func AliasKey(alias string) string { return "LLA:" + alias }

// Key is the key of the node of the model.
func (m Model) Key() string { return ModelKey(m.Provider, m.Model) }

// SplitTarget splits "provider/model".
func SplitTarget(target string) (provider, model string, ok bool) {
	provider, model, ok = strings.Cut(target, "/")
	return provider, model, ok && provider != "" && model != ""
}

// Validate checks a provider.
func (p Provider) Validate() error {
	if !ValidName(p.Name) {
		return fmt.Errorf("provider name must be 1-40 characters of a-z, 0-9, - or _")
	}
	if p.Protocol == "" {
		return fmt.Errorf("provider %s: protocol required", p.Name)
	}
	if p.BaseURL != "" && !strings.HasPrefix(p.BaseURL, "http://") && !strings.HasPrefix(p.BaseURL, "https://") {
		return fmt.Errorf("provider %s: base URL must start with http:// or https://", p.Name)
	}
	return nil
}

// Validate checks a model.
func (m Model) Validate() error {
	if m.Provider == "" || m.Model == "" {
		return fmt.Errorf("model needs a provider and a model id")
	}
	if m.QuotaTokens < 0 {
		return fmt.Errorf("model %s/%s: quota must be positive (0: unlimited)", m.Provider, m.Model)
	}
	switch m.QuotaPeriod {
	case "", PeriodDay, PeriodMonth, PeriodTotal:
	default:
		return fmt.Errorf("model %s/%s: quota period must be day, month or total", m.Provider, m.Model)
	}
	return nil
}

// Validate checks an alias.
func (a Alias) Validate() error {
	if !ValidName(a.Alias) {
		return fmt.Errorf("alias must be 1-40 characters of a-z, 0-9, - or _")
	}
	if a.Protected && a.Target == "" {
		return nil
	}
	if _, _, ok := SplitTarget(a.Target); !ok {
		return fmt.Errorf("alias %s: target must be provider/model", a.Alias)
	}
	return nil
}

func viaJSON(from, to any) error {
	b, err := json.Marshal(from)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, to)
}

// Props returns the properties of the LlmProvider node.
func (p Provider) Props() map[string]any { var m map[string]any; _ = viaJSON(p, &m); return m }

// Props returns the properties of the LlmModel node.
func (m Model) Props() map[string]any {
	if m.QuotaPeriod == "" {
		m.QuotaPeriod = PeriodMonth
	}
	if m.DisplayName == "" {
		m.DisplayName = m.Model
	}
	var out map[string]any
	_ = viaJSON(m, &out)
	return out
}

// Props returns the properties of the LlmAlias node.
func (a Alias) Props() map[string]any { var m map[string]any; _ = viaJSON(a, &m); return m }

// ProviderFromProps reads a provider from the properties of its node.
func ProviderFromProps(props map[string]any) (p Provider, err error) {
	if err = viaJSON(props, &p); err != nil {
		return p, fmt.Errorf("provider node: %w", err)
	}
	return p, p.Validate()
}

// ModelFromProps reads a model from the properties of its node.
func ModelFromProps(props map[string]any) (m Model, err error) {
	if err = viaJSON(props, &m); err != nil {
		return m, fmt.Errorf("model node: %w", err)
	}
	if m.QuotaPeriod == "" {
		m.QuotaPeriod = PeriodMonth
	}
	if m.DisplayName == "" {
		m.DisplayName = m.Model
	}
	return m, m.Validate()
}

// AliasFromProps reads an alias from the properties of its node.
func AliasFromProps(props map[string]any) (a Alias, err error) {
	if err = viaJSON(props, &a); err != nil {
		return a, fmt.Errorf("alias node: %w", err)
	}
	return a, a.Validate()
}

// Snapshot is the gateway configuration as of one baseline.
type Snapshot struct {
	Baseline domain.BaselineID
	// Problems lists the nodes that could not be read or that point to nothing.
	Problems  []string
	Providers []Provider
	Models    []Model
	Aliases   []Alias
	// Behaviors are the global behaviours in force (ADR 0093: enabled, not retired); Off are the disabled ones, kept for
	// the administrators' listing.
	Behaviors []Behavior
	Off       []Behavior
}

// BuildSnapshot reads the configuration of a baseline graph. Models of an unknown provider and aliases
// of an unknown model are left out and reported.
func BuildSnapshot(id domain.BaselineID, nodes []domain.Node, _ []domain.Link) *Snapshot {
	s := &Snapshot{Baseline: id}
	for _, n := range nodes {
		// nodes is scoped to the platform namespace by the Directory's cache
		// (Namespace: NamespacePlatform); no need to filter it again here.
		if n.State == StateRetired {
			continue
		}
		var err error
		switch n.Type {
		case NodeTypeProvider:
			var p Provider
			if p, err = ProviderFromProps(n.Properties); err == nil {
				s.Providers = append(s.Providers, p)
			}
		case NodeTypeModel:
			var m Model
			if m, err = ModelFromProps(n.Properties); err == nil {
				s.Models = append(s.Models, m)
			}
		case NodeTypeAlias:
			var a Alias
			if a, err = AliasFromProps(n.Properties); err == nil {
				s.Aliases = append(s.Aliases, a)
			}
		case NodeTypeBehavior:
			var b Behavior
			if b, err = BehaviorFromProps(n.Properties); err == nil {
				if b.Enabled {
					s.Behaviors = append(s.Behaviors, b)
				} else {
					s.Off = append(s.Off, b)
				}
			}
		}
		if err != nil {
			s.Problems = append(s.Problems, fmt.Sprintf("%s: %v", n.Key, err))
		}
	}
	prov := map[string]bool{}
	for _, p := range s.Providers {
		prov[p.Name] = true
	}
	keep := s.Models[:0]
	have := map[string]bool{}
	for _, m := range s.Models {
		if !prov[m.Provider] {
			s.Problems = append(s.Problems, fmt.Sprintf("%s: unknown provider %s", m.Key(), m.Provider))
			continue
		}
		have[m.Provider+"/"+m.Model] = true
		keep = append(keep, m)
	}
	s.Models = keep
	keepA := s.Aliases[:0]
	for _, a := range s.Aliases {
		if a.Protected && !have[a.Target] {
			// nothing is configured for it (yet): it stays listed, resolving to nothing, and is no problem
			keepA = append(keepA, a)
			continue
		}
		if !have[a.Target] {
			s.Problems = append(s.Problems, fmt.Sprintf("%s: unknown model %s", AliasKey(a.Alias), a.Target))
			continue
		}
		keepA = append(keepA, a)
	}
	s.Aliases = keepA
	sort.Slice(s.Providers, func(i, j int) bool { return s.Providers[i].Name < s.Providers[j].Name })
	sort.Slice(s.Models, func(i, j int) bool { return s.Models[i].Key() < s.Models[j].Key() })
	sort.Slice(s.Aliases, func(i, j int) bool { return s.Aliases[i].Alias < s.Aliases[j].Alias })
	byName := func(l []Behavior) {
		sort.Slice(l, func(i, j int) bool {
			if l[i].Order != l[j].Order {
				return l[i].Order < l[j].Order
			}
			return l[i].Name < l[j].Name
		})
	}
	byName(s.Behaviors)
	byName(s.Off)
	return s
}

// Directory reads the snapshot of the head of the main branch (see graphsnap.Cache).
type Directory struct {
	Graph graphsnap.Graph
	// TTL is how often the head is looked at (one second by default).
	TTL   time.Duration
	once  sync.Once
	cache graphsnap.Cache[*Snapshot]
}

// Snapshot returns the current snapshot; the last one keeps serving when the graph cannot be read.
func (d *Directory) Snapshot(ctx context.Context) (*Snapshot, error) {
	d.once.Do(func() {
		d.cache = graphsnap.Cache[*Snapshot]{Graph: d.Graph, Namespace: NamespacePlatform, TTL: d.TTL, Build: BuildSnapshot}
	})
	s, _, err := d.cache.Get(ctx)
	return s, err
}
