package main

import (
	"context"
	"sync"
	"time"
)

var _ MetricHandler = (*ThrottledHandler)(nil)
var _ Shutdownable = (*ThrottledHandler)(nil)
var _ HealthChecker = (*ThrottledHandler)(nil)

// ThrottledHandler passes at most one update per device per interval to the wrapped handler.
// Updates in between are coalesced: the latest one is passed on once the interval has elapsed.
// A change of the online status is always passed on immediately.
type ThrottledHandler struct {
	inner    MetricHandler
	interval time.Duration

	mu      sync.Mutex
	devices map[string]*throttleState

	stop chan struct{}
	done chan struct{}
}

type throttleState struct {
	lastWrite time.Time
	online    int
	pending   *DeviceUpdate
}

func NewThrottledHandler(inner MetricHandler, interval time.Duration) *ThrottledHandler {
	t := &ThrottledHandler{
		inner:    inner,
		interval: interval,
		devices:  make(map[string]*throttleState),
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
	}
	go t.run()
	return t
}

// Handle passes the update on or keeps it as pending. The wrapped handler is called with the lock held,
// so updates reach it in order; it is expected to return quickly (a QueuedHandler).
func (t *ThrottledHandler) Handle(ctx context.Context, u *DeviceUpdate) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	st, ok := t.devices[u.Device.SN]
	if !ok {
		st = &throttleState{}
		t.devices[u.Device.SN] = st
	}

	statusChanged := ok && u.Device.Online != st.online
	if statusChanged && st.pending != nil {
		// the last state before the change must not be lost
		_ = t.inner.Handle(ctx, st.pending)
	}
	if !ok || statusChanged || time.Since(st.lastWrite) >= t.interval {
		st.lastWrite = time.Now()
		st.online = u.Device.Online
		st.pending = nil
		return t.inner.Handle(ctx, u)
	}
	st.pending = u
	return nil
}

func (t *ThrottledHandler) run() {
	defer close(t.done)
	ticker := time.NewTicker(max(t.interval/4, 100*time.Millisecond))
	defer ticker.Stop()
	for {
		select {
		case <-t.stop:
			return
		case <-ticker.C:
			t.flush(false)
		}
	}
}

// flush passes on pending updates whose interval has elapsed, or all of them when force is true
func (t *ThrottledHandler) flush(force bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, st := range t.devices {
		if st.pending != nil && (force || time.Since(st.lastWrite) >= t.interval) {
			_ = t.inner.Handle(context.Background(), st.pending)
			st.lastWrite = time.Now()
			st.pending = nil
		}
	}
}

func (t *ThrottledHandler) Health() error {
	if h, ok := t.inner.(HealthChecker); ok {
		return h.Health()
	}
	return nil
}

// Close passes on all pending updates and closes the wrapped handler
func (t *ThrottledHandler) Close(ctx context.Context) {
	select {
	case <-t.stop:
	default:
		close(t.stop)
	}
	<-t.done
	t.flush(true)
	if s, ok := t.inner.(Shutdownable); ok {
		s.Close(ctx)
	}
}
