package risk

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/zimwip/goap/pkg/domain"
)

// KindDerogation is a waiver with a rule, a signatory and an expiry (ADR 0075 §2): the decision to accept a gap (a
// waived postcondition, gate criterion or policy rule) for a bounded time, by a person who answers for it. Unlike a
// waiver (KindWaiver, which unblocks one run and never ends), it names what it waives, covers a target, is signed and
// expires. It is an item of the change versioned by its key like a risk: closing it adds a version.
//
// Data: key, rule (what is waived), target (the change impact, action run or gate it applies to), reason, signatory
// (the principal who answers for it, checked by the ABAC permission derogation:sign when the item is written), expires
// (RFC 3339, or a date: the end of that day, UTC) and status (open, the default, or closed). Every version restates
// rule, target, reason, signatory and expires.
const KindDerogation domain.ItemKind = "derogation"

// PermissionSign is the permission asked of whoever writes a derogation (ABAC resource "derogation", action "sign").
const PermissionSign = "derogation:sign"

// Derogation statuses.
const (
	DerogationOpen   = "open"
	DerogationClosed = "closed"
)

// DerogationStatuses lists the statuses a derogation may have.
var DerogationStatuses = []string{DerogationOpen, DerogationClosed}

// Derogation is the current version of a derogation of the change.
type Derogation struct {
	Key       string    `json:"key"`
	Rule      string    `json:"rule"`
	Target    string    `json:"target"`
	Reason    string    `json:"reason"`
	Signatory string    `json:"signatory"`
	Expires   time.Time `json:"expires"`
	Status    string    `json:"status"`
	// Item is the item holding this version, By what produced it, Versions how many versions the key has.
	Item     domain.ItemID `json:"item"`
	By       string        `json:"by,omitempty"`
	Versions int           `json:"versions"`
}

// Open reports whether the derogation was not closed.
func (d Derogation) Open() bool { return d.Status != DerogationClosed }

// ExpiredAt reports whether the derogation, still open, ran out at now.
func (d Derogation) ExpiredAt(now time.Time) bool { return d.Open() && !d.Expires.After(now) }

// InForce reports whether the derogation is open and has not run out at now: only then it covers anything.
func (d Derogation) InForce(now time.Time) bool { return d.Open() && d.Expires.After(now) }

// ParseExpiry reads the expiry of a derogation: RFC 3339, or a date (YYYY-MM-DD) meaning the end of that day, UTC.
func ParseExpiry(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC(), nil
	}
	if t, err := time.Parse(time.DateOnly, s); err == nil {
		return t.Add(24*time.Hour - time.Nanosecond).UTC(), nil
	}
	return time.Time{}, fmt.Errorf("expiry %q is neither a date (YYYY-MM-DD) nor RFC 3339", s)
}

// validateDerogation checks the data of a derogation item: no reason, rule, target, signatory or expiry, no derogation;
// an open version must expire in the future (a closing version may carry the expiry that passed).
func validateDerogation(it domain.ChangeItem) error {
	d := it.Data
	for _, f := range []string{"key", "rule", "target", "reason", "signatory", "expires"} {
		if dataString(d, f) == "" {
			return fmt.Errorf("derogation item requires data.%s", f)
		}
	}
	status := dataString(d, "status")
	if status != "" && !slices.Contains(DerogationStatuses, status) {
		return fmt.Errorf("derogation status must be one of %s", strings.Join(DerogationStatuses, ", "))
	}
	exp, err := ParseExpiry(dataString(d, "expires"))
	if err != nil {
		return fmt.Errorf("derogation: %w", err)
	}
	if status != DerogationClosed && !exp.After(time.Now()) {
		return fmt.Errorf("derogation expires in the past (%s)", exp.Format(time.RFC3339))
	}
	return nil
}

// Derogations returns the derogations of the change: the current version of each key, in the order they were signed.
func Derogations(c domain.Change) []Derogation {
	order, last, versions := registerOf(&c, KindDerogation)
	out := make([]Derogation, 0, len(order))
	for _, key := range order {
		it := last[key]
		d := it.Data
		exp, _ := ParseExpiry(dataString(d, "expires")) // validated on write; an unreadable one is the zero time: expired
		dr := Derogation{Key: key, Rule: dataString(d, "rule"), Target: dataString(d, "target"), Reason: dataString(d, "reason"),
			Signatory: dataString(d, "signatory"), Expires: exp, Status: dataString(d, "status"), Item: it.ID, By: it.ProducedBy, Versions: versions[key]}
		if dr.Status == "" {
			dr.Status = DerogationOpen
		}
		out = append(out, dr)
	}
	return out
}

// Expired returns the derogations of the change that are open and ran out at now: what the platform must close, sending
// what they covered back to review. It is pure, the clock is the caller's (never the graph's, ADR 0075).
func Expired(c domain.Change, now time.Time) []Derogation {
	var out []Derogation
	for _, d := range Derogations(c) {
		if d.ExpiredAt(now) {
			out = append(out, d)
		}
	}
	return out
}

// Covering returns the derogation in force at now whose target is one of targets (a change impact id or key, an action
// name, an execution), and whether there is one. The rule is not matched: it names what is waived, a human reads it.
func Covering(c domain.Change, now time.Time, targets ...string) (Derogation, bool) {
	for _, d := range Derogations(c) {
		if d.InForce(now) && slices.Contains(targets, d.Target) {
			return d, true
		}
	}
	return Derogation{}, false
}

// ClosingData returns the data of the version that closes a derogation.
func (d Derogation) ClosingData(reason string) map[string]any {
	return map[string]any{"key": d.Key, "rule": d.Rule, "target": d.Target, "reason": d.Reason, "signatory": d.Signatory,
		"expires": d.Expires.Format(time.RFC3339), "status": DerogationClosed, "closedBecause": reason}
}
