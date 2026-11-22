package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net"
	"net/http"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/partyzanex/padmark/internal/errorcontext"
)

const (
	// forcedStopDivisor gives the part of --shutdown-timeout that stays for the forced stop:
	// one fifth.
	forcedStopDivisor = 5
	// forcedStopParts divides the forced stop between the request handlers and the background
	// work: one half each.
	forcedStopParts = 2
	// maxForcedStop limits that part, so that a long timeout stays for the open requests. After
	// the connections close and the request contexts end, a handler returns in milliseconds.
	maxForcedStop = 2 * time.Second
	// handlerPollInterval is the period of the check for request handlers that still run.
	handlerPollInterval = 5 * time.Millisecond
)

// errHandlersStillRun tells that request handlers did not return after the forced stop.
var errHandlersStillRun = errors.New("request handlers did not stop")

// stopLimits divides --shutdown-timeout between the phases of the shutdown sequence. The sum
// of the three values is the timeout, so the full sequence stays within one value that an
// operator can compare with the stop grace period of the container. The database close comes
// after these phases and has no limit of its own.
type stopLimits struct {
	// drain is the time for the open requests to complete.
	drain time.Duration
	// handlers is the time for the request handlers to return after their connections closed.
	handlers time.Duration
	// background is the time for the background work, such as the file sweep, to stop.
	background time.Duration
}

// newStopLimits divides timeout: the forced stop gets one fifth, at most maxForcedStop, and
// the open requests get the remaining time.
func newStopLimits(timeout time.Duration) stopLimits {
	forced := min(timeout/forcedStopDivisor, maxForcedStop)
	handlers := forced / forcedStopParts

	return stopLimits{drain: timeout - forced, handlers: handlers, background: forced - handlers}
}

// listeners returns the limit of the listener phase: the drain and the wait for the handlers.
func (l stopLimits) listeners() time.Duration {
	return l.drain + l.handlers
}

// unusedConns holds the connections that sent no complete request header (http.StateNew). A
// browser or a proxy opens such connections in advance. http.Server.Shutdown waits for each of
// them for up to 5 seconds, so the stop closes them at once. No request is lost: net/http
// closes a connection without a response when its request header completes after Shutdown
// started.
type unusedConns struct {
	conns  map[net.Conn]struct{}
	mu     sync.Mutex
	closed atomic.Int64
}

// track is the http.Server.ConnState hook.
func (u *unusedConns) track(conn net.Conn, state http.ConnState) {
	u.mu.Lock()
	defer u.mu.Unlock()

	if state == http.StateNew {
		u.conns[conn] = struct{}{}

		return
	}

	delete(u.conns, conn)
}

// closeAll closes each unused connection. The server calls track with http.StateClosed for
// them, which removes them from the set. The mutex does not stay locked during the close calls.
func (u *unusedConns) closeAll() {
	u.mu.Lock()
	conns := slices.Collect(maps.Keys(u.conns))
	u.mu.Unlock()

	// The count comes before the close calls: Shutdown returns when the connections are closed,
	// and the caller reads the count after that.
	u.closed.Store(int64(len(conns)))

	for _, conn := range conns {
		// Fail-soft: an error tells that the connection was closed already.
		if conn.Close() != nil {
			u.closed.Add(-1)
		}
	}
}

// listener is one HTTP server together with the state that its stop uses.
type listener struct {
	server *http.Server
	log    *slog.Logger
	// cancelRequests ends the context of each request. A closed connection does not always end
	// it: net/http cancels the context only when it reads from the connection.
	cancelRequests context.CancelFunc
	name           string
	unused         unusedConns
	limits         stopLimits
	// active is the number of requests whose handlers run.
	active atomic.Int64
}

// newListener prepares srv for the stop: it counts the requests, gives them a context that the
// stop can cancel and tracks the unused connections. Call it before srv starts.
func newListener(
	ctx context.Context, name string, srv *http.Server, limits stopLimits, log *slog.Logger,
) *listener {
	// The request contexts must stay open after ctx ends: the stop sequence cancels them.
	requestCtx, cancelRequests := context.WithCancel(context.WithoutCancel(ctx))

	lsn := &listener{
		server: srv, log: log, cancelRequests: cancelRequests, name: name, limits: limits,
		unused: unusedConns{conns: make(map[net.Conn]struct{}), mu: sync.Mutex{}, closed: atomic.Int64{}},
		active: atomic.Int64{},
	}

	handler := srv.Handler
	srv.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lsn.active.Add(1)
		defer lsn.active.Add(-1)

		handler.ServeHTTP(w, r)
	})
	srv.BaseContext = func(net.Listener) context.Context { return requestCtx }
	srv.ConnState = lsn.unused.track
	// Shutdown calls the function after it closes the listening socket.
	srv.RegisterOnShutdown(lsn.unused.closeAll)

	return lsn
}

// stop lets the open requests complete for the drain limit. When requests are open after that
// time, it closes their connections, cancels their contexts and waits for their handlers for
// the handlers limit, because the database closes later. The forced stop is the specified
// behavior, not a failure: only a handler that does not return or a close error is an error.
func (l *listener) stop(ctx context.Context) error {
	started := time.Now()

	drainCtx, cancel := context.WithTimeout(ctx, l.limits.drain)
	defer cancel()

	err := l.server.Shutdown(drainCtx)
	if err == nil || drainCtx.Err() == nil {
		l.cancelRequests()
		l.log.DebugContext(ctx, "shutdown: listener stopped", "listener", l.name,
			"duration", time.Since(started), "unused_connections_closed", l.unused.closed.Load())

		return errorcontext.Wrap(err, "stop "+l.name+" listener")
	}

	active := l.active.Load()
	closeErr := l.server.Close()

	l.cancelRequests()
	l.log.WarnContext(ctx, "shutdown: requests were open at the drain limit; their connections are closed",
		"listener", l.name, "active_requests", active, "drain", l.limits.drain)

	waitCtx, cancelWait := context.WithTimeout(ctx, l.limits.handlers)
	defer cancelWait()

	if remaining := l.waitHandlers(waitCtx); remaining > 0 {
		return errors.Join(
			fmt.Errorf("%s listener: %w: %d after %s", l.name, errHandlersStillRun, remaining, l.limits.handlers),
			closeErr,
		)
	}

	l.log.DebugContext(ctx, "shutdown: listener stopped after the forced close", "listener", l.name,
		"duration", time.Since(started))

	return errorcontext.Wrap(closeErr, "close "+l.name+" listener")
}

// waitHandlers blocks until no request handler runs or ctx ends. It returns the number of
// handlers that still run.
func (l *listener) waitHandlers(ctx context.Context) int64 {
	ticker := time.NewTicker(handlerPollInterval)
	defer ticker.Stop()

	for {
		active := l.active.Load()
		if active == 0 {
			return 0
		}

		select {
		case <-ctx.Done():
			return l.active.Load()
		case <-ticker.C:
		}
	}
}
