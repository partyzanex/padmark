package server

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/partyzanex/shutdown"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
)

const (
	// listenerTestWait limits each wait of a test for an event that must occur.
	listenerTestWait = 10 * time.Second
	// longDrain is a drain limit that a correct stop never uses up in a test.
	longDrain = time.Minute
	// stopAtOnce is the limit for a stop that must not wait for a limit. It is below the
	// 5 seconds that net/http waits for an unused connection.
	stopAtOnce = 2 * time.Second
)

func TestNewStopLimits(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		timeout, drain, handlers, background time.Duration
	}{
		{timeout: DefaultShutdownTimeout, drain: 8 * time.Second, handlers: time.Second, background: time.Second},
		{timeout: 2 * time.Minute, drain: 118 * time.Second, handlers: time.Second, background: time.Second},
		{
			timeout: 50 * time.Millisecond, drain: 40 * time.Millisecond,
			handlers: 5 * time.Millisecond, background: 5 * time.Millisecond,
		},
	} {
		limits := newStopLimits(test.timeout)

		require.Equal(t, stopLimits{drain: test.drain, handlers: test.handlers, background: test.background}, limits)
		require.Equal(t, test.timeout, limits.listeners()+limits.background,
			"the full shutdown sequence must stay within the timeout")
	}
}

// ListenerSuite stops a listener that serves on a loopback port.
type ListenerSuite struct {
	suite.Suite

	logs *syncBuffer
	log  *slog.Logger
}

func TestListenerSuite(t *testing.T) {
	suite.Run(t, new(ListenerSuite))
}

func (s *ListenerSuite) SetupTest() {
	s.logs = new(syncBuffer)
	s.log = slog.New(slog.NewTextHandler(s.logs, &slog.HandlerOptions{
		Level: slog.LevelDebug, AddSource: false, ReplaceAttr: nil,
	}))
}

func (s *ListenerSuite) TestStop_ClosesStalledRequestAtDrainLimit() {
	ctx := s.T().Context()
	entered, canceled := make(chan struct{}), make(chan struct{})

	// The handler does not read the request body, so net/http does not read from the
	// connection and does not cancel the request context when the connection closes.
	lsn, address := s.start(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		close(entered)
		<-r.Context().Done()
		close(canceled)
	}), stopLimits{drain: 100 * time.Millisecond, handlers: longDrain, background: 0})

	conn := s.dial(address)
	s.write(conn, "POST / HTTP/1.1\r\nHost: test\r\nContent-Length: 100\r\n\r\nhalf")
	s.waitFor(entered, "the handler must start")

	started := time.Now()
	err := lsn.stop(ctx)
	elapsed := time.Since(started)

	s.Require().NoError(err, "the forced close is not a failure")
	s.GreaterOrEqual(elapsed, lsn.limits.drain, "the open request must get the drain limit")
	s.Less(elapsed, stopAtOnce, "the stop must not wait for the handler limit")
	s.waitFor(canceled, "the stop must cancel the request context")
	s.Zero(lsn.active.Load())
	s.requireClosedByServer(conn)
	s.Contains(s.logs.String(), "listener=main active_requests=1")
}

func (s *ListenerSuite) TestStop_ReportsHandlerThatDoesNotReturn() {
	entered, release := make(chan struct{}), make(chan struct{})

	lsn, address := s.start(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		close(entered)
		<-release
	}), stopLimits{drain: 50 * time.Millisecond, handlers: 50 * time.Millisecond, background: 0})
	// The cleanup calls run in reverse order: this one releases the handler before the server stops.
	s.T().Cleanup(func() { close(release) })

	conn := s.dial(address)
	s.write(conn, "GET / HTTP/1.1\r\nHost: test\r\n\r\n")
	s.waitFor(entered, "the handler must start")

	err := lsn.stop(s.T().Context())

	s.Require().ErrorIs(err, errHandlersStillRun)
	s.ErrorContains(err, "main listener")
}

func (s *ListenerSuite) TestStop_ClosesUnusedConnectionAtOnce() {
	lsn, address := s.start(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}), stopLimits{drain: longDrain, handlers: longDrain, background: 0})

	unused := s.dial(address)
	// The server accepts connections in sequence, so a response on a later connection tells
	// that the server has the unused connection.
	status, err := get(s.T().Context(), address)
	s.Require().NoError(err)
	s.Require().Equal(http.StatusNoContent, status)

	started := time.Now()
	err = lsn.stop(s.T().Context())
	elapsed := time.Since(started)

	s.Require().NoError(err)
	s.Less(elapsed, stopAtOnce, "the stop must not wait for the unused connection")
	s.Equal(int64(1), lsn.unused.closed.Load())
	s.requireClosedByServer(unused)
	s.NotContains(s.logs.String(), "level=WARN")
}

func (s *ListenerSuite) TestStop_WaitsForOpenRequestToComplete() {
	entered, release := make(chan struct{}), make(chan struct{})

	lsn, address := s.start(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(entered)
		<-release
		w.WriteHeader(http.StatusNoContent)
	}), stopLimits{drain: longDrain, handlers: longDrain, background: 0})

	type result struct {
		err    error
		status int
	}

	response := make(chan result, 1)

	go func() {
		status, err := get(s.T().Context(), address)
		response <- result{err: err, status: status}
	}()

	s.waitFor(entered, "the handler must start")

	stopped := make(chan error, 1)

	go func() { stopped <- lsn.stop(s.T().Context()) }()

	// The stop must wait for the request. A wrong stop closes the connection, and the client
	// then gets an error and not this status.
	s.waitUntil(func() bool { return s.shutdownStarted(address) }, "the listening socket must close")
	close(release)

	select {
	case got := <-response:
		s.Require().NoError(got.err)
		s.Equal(http.StatusNoContent, got.status)
	case <-time.After(listenerTestWait):
		s.FailNow("the open request did not complete")
	}

	select {
	case err := <-stopped:
		s.Require().NoError(err)
	case <-time.After(listenerTestWait):
		s.FailNow("the stop did not return")
	}

	s.NotContains(s.logs.String(), "level=WARN")
}

// TestWaitAndShutdown_ForcedStopClosesEachListener makes sure that the listener phase stops
// each listener when one of them uses all of its drain limit, and that this is not an error.
func (s *ListenerSuite) TestWaitAndShutdown_ForcedStopClosesEachListener() {
	limits := stopLimits{drain: 100 * time.Millisecond, handlers: longDrain, background: 0}
	stop := newServerStop(limits, s.log)
	stop.addListenerPhase()

	entered := make(chan struct{})

	busy, busyAddress := s.start(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		close(entered)
		<-r.Context().Done()
	}), limits)
	idle, idleAddress := s.start(http.NotFoundHandler(), limits)

	stop.listeners.Append(shutdown.ContextFn(idle.stop))
	stop.listeners.Append(shutdown.ContextFn(busy.stop))

	conn := s.dial(busyAddress)
	s.write(conn, "GET / HTTP/1.1\r\nHost: test\r\n\r\n")
	s.waitFor(entered, "the handler must start")

	serverCtx, cancel := context.WithCancelCause(s.T().Context())
	cancel(nil)

	started := time.Now()
	err := waitAndShutdown(serverCtx, stop)

	s.Require().ErrorIs(err, context.Canceled)
	s.NotContains(err.Error(), "graceful shutdown", "the forced close is not a failure")
	s.Less(time.Since(started), stopAtOnce)
	s.True(s.shutdownStarted(busyAddress), "the busy listener must be closed")
	s.True(s.shutdownStarted(idleAddress), "the idle listener must be closed")
	s.requireClosedByServer(conn)
}

// --- helpers ---

// start serves handler on a loopback port. It returns the listener under test and its address.
func (s *ListenerSuite) start(handler http.Handler, limits stopLimits) (lsn *listener, address string) {
	srv := newHTTPServer("127.0.0.1:0", handler)
	lsn = newListener(s.T().Context(), "main", srv, limits, s.log)

	socket, err := new(net.ListenConfig).Listen(s.T().Context(), "tcp", "127.0.0.1:0")
	s.Require().NoError(err)

	served := make(chan error, 1)

	go func() { served <- srv.Serve(socket) }()

	s.T().Cleanup(func() {
		s.Require().NoError(srv.Close())
		s.Require().ErrorIs(<-served, http.ErrServerClosed)
	})

	return lsn, socket.Addr().String()
}

// dial opens a client connection that the test cleanup closes.
func (s *ListenerSuite) dial(address string) *net.TCPConn {
	conn, err := new(net.Dialer).DialContext(s.T().Context(), "tcp", address)
	s.Require().NoError(err)
	s.T().Cleanup(func() { _ = conn.Close() })

	tcpConn, ok := conn.(*net.TCPConn)
	s.Require().True(ok)

	return tcpConn
}

func (s *ListenerSuite) write(conn io.Writer, data string) {
	_, err := io.WriteString(conn, data)
	s.Require().NoError(err)
}

// get sends one request on a new connection and returns the status code. It makes no
// assertion, so a goroutine of a test can call it.
func get(ctx context.Context, address string) (int, error) {
	client := &http.Client{Timeout: listenerTestWait, Transport: nil, CheckRedirect: nil, Jar: nil}

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+address+"/", http.NoBody)
	if err != nil {
		return 0, err
	}

	// The connection must not stay open: an idle connection is not the subject of these tests.
	request.Close = true

	response, err := client.Do(request)
	if err != nil {
		return 0, err
	}

	return response.StatusCode, response.Body.Close()
}

// shutdownStarted tells that the listening socket at address is closed.
func (s *ListenerSuite) shutdownStarted(address string) bool {
	conn, err := new(net.Dialer).DialContext(s.T().Context(), "tcp", address)
	if err != nil {
		return true
	}

	_ = conn.Close()

	return false
}

// requireClosedByServer makes sure that the server closed conn: the read ends before its
// deadline, with the end of the stream or a connection reset.
func (s *ListenerSuite) requireClosedByServer(conn net.Conn) {
	s.Require().NoError(conn.SetReadDeadline(time.Now().Add(listenerTestWait)))

	_, err := io.Copy(io.Discard, conn)

	if netErr, ok := errors.AsType[net.Error](err); ok {
		s.Require().False(netErr.Timeout(), "the server did not close the connection")
	}
}

func (s *ListenerSuite) waitFor(event <-chan struct{}, message string) {
	select {
	case <-event:
	case <-time.After(listenerTestWait):
		s.FailNow(message)
	}
}

func (s *ListenerSuite) waitUntil(condition func() bool, message string) {
	s.Require().Eventually(condition, listenerTestWait, 5*time.Millisecond, message)
}
