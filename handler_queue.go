package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"
	"syscall"
	"time"
)

var _ MetricHandler = (*QueuedHandler)(nil)
var _ Shutdownable = (*QueuedHandler)(nil)
var _ HealthChecker = (*QueuedHandler)(nil)

// transientRetryDelay is the pause before an update that failed with a network error is retried (once)
const transientRetryDelay = time.Second

// QueuedHandler decouples the exporters from slow metric handlers (database, network).
// Updates are processed by a single worker in arrival order, each with its own timeout,
// and dropped (with a warning) when the queue is full instead of piling up goroutines.
type QueuedHandler struct {
	name           string
	inner          MetricHandler
	timeout        time.Duration
	telemetry      *Telemetry
	unhealthyAfter time.Duration

	mu     sync.RWMutex
	closed bool
	jobs   chan *DeviceUpdate
	done   chan struct{}

	statusMu     sync.Mutex
	failingSince time.Time // zero while the last update succeeded
	lastErr      error
}

type QueueOptions struct {
	Size int
	// Timeout bounds the processing of one update
	Timeout   time.Duration
	Telemetry *Telemetry
	// UnhealthyAfter makes Health fail when updates keep failing for this long. Zero disables it.
	UnhealthyAfter time.Duration
}

func NewQueuedHandler(name string, inner MetricHandler, opts QueueOptions) *QueuedHandler {
	q := &QueuedHandler{
		name:           name,
		inner:          inner,
		timeout:        opts.Timeout,
		telemetry:      opts.Telemetry,
		unhealthyAfter: opts.UnhealthyAfter,
		jobs:           make(chan *DeviceUpdate, opts.Size),
		done:           make(chan struct{}),
	}
	opts.Telemetry.HandlerStarted(name)
	go q.run()
	return q
}

// Handle enqueues the update and returns immediately. The context is not used: the update outlives
// the caller, the worker applies its own timeout.
func (q *QueuedHandler) Handle(_ context.Context, u *DeviceUpdate) error {
	q.mu.RLock()
	defer q.mu.RUnlock()
	if q.closed {
		return nil
	}
	select {
	case q.jobs <- u:
	default:
		q.telemetry.HandlerDropped(q.name)
		slog.Warn("Metric handler queue is full, dropping update", "handler", q.name, "device", u.Device.SN)
	}
	return nil
}

func (q *QueuedHandler) run() {
	defer close(q.done)
	for u := range q.jobs {
		q.process(u)
	}
}

func (q *QueuedHandler) process(u *DeviceUpdate) {
	ctx, cancel := context.WithTimeout(context.Background(), q.timeout)
	defer cancel()
	start := time.Now()
	err := q.handle(ctx, u)
	if err != nil && isTransient(err) && ctx.Err() == nil {
		slog.Warn("Metric handler failed, retrying", "handler", q.name, "device", u.Device.SN, "error", err)
		select {
		case <-ctx.Done():
		case <-time.After(transientRetryDelay):
			err = q.handle(ctx, u)
		}
	}
	q.telemetry.HandlerDuration(q.name, time.Since(start))
	q.record(err)
	if err != nil {
		slog.Error("Metric handler failed", "handler", q.name, "device", u.Device.SN, "error", err)
	}
}

func (q *QueuedHandler) handle(ctx context.Context, u *DeviceUpdate) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic: %v", r)
		}
	}()
	return q.inner.Handle(ctx, u)
}

func (q *QueuedHandler) record(err error) {
	q.telemetry.HandlerResult(q.name, err)
	q.statusMu.Lock()
	defer q.statusMu.Unlock()
	q.lastErr = err
	switch {
	case err == nil:
		q.failingSince = time.Time{}
	case q.failingSince.IsZero():
		q.failingSince = time.Now()
	}
}

// Health reports an error when every update failed for longer than unhealthyAfter
func (q *QueuedHandler) Health() error {
	if q.unhealthyAfter <= 0 {
		return nil
	}
	q.statusMu.Lock()
	defer q.statusMu.Unlock()
	if !q.failingSince.IsZero() && time.Since(q.failingSince) > q.unhealthyAfter {
		return fmt.Errorf("%s handler failing for %s: %w", q.name, time.Since(q.failingSince).Round(time.Second), q.lastErr)
	}
	return nil
}

// isTransient reports whether err looks like a network problem that may go away on retry
func isTransient(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return false
	}
	var netErr net.Error
	return errors.As(err, &netErr) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.ECONNREFUSED)
}

// Close stops accepting updates, drains the queue (until ctx expires) and closes the wrapped handler
func (q *QueuedHandler) Close(ctx context.Context) {
	q.mu.Lock()
	if !q.closed {
		q.closed = true
		close(q.jobs)
	}
	q.mu.Unlock()

	select {
	case <-q.done:
	case <-ctx.Done():
		slog.Warn("Timed out while draining metric handler queue", "handler", q.name, "pending", len(q.jobs))
	}

	if s, ok := q.inner.(Shutdownable); ok {
		s.Close(ctx)
	}
}
