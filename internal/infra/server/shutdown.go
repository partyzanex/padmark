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
	"syscall"
	"time"

	"github.com/partyzanex/shutdown"
	"github.com/partyzanex/shutdown/compat"
	"github.com/uptrace/bun"

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

// serverStop is the one shutdown manager of serve. It is a LIFO manager of three phases, so
// serve registers the database first, then the background work and then the listeners: the
// listeners stop first, the background work stops next and the database closes last. Each phase
// sets its own deadline from stopLimits, so a phase that uses all of its time does not take the
// time of the next phase. The manager context has no deadline: the LIFO manager skips the
// remaining phases when its context ends, and the database must close also after a slow stop.
type serverStop struct {
	manager *shutdown.Lifo
	// background holds the background work, such as the file sweep, in one LIFO phase.
	background *shutdown.Lifo
	// listeners holds the HTTP listeners. They do not depend on each other, so they stop at the
	// same time: a sequential manager would skip the listeners that remain after the deadline.
	listeners *shutdown.Group
	log       *slog.Logger
	limits    stopLimits
}

// newServerStop makes the shutdown manager. It registers no phase: serve registers them in
// the order of the resources that they close.
func newServerStop(limits stopLimits, log *slog.Logger) *serverStop {
	return &serverStop{
		manager: shutdown.NewLIFO(), background: shutdown.NewLIFO(), listeners: shutdown.NewGroup(),
		log: log, limits: limits,
	}
}

// addDatabase registers the database close. Call it first, so that the database closes last.
// The close has no limit of its own, and its error is only logged, as before the manager.
func (s *serverStop) addDatabase(db *bun.DB) {
	s.manager.Append(shutdown.ContextFn(func(ctx context.Context) error {
		closeDatabase(ctx, db, s.log)

		return nil
	}))
}

// addBackgroundPhase registers the stop of the background work. Call it after addDatabase. A
// failure is logged at the error level and does not change the exit code.
func (s *serverStop) addBackgroundPhase() {
	s.manager.Append(shutdown.ContextFn(func(ctx context.Context) error {
		stopBackground(ctx, s.background, s.limits.background, s.log)

		return nil
	}))
}

// addListenerPhase registers the stop of the listeners. Call it after addBackgroundPhase and
// before the first listener starts. Only this phase returns its error to serve.
func (s *serverStop) addListenerPhase() {
	s.manager.Append(shutdown.ContextFn(s.stopListeners))
}

// addListener adds the stop of srv to the listener phase. Call it before srv starts.
func (s *serverStop) addListener(ctx context.Context, name string, srv *http.Server) {
	s.listeners.Append(shutdown.ContextFn(newListener(ctx, name, srv, s.limits, s.log).stop))
}

// closeAfterFailedStart runs the shutdown sequence after startErr stopped the startup. Then only
// the database and the background work are registered. The sequence context has no cancel, so a
// cancelled ctx does not skip the close. The result is startErr joined with a close error.
func (s *serverStop) closeAfterFailedStart(ctx context.Context, startErr error) error {
	return errors.Join(startErr, s.manager.CloseContext(context.WithoutCancel(ctx)))
}

// stopListeners stops each listener under the limit of the listener phase. The deadline
// starts here, after the wake-up, not at server startup. Each listener closes its open
// connections when its drain limit ends; see listener.stop.
func (s *serverStop) stopListeners(ctx context.Context) error {
	shutCtx, cancel := context.WithTimeout(ctx, s.limits.listeners())
	defer cancel()

	started := time.Now()

	s.log.InfoContext(shutCtx, "shutdown: listeners stop started",
		"drain", s.limits.drain, "limit", s.limits.listeners())

	err := s.listeners.CloseContext(shutCtx)

	s.log.InfoContext(shutCtx, "shutdown: listeners stopped", "duration", time.Since(started))

	return errorcontext.Wrap(err, "graceful shutdown")
}

// waitAndShutdown blocks until SIGINT/SIGTERM arrives or serverCtx is cancelled (the main
// listener failed, or the parent context ended), then runs the shutdown sequence of stop. The
// manager runs the sequence one time. The context of the sequence keeps the values of serverCtx
// and has no deadline or cancel; see serverStop. On the failure path the listener cause is
// joined with any shutdown error, so both stay reachable through errors.Is.
func waitAndShutdown(serverCtx context.Context, stop *serverStop) error {
	sig, waitErr := compat.WaitForSignal(serverCtx, syscall.SIGINT, syscall.SIGTERM)

	shutCtx := context.WithoutCancel(serverCtx)

	if waitErr != nil {
		stop.log.InfoContext(shutCtx, "shutdown started: the server context ended", "cause", context.Cause(serverCtx))
	} else {
		stop.log.InfoContext(shutCtx, "shutdown started: signal received", "signal", sig.String())
	}

	shutdownErr := stop.manager.CloseContext(shutCtx)

	if waitErr != nil {
		return errors.Join(fmt.Errorf("server: %w", context.Cause(serverCtx)), shutdownErr)
	}

	if shutdownErr != nil {
		return fmt.Errorf("shutdown server: %w", shutdownErr)
	}

	return nil
}

// stopBackground closes the background work under a new deadline of timeout. A failure is
// logged at the error level: the database closes next, so work that did not stop can fail.
func stopBackground(ctx context.Context, background *shutdown.Lifo, timeout time.Duration, log *slog.Logger) {
	stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout)
	defer cancel()

	started := time.Now()

	log.InfoContext(stopCtx, "shutdown: background work stop started", "limit", timeout)

	err := background.CloseContext(stopCtx)
	if err != nil {
		log.ErrorContext(ctx, "stop background work", "err", err)
	}

	log.InfoContext(stopCtx, "shutdown: background work stopped", "duration", time.Since(started))
}

// closeDatabase closes the database of the server and logs the duration, because the close has
// no limit of its own: an operator must see when it holds the stop.
func closeDatabase(ctx context.Context, db *bun.DB, log *slog.Logger) {
	started := time.Now()

	log.InfoContext(ctx, "shutdown: database close started")
	closeServerDB(ctx, db, log)
	log.InfoContext(ctx, "shutdown: database closed", "duration", time.Since(started))
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
