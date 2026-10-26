package main

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
)

// dependencies sets what each service waits for before it starts: the services whose URL its environment holds, but
// those it names lazy (it calls them later, they may call it at their start: the cycles broken by hand). A cycle left
// is an error: the stack would never start.
func dependencies(services []service) error {
	byPort := map[int]string{}
	for _, s := range services {
		if s.port > 0 {
			byPort[s.port] = s.name
		}
	}
	for i, s := range services {
		var after []string
		for _, v := range s.env {
			for port, name := range byPort {
				if name != s.name && !slices.Contains(s.lazy, name) && !slices.Contains(after, name) && mentions(v, port) {
					after = append(after, name)
				}
			}
		}
		slices.Sort(after)
		services[i].after = after
	}
	return cycle(services)
}

// mentions tells whether a value holds the URL of a port of the stack.
func mentions(v string, port int) bool {
	u := url(port)
	for {
		i := strings.Index(v, u)
		if i < 0 {
			return false
		}
		v = v[i+len(u):]
		if v == "" || v[0] < '0' || v[0] > '9' {
			return true
		}
	}
}

// cycle refuses services waiting for each other.
func cycle(services []service) error {
	after := map[string][]string{}
	for _, s := range services {
		after[s.name] = s.after
	}
	state := map[string]int{} // 1: being visited, 2: done
	var path []string
	var visit func(string) error
	visit = func(n string) error {
		switch state[n] {
		case 1:
			return fmt.Errorf("services waiting for each other: %s → %s (name one lazy)", strings.Join(path, " → "), n)
		case 2:
			return nil
		}
		state[n] = 1
		path = append(path, n)
		for _, d := range after[n] {
			if err := visit(d); err != nil {
				return err
			}
		}
		path = path[:len(path)-1]
		state[n] = 2
		return nil
	}
	for _, s := range services {
		if err := visit(s.name); err != nil {
			return err
		}
	}
	return nil
}

// startAll starts the services, each once the services it waits for answer their readiness, the others together.
// A service whose dependency did not get ready starts all the same (it retries its calls, the supervisor restarts it):
// the error says which were not ready.
func (s *supervisor) startAll(ctx context.Context, services []service) error {
	ready := map[string]chan struct{}{}
	for _, svc := range services {
		ready[svc.name] = make(chan struct{})
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	var errs []error
	for _, svc := range services {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer close(ready[svc.name])
			for _, dep := range svc.after {
				select {
				case <-ready[dep]:
				case <-ctx.Done():
					return
				}
			}
			if err := s.start(ctx, svc); err != nil && ctx.Err() == nil {
				mu.Lock()
				errs = append(errs, err)
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return err
	}
	return errors.Join(errs...)
}

// ports lists the ports the services listen on.
func ports(services []service) []portUse {
	var out []portUse
	for _, s := range services {
		if s.port > 0 {
			out = append(out, portUse{s.port, s.name})
		}
	}
	return out
}
