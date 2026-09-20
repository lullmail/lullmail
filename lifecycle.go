package main

// App lifecycle (audit OPS-04/OPS-05): one task group owns every background
// launch — admission before acceptance, a bounded join at shutdown — and
// every account operation runs under a per-account admission gate whose
// cancellation propagates into provider I/O. Deletion of one account seals
// only that account's gate, so it can never wait behind another account's
// work; the global owner lock survives solely to fence full-owner deletion
// against account creation and gated requests.

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/neutron-build/neutron/mail"
)

// backgroundTasks is the admission-then-join task group. Go launches work
// that outlives its trigger (initial syncs, reconciliation jobs, send
// workers, push dispatches); before this, each launch owned its own
// context.Background() and shutdown closed the pools underneath them.
type backgroundTasks struct {
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu     sync.Mutex
	closed bool
	// done closes exactly once, after the last admitted unit finishes.
	// Every Stop caller waits on the SAME channel: once closing begins, a
	// later Stop can no longer report a drain an earlier Stop is still
	// waiting for (audit 5 LIFE-03).
	done chan struct{}
}

func newBackgroundTasks(parent context.Context) *backgroundTasks {
	ctx, cancel := context.WithCancel(parent)
	return &backgroundTasks{ctx: ctx, cancel: cancel, done: make(chan struct{})}
}

// Go admits one background unit. Admission fails once shutdown began (or
// the root is already canceled), so a request arriving during the drain
// cannot hand new work to a group that is already leaving.
func (t *backgroundTasks) Go(fn func(context.Context)) bool {
	t.mu.Lock()
	if t.closed || t.ctx.Err() != nil {
		t.mu.Unlock()
		return false
	}
	t.wg.Add(1)
	t.mu.Unlock()
	go func() {
		defer t.wg.Done()
		fn(t.ctx)
	}()
	return true
}

// Stop cancels the group root and joins every admitted unit, bounded by
// timeout. It reports whether the join completed. Repeated calls all wait
// on the one shared drain channel, so the answer is honest for every
// caller, not just the first (audit 5 LIFE-03). Units that outlive the
// bound keep running against closed pools — the same cliff a process exit
// always was, now with a drain in front of it.
func (t *backgroundTasks) Stop(timeout time.Duration) bool {
	t.mu.Lock()
	if !t.closed {
		t.closed = true
		t.cancel()
		go func() {
			t.wg.Wait()
			close(t.done)
		}()
	}
	t.mu.Unlock()

	select {
	case <-t.done:
		return true
	case <-time.After(timeout):
		return false
	}
}

// shuttingDown reports whether the group has begun (or finished) stopping.
func (t *backgroundTasks) shuttingDown() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.closed
}

// tasksGroup lazily builds the group for App values that never ran
// startBackground (tests, one-shot commands): their launches get a
// never-canceled root so behavior matches a process that never drains.
func (a *App) tasksGroup() *backgroundTasks {
	a.tasksMu.Lock()
	defer a.tasksMu.Unlock()
	if a.tasks == nil {
		a.tasks = newBackgroundTasks(context.Background())
	}
	return a.tasks
}

// bgRoot is the cancellation root for detached work: the server's shutdown
// context once startBackground ran, a plain background context otherwise.
func (a *App) bgRoot() context.Context {
	a.tasksMu.Lock()
	t := a.tasks
	a.tasksMu.Unlock()
	if t == nil {
		return context.Background()
	}
	return t.ctx
}

// launch runs one named background unit on the group, with panics contained
// the way HTTP handlers are: a crashed sync must not take the process down.
// It reports whether the unit was admitted — a caller whose work was
// refused by a closing group must not acknowledge that work (audit 5
// LIFE-03).
func (a *App) launch(name string, fn func(ctx context.Context)) bool {
	log := a.log
	if log == nil {
		log = slog.Default()
	}
	t := a.tasksGroup()
	return t.Go(func(ctx context.Context) {
		defer func() {
			if p := recover(); p != nil {
				log.Error("background task panicked", "task", name, "panic", p)
			}
		}()
		fn(ctx)
	})
}

// shuttingDown reports whether the app's background group has begun its
// drain. Finalization paths use it to stay quiet about the cancellation
// they themselves were just handed (the OPS-04 writeback-noise residual).
func (a *App) shuttingDown() bool {
	a.tasksMu.Lock()
	t := a.tasks
	a.tasksMu.Unlock()
	return t != nil && t.shuttingDown()
}

// startBackgroundTasks roots the group at the server's shutdown context.
// Called once from startBackground before any launch can happen.
func (a *App) startBackgroundTasks(ctx context.Context) {
	a.tasksMu.Lock()
	defer a.tasksMu.Unlock()
	if a.tasks == nil {
		a.tasks = newBackgroundTasks(ctx)
	}
}

// stopBackground is the bounded Shutdown: cancel every background context
// (account gates derive from the same root, so provider I/O aborts), then
// join the in-flight work before the caller releases the pools.
func (a *App) stopBackground(timeout time.Duration) bool {
	a.tasksMu.Lock()
	t := a.tasks
	a.tasksMu.Unlock()
	if t == nil {
		return true
	}
	return t.Stop(timeout)
}

// contextLock is a one-holder lock whose acquisition observes a context:
// a queued waiter returns immediately on cancellation instead of piling
// onto a plain mutex (audit OPS-05's coalescing half).
type contextLock struct {
	token chan struct{}
}

func newContextLock() *contextLock {
	return &contextLock{token: make(chan struct{}, 1)}
}

func (m *contextLock) Lock(ctx context.Context) (func(), error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case m.token <- struct{}{}:
		if err := ctx.Err(); err != nil {
			<-m.token
			return nil, err
		}
		return func() { <-m.token }, nil
	}
}

// accountSyncGates serializes whole-account sync work per account, on top
// of the engine's own per-account lock: a burst of manual ?op=sync requests
// for one mailbox queues here with cancellation instead of stacking
// provider connections.
var accountSyncGates sync.Map

func accountSyncGate(acct mail.AccountID) *contextLock {
	v, _ := accountSyncGates.LoadOrStore(acct, newContextLock())
	return v.(*contextLock)
}
