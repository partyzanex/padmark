//go:build integration

package server_test

import (
	"bufio"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"

	"github.com/partyzanex/padmark/tests/internal/servertest"
)

const (
	// shutdownTimeout is the --shutdown-timeout of the stalled-request test. The open request
	// gets four fifths of it, then the server closes the connection.
	shutdownTimeout = 2 * time.Second
	shutdownDrain   = shutdownTimeout * 4 / 5
	// exitMargin is the time above the shutdown timeout for the process exit on a slow machine.
	exitMargin = 2 * time.Second
	// unusedStopLimit is the limit for a stop that has only an unused connection. It is below
	// the 5 seconds that net/http waits for such a connection.
	unusedStopLimit = 3 * time.Second
	clientWait      = 10 * time.Second
)

// ServerSuite starts the server binary of this repository and examines it from the outside.
// The suite builds the binary once; each test starts its own server processes.
type ServerSuite struct {
	suite.Suite

	binary string
}

func TestServerSuite(t *testing.T) {
	suite.Run(t, new(ServerSuite))
}

func (s *ServerSuite) SetupSuite() {
	s.binary = servertest.Build(s.T())
}

// TestSIGTERM_ClosesStalledRequestWithinTimeout reproduces the slow stop: a client sends a part
// of a request body and waits. The read timeout of the server is 30 seconds, so only the
// shutdown sequence can close this connection in time.
func (s *ServerSuite) TestSIGTERM_ClosesStalledRequestWithinTimeout() {
	process := servertest.Launch(s.T(), s.binary, s.T().TempDir(),
		"--shutdown-timeout", shutdownTimeout.String(), "--log-format", "text")

	conn := s.dial(process.BaseURL)
	s.write(conn, "POST /notes HTTP/1.1\r\nHost: test\r\nContent-Type: application/json\r\n"+
		"Content-Length: 1000\r\nExpect: 100-continue\r\n\r\n")

	// The server sends this line when the handler starts to read the body, so the request is
	// open in the handler before the signal.
	reader := bufio.NewReader(conn)
	s.Require().NoError(conn.SetReadDeadline(time.Now().Add(clientWait)))

	line, err := reader.ReadString('\n')
	s.Require().NoError(err)
	s.Require().Contains(line, "100 Continue")
	s.write(conn, `{"content":"abc`)

	started := time.Now()

	process.Terminate(s.T())

	exitCode, output := process.WaitExit(s.T(), shutdownTimeout+exitMargin)
	elapsed := time.Since(started)
	output = shutdownLog(output)

	s.GreaterOrEqual(elapsed, shutdownDrain, "the open request must get the drain time")
	s.Zero(exitCode, "the forced close is not a failure: %s", output)
	s.Contains(output, "signal=terminated")
	s.Contains(output, "listener=main active_requests=1")
	s.Contains(output, "shutdown: background work stopped")
	s.Contains(output, "shutdown: database closed")
	s.requireClosedByServer(conn, reader)
}

// TestSIGTERM_DoesNotWaitForUnusedConnection makes sure that a connection without a request,
// which a browser or a proxy opens in advance, does not hold the stop.
func (s *ServerSuite) TestSIGTERM_DoesNotWaitForUnusedConnection() {
	process := servertest.Launch(s.T(), s.binary, s.T().TempDir(), "--log-format", "text")

	unused := s.dial(process.BaseURL)
	// The server accepts connections in sequence, so a response on a later connection tells
	// that the server has the unused connection.
	s.Require().Equal(http.StatusOK, s.get(process.BaseURL+"/healthz"))

	process.Terminate(s.T())

	exitCode, output := process.WaitExit(s.T(), unusedStopLimit)
	output = shutdownLog(output)

	s.Zero(exitCode, output)
	s.Contains(output, "signal=terminated")
	s.NotContains(output, "level=WARN msg=\"shutdown")
	s.requireClosedByServer(unused, unused)
}

// --- helpers ---

// shutdownLog returns the lines of the server output that are about the stop: the shutdown
// records and the error that the process prints before a failed exit.
func shutdownLog(output string) string {
	var lines []string

	for line := range strings.Lines(output) {
		if strings.Contains(line, "shutdown") || strings.HasPrefix(line, "error:") {
			lines = append(lines, line)
		}
	}

	return strings.Join(lines, "")
}

// dial opens a client connection to the server. The test cleanup closes it.
func (s *ServerSuite) dial(baseURL string) *net.TCPConn {
	conn, err := new(net.Dialer).DialContext(s.T().Context(), "tcp", strings.TrimPrefix(baseURL, "http://"))
	s.Require().NoError(err)
	s.T().Cleanup(func() { _ = conn.Close() })

	tcpConn, ok := conn.(*net.TCPConn)
	s.Require().True(ok)

	return tcpConn
}

func (s *ServerSuite) write(conn io.Writer, data string) {
	_, err := io.WriteString(conn, data)
	s.Require().NoError(err)
}

// get sends one request on a new connection and returns the status code.
func (s *ServerSuite) get(url string) int {
	client := &http.Client{Timeout: clientWait, Transport: nil, CheckRedirect: nil, Jar: nil}

	request, err := http.NewRequestWithContext(s.T().Context(), http.MethodGet, url, http.NoBody)
	s.Require().NoError(err)

	// The connection must not stay open: an idle connection is not the subject of this test.
	request.Close = true

	response, err := client.Do(request)
	s.Require().NoError(err)
	s.Require().NoError(response.Body.Close())

	return response.StatusCode
}

// requireClosedByServer makes sure that the server side of conn is closed: the read ends before
// its deadline, with the end of the stream or a connection reset.
func (s *ServerSuite) requireClosedByServer(conn net.Conn, reader io.Reader) {
	s.Require().NoError(conn.SetReadDeadline(time.Now().Add(clientWait)))

	_, err := io.Copy(io.Discard, reader)

	if netErr, ok := errors.AsType[net.Error](err); ok {
		s.Require().False(netErr.Timeout(), "the server did not close the connection")
	}
}
