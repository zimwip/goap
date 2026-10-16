// Package okf renders the state of a graph at a baseline as an OKF tree (ADR 0111): one Markdown file per node, its
// identity, properties and links in a YAML front matter, its long texts and its links to other nodes in a Markdown
// body, and an index per namespace. The snapshot comes from the graph (pkg/graph.Snapshot, ADR 0104): no history, the
// state of one baseline.
package okf

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"maps"
	"path"
	"slices"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/zimwip/goap/contracts/domain"
	"github.com/zimwip/goap/graph/graph"
)

// Format names the OKF layout written here, recorded in the front matter of the index.
const Format = "goap.okf/v1"

// BodyAttribute is an attribute of a node type rendered in the Markdown body rather than in the front matter.
type BodyAttribute struct {
	Name, Label string
}

// Options say how the nodes of a type are rendered; both are optional (nil: the defaults).
type Options struct {
	// Name is the attribute holding the display name of the nodes of a type (`asName`); "" or nil: the first of
	// "title" and "name" the node carries as a string, else its key.
	Name func(typ string) string
	// Body are the attributes rendered as sections of the body (the long texts); nil: "description".
	Body func(typ string) []BodyAttribute
}

// The front matter of a node is flat, so that Obsidian shows it as properties and draws its links (ADR 0111):
// the identity keys below, `aliases` (the key) and `tags` (`<namespace>/<Type>`, `state/<state>`), then one key per
// outgoing link type (its local name) listing wikilinks to the targets' files, then the properties of the node by
// name (a property named like a key already written takes the prefix PropertyPrefix, unless it holds the same
// value: the title), then `linkProperties`, the
// properties of the links that carry some.
const (
	KeyKey            = "key"
	KeyType           = "type"
	KeyTitle          = "title"
	KeyState          = "state"
	KeyOwner          = "owner"
	KeyNamespace      = "namespace"
	KeyBaseline       = "baseline"
	KeyAliases        = "aliases"
	KeyTags           = "tags"
	KeyLinkProperties = "linkProperties"
	PropertyPrefix    = "prop_"
)

// LinkProperties are the properties of one outgoing link, in the front matter (`linkProperties`).
type LinkProperties struct {
	Type       string         `yaml:"type"`
	To         string         `yaml:"to"`
	Properties map[string]any `yaml:"properties"`
}

// mapping is an ordered YAML mapping.
type mapping struct{ node yaml.Node }

func newMapping() *mapping { return &mapping{node: yaml.Node{Kind: yaml.MappingNode}} }

func (m *mapping) has(k string) bool {
	for i := 0; i < len(m.node.Content); i += 2 {
		if m.node.Content[i].Value == k {
			return true
		}
	}
	return false
}

// scalar is the string value of a key, when it is a string.
func (m *mapping) scalar(k string) (string, bool) {
	for i := 0; i < len(m.node.Content); i += 2 {
		if m.node.Content[i].Value == k && m.node.Content[i+1].Kind == yaml.ScalarNode && m.node.Content[i+1].Tag == "!!str" {
			return m.node.Content[i+1].Value, true
		}
	}
	return "", false
}

func (m *mapping) set(k string, v any) error {
	var n yaml.Node
	if err := n.Encode(v); err != nil {
		return err
	}
	m.node.Content = append(m.node.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: k}, &n)
	return nil
}

// Index is the YAML header of the index of a namespace.
type Index struct {
	Format     string         `yaml:"format"`
	Namespace  string         `yaml:"namespace"`
	Branch     string         `yaml:"branch,omitempty"`
	Baseline   string         `yaml:"baseline,omitempty"`
	ExportedAt time.Time      `yaml:"exportedAt"`
	Nodes      int            `yaml:"nodes"`
	Types      map[string]int `yaml:"types,omitempty"`
}

// IndexFile is the name of the index of a namespace, at the root of its folder.
const IndexFile = "index.md"

type entry struct {
	node  graph.SnapshotNode
	path  string // relative to the folder of the namespace
	title string
}

// Write renders a snapshot as an OKF tree into a zip archive written to w as it goes, one file at a time (nothing but
// the snapshot is held): `<namespace>/index.md`, then one `<namespace>/<Type>/<key>.md` per node (the key made a safe
// file name, unique in its folder). at stamps the files.
func Write(w io.Writer, s graph.Snapshot, o Options, at time.Time) error {
	root := safeName(s.Namespace)
	entries := make([]*entry, 0, len(s.Nodes))
	byKey := map[string]*entry{}
	taken := map[string]bool{}
	for _, n := range s.Nodes {
		dir := safeName(typeName(n.Type))
		base := safeName(n.Key)
		p := dir + "/" + base + ".md"
		for i := 2; taken[strings.ToLower(p)]; i++ {
			p = fmt.Sprintf("%s/%s-%d.md", dir, base, i)
		}
		taken[strings.ToLower(p)] = true
		e := &entry{node: n, path: p, title: titleOf(n, o)}
		entries = append(entries, e)
		byKey[n.Key] = e
	}
	incoming := map[string][]incomingLink{}
	for _, e := range entries {
		for _, l := range e.node.Links {
			incoming[l.To] = append(incoming[l.To], incomingLink{typ: l.Type, from: e})
		}
	}
	z := zip.NewWriter(w)
	put := func(name string, content []byte) error {
		f, err := z.CreateHeader(&zip.FileHeader{Name: root + "/" + name, Method: zip.Deflate, Modified: at})
		if err != nil {
			return err
		}
		_, err = f.Write(content)
		return err
	}
	index, err := renderIndex(s, entries)
	if err != nil {
		return err
	}
	if err := put(IndexFile, index); err != nil {
		return err
	}
	for _, e := range entries {
		content, err := renderNode(s, e, byKey, incoming[e.node.Key], o)
		if err != nil {
			return fmt.Errorf("node %s: %w", e.node.Key, err)
		}
		if err := put(e.path, content); err != nil {
			return err
		}
	}
	return z.Close()
}

type incomingLink struct {
	typ  string
	from *entry
}

func renderNode(s graph.Snapshot, e *entry, byKey map[string]*entry, in []incomingLink, o Options) ([]byte, error) {
	n := e.node
	body := bodyAttributes(n.Type, o)
	inBody := map[string]bool{}
	for _, a := range body {
		if _, ok := n.Properties[a.Name].(string); ok {
			inBody[a.Name] = true
		}
	}
	fm, err := frontMatter(s, e, byKey, inBody)
	if err != nil {
		return nil, err
	}
	var b bytes.Buffer
	if err := writeFrontMatter(&b, fm); err != nil {
		return nil, err
	}
	fmt.Fprintf(&b, "# %s\n", oneLine(e.title))
	for _, a := range body {
		if !inBody[a.Name] {
			continue
		}
		text := strings.TrimSpace(n.Properties[a.Name].(string))
		if text == "" {
			continue
		}
		fmt.Fprintf(&b, "\n## %s\n\n%s\n", oneLine(a.Label), text)
	}
	if len(n.Links) > 0 {
		b.WriteString("\n## Links\n\n")
		for _, l := range n.Links {
			t := byKey[l.To]
			fmt.Fprintf(&b, "- %s: [%s](%s)\n", linkName(l.Type), oneLine(t.title), relPath(e, t))
		}
	}
	if len(in) > 0 {
		slices.SortFunc(in, func(a, b incomingLink) int {
			if c := strings.Compare(a.typ, b.typ); c != 0 {
				return c
			}
			return strings.Compare(a.from.node.Key, b.from.node.Key)
		})
		b.WriteString("\n## Referenced by\n\n")
		for _, l := range in {
			fmt.Fprintf(&b, "- [%s](%s) %s\n", oneLine(l.from.title), relPath(e, l.from), linkName(l.typ))
		}
	}
	return b.Bytes(), nil
}

// frontMatter builds the flat front matter of a node (see KeyKey).
func frontMatter(s graph.Snapshot, e *entry, byKey map[string]*entry, inBody map[string]bool) (*mapping, error) {
	n := e.node
	m := newMapping()
	var err error
	put := func(k string, v any) {
		if err == nil {
			err = m.set(k, v)
		}
	}
	put(KeyKey, n.Key)
	put(KeyType, n.Type)
	put(KeyTitle, e.title)
	if n.State != "" {
		put(KeyState, n.State)
	}
	if n.Owner != "" {
		put(KeyOwner, n.Owner)
	}
	put(KeyNamespace, s.Namespace)
	if s.Baseline != "" {
		put(KeyBaseline, s.Baseline)
	}
	put(KeyAliases, []string{n.Key})
	tags := []string{tagOf(s.Namespace) + "/" + tagOf(typeName(n.Type))}
	if n.State != "" {
		tags = append(tags, "state/"+tagOf(n.State))
	}
	put(KeyTags, tags)
	byType := map[string][]string{}
	var types []string
	var props []LinkProperties
	for _, l := range n.Links {
		t := typeName(l.Type)
		if _, ok := byType[t]; !ok {
			types = append(types, t)
		}
		byType[t] = append(byType[t], wikilink(byKey[l.To]))
		if len(l.Properties) > 0 {
			props = append(props, LinkProperties{Type: l.Type, To: l.To, Properties: l.Properties})
		}
	}
	for _, t := range types {
		if !m.has(t) {
			put(t, byType[t])
		}
	}
	for _, k := range slices.Sorted(maps.Keys(n.Properties)) {
		if inBody[k] {
			continue
		}
		name := k
		if v, ok := m.scalar(name); ok && v == n.Properties[k] {
			continue // the same value is already there (the title)
		}
		if m.has(name) || name == KeyLinkProperties || strings.HasPrefix(name, PropertyPrefix) {
			name = PropertyPrefix + k
		}
		put(name, n.Properties[k])
	}
	if len(props) > 0 {
		put(KeyLinkProperties, props)
	}
	return m, err
}

// wikilink is an Obsidian link to the file of a node: its path without extension (resolved from the vault root or as
// a path suffix, so the vault may be the namespace folder or the folder above), its title as label.
func wikilink(t *entry) string {
	return "[[" + strings.TrimSuffix(t.path, ".md") + "|" + strings.NewReplacer("|", "-", "]", ")", "[", "(").Replace(oneLine(t.title)) + "]]"
}

// tagOf makes a value a valid tag: letters, digits, '_', '-' and '/' kept, anything else '-'.
func tagOf(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-', r == '/':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	return b.String()
}

func renderIndex(s graph.Snapshot, entries []*entry) ([]byte, error) {
	idx := Index{Format: Format, Namespace: s.Namespace, Branch: s.Branch, Baseline: s.Baseline, ExportedAt: s.ExportedAt.UTC(), Nodes: len(entries)}
	byType := map[string][]*entry{}
	for _, e := range entries {
		byType[e.node.Type] = append(byType[e.node.Type], e)
	}
	if len(byType) > 0 {
		idx.Types = map[string]int{}
	}
	for t, es := range byType {
		idx.Types[t] = len(es)
	}
	var b bytes.Buffer
	if err := writeFrontMatter(&b, idx); err != nil {
		return nil, err
	}
	fmt.Fprintf(&b, "# %s\n", oneLine(s.Namespace))
	if s.Baseline != "" {
		fmt.Fprintf(&b, "\nState of baseline `%s` (branch %s).\n", s.Baseline, s.Branch)
	} else {
		b.WriteString("\nNothing landed yet: the empty state.\n")
	}
	types := slices.Sorted(func(yield func(string) bool) {
		for t := range byType {
			if !yield(t) {
				return
			}
		}
	})
	for _, t := range types {
		fmt.Fprintf(&b, "\n## %s\n\n", t)
		es := byType[t]
		slices.SortFunc(es, func(a, b *entry) int { return strings.Compare(a.node.Key, b.node.Key) })
		for _, e := range es {
			if e.title != e.node.Key {
				fmt.Fprintf(&b, "- [%s](%s) `%s`\n", oneLine(e.title), e.path, e.node.Key)
			} else {
				fmt.Fprintf(&b, "- [%s](%s)\n", oneLine(e.title), e.path)
			}
		}
	}
	return b.Bytes(), nil
}

func writeFrontMatter(b *bytes.Buffer, v any) error {
	b.WriteString("---\n")
	enc := yaml.NewEncoder(b)
	enc.SetIndent(2)
	if m, ok := v.(*mapping); ok {
		v = &m.node
	}
	if err := enc.Encode(v); err != nil {
		return err
	}
	if err := enc.Close(); err != nil {
		return err
	}
	b.WriteString("---\n\n")
	return nil
}

func bodyAttributes(typ string, o Options) []BodyAttribute {
	if o.Body != nil {
		return o.Body(typ)
	}
	return []BodyAttribute{{Name: "description", Label: "Description"}}
}

func titleOf(n graph.SnapshotNode, o Options) string {
	names := []string{"title", "name"}
	if o.Name != nil {
		if a := o.Name(n.Type); a != "" {
			names = []string{a}
		}
	}
	for _, a := range names {
		if v, ok := n.Properties[a].(string); ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return n.Key
}

// relPath is the path of the file of `to` relative to the file of `from` (both one folder below the root).
func relPath(from, to *entry) string {
	if to == nil {
		return ""
	}
	if path.Dir(from.path) == path.Dir(to.path) {
		return path.Base(to.path)
	}
	return "../" + to.path
}

// typeName is the local name of a qualified type ("alm@Requirement": "Requirement").
func typeName(typ string) string {
	if q, err := domain.ParseTypeRef(typ); err == nil && q.Name != "" {
		return q.Name
	}
	return typ
}

func linkName(typ string) string { return typeName(typ) }

// safeName makes a key a portable file name: letters, digits, '.', '_' and '-' kept, anything else '-'; never empty
// nor a dot name.
func safeName(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-', r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), ".")
	if out == "" {
		return "_"
	}
	return out
}

// oneLine keeps a title on one line of Markdown.
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
