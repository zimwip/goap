package engine

import (
	"context"
	"errors"
	"sync"
	"time"
)

// Locker is the lock service engine replicas share (ADR 0123): a lock is a lease on a key, held by one replica,
// renewed while it works and taken over by another once it expires. The process store implements it when several
// replicas share it (internal/enginesvc.SQLStore); the holder is the store's replica identity.
type Locker interface {
	// Acquire takes the lock of key for ttl, or renews it when this replica holds it already. held is false while
	// another replica's lease runs; interrupt is what another replica asked of the holder (Interrupt), "" for nothing.
	Acquire(ctx context.Context, key string, ttl time.Duration) (held bool, interrupt string, err error)
	// Release lets the lock of key go when this replica holds it.
	Release(ctx context.Context, key string) error
	// Interrupt asks the replica holding the lock of key to let go of it (by: who asked); false when no other
	// replica holds it.
	Interrupt(ctx context.Context, key, by string) (bool, error)
}

// OrphanFinder is implemented by a store that knows which running processes no replica works on (ADR 0123): the
// processes running, written before the instant given, whose lock nobody holds.
type OrphanFinder interface {
	Orphans(ctx context.Context, before time.Time) ([]string, error)
}

// ErrFenced is the refusal of a write to a process whose lock another replica holds (ADR 0123): the writer lost
// its lease, and the run it was doing goes on elsewhere.
var ErrFenced = errors.New("process held by another engine replica")

// DefaultLockTTL is the lease of a lock when Engine.LockTTL is not set.
const DefaultLockTTL = 30 * time.Second

func (e *Engine) lockTTL() time.Duration {
	if e.LockTTL > 0 {
		return e.LockTTL
	}
	return DefaultLockTTL
}

// lock serializes the work on a key (a process id, or a named section): in this replica by a mutex, across the
// replicas by the shared lock when Engine.Locks is set. It returns the unlock.
func (e *Engine) lock(id string) func() {
	m, _ := e.locks.LoadOrStore(id, &sync.Mutex{})
	mu := m.(*sync.Mutex)
	mu.Lock()
	if e.Locks == nil {
		return mu.Unlock
	}
	release := e.hold(id)
	return func() {
		release()
		mu.Unlock()
	}
}

// hold takes the shared lock of key, waiting while another replica holds it, and keeps it until the returned
// release: renewed every tenth of its lease. An interruption asked by another replica (Cancel, a supersede) is
// handled like a local one: the run in flight on key is cancelled and lets go without failing. A lease lost (this
// replica could not renew it in time and another took it) cancels the run in flight too; its writes are refused
// (ErrFenced).
func (e *Engine) hold(key string) func() {
	ctx := context.Background()
	ttl := e.lockTTL()
	var interrupt string
	for wait := 20 * time.Millisecond; ; wait = min(2*wait, time.Second) {
		held, by, err := e.Locks.Acquire(ctx, key, ttl)
		if err != nil {
			e.log().Warn("lock", "key", key, "err", err)
		} else if held {
			interrupt = by
			break
		}
		time.Sleep(wait)
	}
	h := &held{e: e, key: key}
	e.held.Store(key, h)
	h.interrupt(interrupt)
	stop, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		tick := time.NewTicker(max(ttl/10, 10*time.Millisecond))
		defer tick.Stop()
		for {
			select {
			case <-stop:
				return
			case <-tick.C:
				ok, by, err := e.Locks.Acquire(ctx, key, ttl)
				switch {
				case err != nil:
					e.log().Warn("lock renewal", "key", key, "err", err)
				case !ok:
					e.log().Error("lock lost: another replica took over", "key", key)
					e.cancelRun(key)
					return
				default:
					h.interrupt(by)
				}
			}
		}
	}()
	return func() {
		close(stop)
		<-done
		e.held.CompareAndDelete(key, h)
		if h.done() {
			e.cancelling.Delete(key)
		}
		if err := e.Locks.Release(ctx, key); err != nil {
			e.log().Warn("lock release", "key", key, "err", err)
		}
	}
}

// held is a shared lock this replica holds, and whether another replica asked it to let go.
type held struct {
	e           *Engine
	key         string
	mu          sync.Mutex
	interrupted bool
}

// interrupt handles an interruption asked by another replica (by: who asked; "" for none), once: the run in
// flight on the key is cancelled and lets go without failing, as for a local cancellation.
func (h *held) interrupt(by string) {
	if by == "" {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.interrupted {
		return
	}
	h.interrupted = true
	h.e.cancelling.Store(h.key, by)
	h.e.cancelRun(h.key)
}

func (h *held) done() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.interrupted
}

// cancelRun cancels the run in flight on id in this replica, if any.
func (e *Engine) cancelRun(id string) {
	if h, ok := e.running.Load(id); ok {
		h.(*runHandle).cancel()
	}
}

// interrupt asks the run in flight on id to let go, wherever it runs: this replica's run is cancelled, another
// replica's is asked through the shared lock (it lets go at its next renewal, at once when Signals carries the ask).
func (e *Engine) interrupt(ctx context.Context, id, by string) {
	e.cancelRun(id)
	if e.Locks == nil {
		return
	}
	asked, err := e.Locks.Interrupt(ctx, id, by)
	if err != nil {
		e.log().Warn("interrupt", "process", id, "err", err)
		return
	}
	if asked && e.Signals != nil {
		if err := e.Signals.Interrupt(ctx, id, by); err != nil {
			e.log().Warn("interrupt signal", "process", id, "err", err)
		}
	}
}

// Interrupted handles the signal of another replica asking the holder of the lock of id to let go (Signals): when
// this replica holds it, its run in flight is cancelled at once rather than at the next renewal.
func (e *Engine) Interrupted(id, by string) {
	if h, ok := e.held.Load(id); ok {
		h.(*held).interrupt(by)
	}
}

// Signals carries hints between the replicas (ADR 0123): best effort, a lost signal only delays the work, the
// shared locks stay the arbiter. The receiving side calls Engine.Woken and Engine.Interrupted.
type Signals interface {
	// Wake tells the replicas that a process is runnable and no replica runs it (this one is at capacity).
	Wake(ctx context.Context) error
	// Interrupt tells the holder of the lock of id that another replica asked it to let go.
	Interrupt(ctx context.Context, id, by string) error
}

// Woken handles the signal of a replica that left a runnable process to the pool: Recover looks for it at once.
func (e *Engine) Woken() {
	select {
	case e.wakeCh() <- struct{}{}:
	default:
	}
}

func (e *Engine) wakeCh() chan struct{} {
	e.wakeOnce.Do(func() { e.wake = make(chan struct{}, 1) })
	return e.wake
}

// atCapacity reports whether this replica runs as many processes as it may (Engine.MaxRuns).
func (e *Engine) atCapacity() bool {
	return e.MaxRuns > 0 && e.inflight.Load() >= int64(e.MaxRuns)
}

// leave leaves a runnable process to the pool (this replica is at capacity): Recover takes it on a replica with room,
// told by Signals, this one included once a run of its own ends.
func (e *Engine) leave(id string) {
	e.log().Info("process left to the pool: this replica is at capacity", "process", id, "maxRuns", e.MaxRuns)
	if e.Signals != nil {
		if err := e.Signals.Wake(context.Background()); err != nil {
			e.log().Warn("wake signal", "err", err)
		}
	}
}

// runCounted runs a process as one of the runs this replica holds (Engine.MaxRuns); when it ends, the replica looks
// for work left to the pool.
func (e *Engine) runCounted(id string) {
	e.inflight.Add(1)
	defer func() {
		e.inflight.Add(-1)
		if e.Locks != nil && e.MaxRuns > 0 {
			e.Woken()
		}
	}()
	if _, err := e.Run(context.Background(), id); err != nil {
		e.log().Error("run", "process", id, "err", err)
	}
}

// Recover resumes the processes no replica works on (ADR 0123): the running processes whose lock nobody holds,
// left by a replica that stopped or crashed, or left to the pool by a replica at capacity. At once for the
// processes written before the call (a restart), then every interval for those idle for a lease (a run is written
// at every step, and holds its lock while it works), and at once for every process when woken (Woken: a replica
// left one to the pool). It takes no more than this replica's room (Engine.MaxRuns). It returns when ctx is done; a
// store that cannot tell the orphans makes it return at once.
func (e *Engine) Recover(ctx context.Context, every time.Duration) {
	finder, ok := e.Store.(OrphanFinder)
	if !ok {
		return
	}
	if every <= 0 {
		every = e.lockTTL()
	}
	before := e.clock()
	for {
		e.recover(ctx, finder, before)
		select {
		case <-ctx.Done():
			return
		case <-time.After(every):
			before = e.clock().Add(-e.lockTTL())
		case <-e.wakeCh():
			before = e.clock().Add(time.Millisecond)
		}
	}
}

// recover resumes the orphans written before an instant. A sub-process is resumed when its parent knows it (it
// waits on it, or its step will find it again); one its parent does not know yet (the parent's step was cut before
// recording it) is failed: the parent, resumed, runs its step again.
func (e *Engine) recover(ctx context.Context, finder OrphanFinder, before time.Time) {
	ids, err := finder.Orphans(ctx, before)
	if err != nil {
		e.log().Warn("recover", "err", err)
		return
	}
	for _, id := range ids {
		if e.atCapacity() {
			return
		}
		if _, busy := e.recovering.LoadOrStore(id, true); busy {
			continue
		}
		p, err := e.Store.Get(ctx, id)
		if err != nil || p.Status != StatusRunning {
			e.recovering.Delete(id)
			continue
		}
		if p.ParentID != "" && !e.knownByParent(ctx, p) {
			e.recovering.Delete(id)
			e.failOrphan(ctx, id)
			continue
		}
		e.log().Info("resuming a process no replica runs", "process", id)
		e.inflight.Add(1) // counted now: the next orphan sees this replica's room
		e.background(func() {
			defer e.recovering.Delete(id)
			e.inflight.Add(-1)
			e.runCounted(id)
		})
	}
}

// knownByParent reports whether the parent of p records it (as a child or as what it waits on).
func (e *Engine) knownByParent(ctx context.Context, p *Process) bool {
	parent, err := e.Store.Get(ctx, p.ParentID)
	if err != nil {
		return false
	}
	if parent.Pending != nil && parent.Pending.ChildProcessID == p.ID {
		return true
	}
	for _, id := range parent.Children {
		if id == p.ID {
			return true
		}
	}
	return false
}

// failOrphan fails a running process left by a stopped replica that nothing will look for again.
func (e *Engine) failOrphan(ctx context.Context, id string) {
	defer e.lock(id)()
	p, err := e.Store.Get(ctx, id)
	if err != nil || p.Status != StatusRunning {
		return
	}
	e.fail(p, errors.New("interrupted: the engine running it stopped before its parent recorded it"))
	if err := e.save(ctx, p, string(p.Status)); err != nil {
		e.log().Warn("recover", "process", id, "err", err)
	}
}
