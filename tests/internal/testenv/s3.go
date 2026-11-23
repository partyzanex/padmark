package testenv

import (
	"log/slog"
	"net"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/partyzanex/padmark/internal/infra/server"
	"github.com/partyzanex/padmark/internal/infra/storage/s3store"
)

// Values of the shared S3-compatible container. adobe/s3mock accepts any credentials, so these
// values are for the throwaway test container only.
const (
	S3AccessKey   = "padmark-test"
	S3SecretKey   = "padmark-test-secret"
	S3Region      = "us-east-1"
	s3Image       = "adobe/s3mock:3.12.0"
	s3Port        = "9090/tcp"
	s3StartupWait = time.Minute
)

// S3 is a new empty bucket on the S3-compatible container that the tests of one package share.
// Each bucket belongs to one caller, so the objects of two tests do not mix.
type S3 struct {
	// Endpoint is the URL of the container, http://host:port.
	Endpoint string
	// Bucket is the name of the bucket. It exists already.
	Bucket string
}

// S3Mock returns a new empty bucket on the shared adobe/s3mock container. The first call of the
// test process starts the container; the reaper stops it (see the package comment).
func S3Mock(t *testing.T) S3 {
	t.Helper()

	container, err := testcontainers.Run(t.Context(), s3Image,
		testcontainers.WithReuseByName(sharedName(s3Image)),
		testcontainers.WithExposedPorts(s3Port),
		testcontainers.WithWaitStrategy(wait.ForListeningPort(s3Port).WithStartupTimeout(s3StartupWait)),
	)
	require.NoError(t, err, "start %s", s3Image)

	hostPort, err := container.PortEndpoint(t.Context(), s3Port, "")
	require.NoError(t, err)

	bucket := S3{Endpoint: "http://" + hostPort, Bucket: "padmark-test-" + randomSuffix(t)}

	created, err := bucket.Store(t).EnsureBucket(t.Context())
	require.NoError(t, err)
	require.True(t, created, "the bucket %s is new", bucket.Bucket)

	return bucket
}

// Config returns the s3store configuration of the bucket with path-style addressing.
func (s S3) Config() *s3store.Config {
	return &s3store.Config{
		Endpoint: s.Endpoint, Bucket: s.Bucket, Region: S3Region,
		AccessKey: S3AccessKey, SecretKey: S3SecretKey, PathStyle: true,
	}
}

// Store returns the s3store.Store of the bucket. It accepts plain HTTP also on a remote Docker
// host, because the container is a throwaway test store.
func (s S3) Store(t *testing.T) *s3store.Store {
	t.Helper()

	store, err := s3store.New(s.Config(), slog.New(slog.DiscardHandler), s3store.AllowInsecureHTTP())
	require.NoError(t, err)

	return store
}

// ServerArgs returns the serve flags that turn file publishing on with the bucket.
func (s S3) ServerArgs() []string {
	return []string{"--s3-endpoint", s.Endpoint, "--s3-bucket", s.Bucket, "--s3-path-style"}
}

// ServerEnv returns the environment of a server process with the bucket, in the KEY=value form.
// The server refuses plain HTTP to a host that is not loopback. A remote Docker host gives such
// a host, so the environment then also permits plain HTTP.
func (s S3) ServerEnv() []string {
	env := []string{server.EnvS3AccessKey + "=" + S3AccessKey, server.EnvS3SecretKey + "=" + S3SecretKey}

	endpoint, err := url.Parse(s.Endpoint)
	if err != nil {
		return env
	}

	host := endpoint.Hostname()
	if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		env = append(env, server.EnvS3AllowInsecure+"=true")
	}

	return env
}
