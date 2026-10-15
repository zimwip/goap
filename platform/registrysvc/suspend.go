package registrysvc

import (
	"context"
	"errors"
	"log/slog"
	"maps"
	"slices"
)

// Suspension (ADR 0125 §3). The domain registry publishes a domain without asking the methodologies: it emits an
// event, and the methodology registry checks its methodologies again. A published version that no longer compiles
// against the domains in force (the latest published version of each, what the graph judges its writes by) is
// suspended, never the domain nor the graph: no change starts on it (MethodologyVersion), no run reads it (Methodology,
// List), until a compatible version is published or the domains come back. A version still compiles against the
// versions it pins (ADR 0113) while it is in force; the pins do not shield it from the graph it writes to. Nothing is
// stored: a suspension is what the compiler says of the version against the domains of now, so a restart, a new
// built-in domain or a missed event change nothing to the rule.

// ErrSuspended marks a published methodology version suspended by a change of the domains it follows.
var ErrSuspended = errors.New("methodology version is suspended")

// suspendedError is ErrSuspended for one version, with the refusal of the compiler.
type suspendedError struct {
	ref   string
	cause error
}

func (e suspendedError) Error() string {
	return "methodology " + e.ref + " is suspended: it no longer compiles against the domains it follows: " + e.cause.Error()
}
func (e suspendedError) Is(target error) bool { return target == ErrSuspended }
func (e suspendedError) Unwrap() error        { return e.cause }

// Suspended says why a stored version is suspended; "" when it is in force, a draft or archived, or when the domains
// cannot be read (not a reason to suspend).
func (s *Service) Suspended(ctx context.Context, r Record) string {
	broken, err := s.suspension(ctx, r)
	if err != nil || broken == nil {
		return ""
	}
	return broken.Error()
}

// suspension is the refusal of the compiler for a published version against the domains in force (nil: in force);
// err a failure to read the domains.
func (s *Service) suspension(ctx context.Context, r Record) (broken, err error) {
	if r.Status != StatusPublished {
		return nil, nil
	}
	ds, err := s.domainsInForce(ctx)
	if err != nil {
		return nil, err
	}
	_, broken = s.compileAgainst(r, ds)
	return broken, nil
}

// DomainsChanged is the methodology registry's answer to an event of the domain registry (a version published or
// archived): the compiled methodologies are dropped and every published version is checked again (Recheck).
func (s *Service) DomainsChanged(ctx context.Context) (map[string]string, error) {
	s.compiled.clear()
	return s.Recheck(ctx)
}

// Recheck checks every published methodology version against the domains in force, and returns the suspended ones
// (name@version → reason). A version that becomes suspended, or comes back in force, since the last check is logged and
// published (goap.registry.methodology.suspended | resumed, payload {name, version, status, reason}).
func (s *Service) Recheck(ctx context.Context) (map[string]string, error) {
	rs, err := s.Store.List(ctx)
	if err != nil {
		return nil, err
	}
	now := map[string]string{}
	byKey := map[string]Record{}
	for _, r := range rs {
		if r.Status != StatusPublished {
			continue
		}
		k := key(r.Methodology.Name, r.Methodology.Version)
		byKey[k] = r
		broken, err := s.suspension(ctx, r)
		if err != nil {
			return nil, err
		}
		if broken != nil {
			now[k] = broken.Error()
		}
	}
	s.suspendedMu.Lock()
	before := s.suspended
	s.suspended = now
	s.suspendedMu.Unlock()
	log := s.Log
	if log == nil {
		log = slog.Default()
	}
	for _, k := range slices.Sorted(maps.Keys(now)) {
		if _, was := before[k]; !was {
			log.Warn("methodology suspended: it no longer compiles against the domains in force", "methodology", k, "reason", now[k])
			s.publishSuspension(ctx, "suspended", byKey[k], now[k])
		}
	}
	for _, k := range slices.Sorted(maps.Keys(before)) {
		if _, still := now[k]; !still {
			log.Info("methodology back in force", "methodology", k)
			if r, ok := byKey[k]; ok {
				s.publishSuspension(ctx, "resumed", r, "")
			}
		}
	}
	return now, nil
}

func (s *Service) publishSuspension(ctx context.Context, event string, r Record, reason string) {
	if s.Events != nil {
		_ = s.Events.Publish(ctx, "goap.registry.methodology."+event, map[string]string{"name": r.Methodology.Name,
			"version": r.Methodology.Version, "status": string(r.Status), "reason": reason})
	}
}
