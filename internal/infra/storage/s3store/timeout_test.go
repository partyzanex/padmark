package s3store

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"
)

// hangKey is the object key of the requests to the hung server. The errors must not contain it.
const hangKey = "key-5d1e0b77"

// TimeoutSuite runs the Store against a server that accepts connections and never responds.
type TimeoutSuite struct {
	suite.Suite

	listener net.Listener
	conns    []net.Conn
	mu       sync.Mutex
	done     sync.WaitGroup
	store    *Store
}

func TestTimeoutSuite(t *testing.T) {
	suite.Run(t, new(TimeoutSuite))
}

func (s *TimeoutSuite) SetupTest() {
	var err error

	s.listener, err = net.Listen("tcp", "127.0.0.1:0")
	s.Require().NoError(err)

	s.conns = nil
	s.done.Go(s.acceptAndHold)

	cfg := Config{
		Endpoint:  "http://" + s.listener.Addr().String(),
		Bucket:    "bucket",
		Region:    "us-east-1",
		AccessKey: "test-access",
		SecretKey: "test-secret",
		PathStyle: true,
	}

	s.store, err = newStore(&cfg, slog.New(slog.DiscardHandler), clientLimits{
		dial: time.Second, tlsHandshake: time.Second, responseHeader: 200 * time.Millisecond, attempts: 1,
	})
	s.Require().NoError(err)
}

func (s *TimeoutSuite) TearDownTest() {
	s.Require().NoError(s.listener.Close())

	s.mu.Lock()
	for _, conn := range s.conns {
		_ = conn.Close()
	}
	s.mu.Unlock()

	s.done.Wait()
}

// acceptAndHold accepts each connection and keeps it open without a response.
func (s *TimeoutSuite) acceptAndHold() {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return
		}

		s.mu.Lock()
		s.conns = append(s.conns, conn)
		s.mu.Unlock()
	}
}

// within runs call and fails the test if call does not return in limit.
func (s *TimeoutSuite) within(limit time.Duration, call func() error) error {
	result := make(chan error, 1)

	go func() { result <- call() }()

	select {
	case err := <-result:
		return err
	case <-time.After(limit):
		s.FailNow(fmt.Sprintf("the call did not return in %s", limit))

		return nil
	}
}

// TestHungServer: each operation fails after the response header timeout, also when the
// request context has no deadline, and the error text does not contain the object key.
func (s *TimeoutSuite) TestHungServer() {
	ctx := s.T().Context()

	operations := map[string]func() error{
		"ping":   func() error { return s.store.Ping(ctx) },
		"stat":   func() error { return s.store.Stat(ctx, hangKey) },
		"get":    func() error { return s.store.WriteTo(ctx, hangKey, new(bytes.Buffer)) },
		"put":    func() error { return s.store.Put(ctx, hangKey, strings.NewReader("data")) },
		"delete": func() error { return s.store.Delete(ctx, hangKey) },
	}

	for name, operation := range operations {
		err := s.within(10*time.Second, operation)

		s.Require().Error(err, name)
		s.NotContains(err.Error(), hangKey, name)
		s.Contains(err.Error(), "timeout awaiting response headers", name)

		// minio-go puts the request URL into the error; the URL of an object request has the key.
		if name != "ping" {
			s.Contains(err.Error(), redactedKey, name)
		}
	}
}

func TestRedact(t *testing.T) {
	t.Parallel()

	cause := errors.New(`Put "http://s3/bucket/` + hangKey + `?uploads": EOF`)
	redacted := redact(hangKey, cause)

	if strings.Contains(redacted.Error(), hangKey) || !strings.Contains(redacted.Error(), redactedKey) {
		t.Fatalf("redact: unexpected text %q", redacted.Error())
	}

	if !errors.Is(redacted, cause) {
		t.Fatal("redact: the original error is not in the chain")
	}

	plain := errors.New("connection refused")
	if !errors.Is(redact(hangKey, plain), plain) || redact(hangKey, nil) != nil {
		t.Fatal("redact: an error without the key must stay the same")
	}
}
