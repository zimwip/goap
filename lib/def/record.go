package def

import "time"

// Status of a stored version (a domain, or a methodology, which share the lifecycle): a draft is editable until it is
// published (immutable); a published version is archived instead of deleted.
type Status string

const (
	StatusDraft     Status = "draft"
	StatusPublished Status = "published"
	StatusArchived  Status = "archived"
)

// Record is a stored domain version, as the domain registry serves it (ADR 0125 §3).
type Record struct {
	Domain      Domain
	Status      Status
	CreatedAt   time.Time
	UpdatedAt   time.Time
	PublishedAt time.Time
	UpdatedBy   string
	// Protected marks a domain the RPCs never write (ADR 0167): only the local command line of the process owning the
	// registry's database does (the built-in domains are seeded protected).
	Protected bool
}

// Latest keeps the latest version of each domain of rs, in their first order: the latest published one if any, the
// latest draft otherwise.
func Latest(rs []Record) []Record {
	latest := map[string]Record{}
	var order []string
	for _, r := range rs {
		cur, ok := latest[r.Domain.Name]
		if !ok {
			order = append(order, r.Domain.Name)
		}
		better := !ok ||
			(r.Status == StatusPublished && (cur.Status != StatusPublished || r.PublishedAt.After(cur.PublishedAt))) ||
			(cur.Status != StatusPublished && r.Status != StatusArchived && r.CreatedAt.After(cur.CreatedAt))
		if better {
			latest[r.Domain.Name] = r
		}
	}
	out := make([]Record, 0, len(order))
	for _, n := range order {
		out = append(out, latest[n])
	}
	return out
}

// InForce keeps the latest published version of each stored domain of rs, the built-in ones included (ADR 0167).
func InForce(rs []Record) []*Domain {
	var out []*Domain
	for _, r := range Latest(rs) {
		if r.Status == StatusPublished {
			d := r.Domain
			out = append(out, &d)
		}
	}
	return out
}
