package s3store

import (
	"bytes"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/stretchr/testify/suite"

	"github.com/partyzanex/padmark/internal/domain"
)

// Throwaway values for the local test server; it does not check signatures.
const (
	fakeBucket    = "bucket"
	fakeObjectKey = "key-8c41f2aa"
)

// ConfigSuite checks the endpoint rules of New. New does not connect, so no server is necessary.
type ConfigSuite struct {
	suite.Suite

	logs *bytes.Buffer
	log  *slog.Logger
}

func TestConfigSuite(t *testing.T) {
	suite.Run(t, new(ConfigSuite))
}

func (s *ConfigSuite) SetupTest() {
	s.logs = new(bytes.Buffer)
	s.log = slog.New(slog.NewTextHandler(s.logs, nil))
}

func (s *ConfigSuite) TestRejectsBadScheme() {
	for _, endpoint := range []string{"", "s3.example.com", "ftp://s3.example.com", "https://", "http:host"} {
		_, err := New(s.config(endpoint), s.log)

		s.ErrorIs(err, errEndpointScheme, endpoint)
	}
}

func (s *ConfigSuite) TestRejectsExtraURLParts() {
	endpoints := []string{
		"https://user:pass-6d0e@s3.example.com",
		"https://user@s3.example.com",
		"https://s3.example.com/bucket",
		"https://s3.example.com/?x=1",
		"https://s3.example.com?",
		"https://s3.example.com#frag",
	}

	for _, endpoint := range endpoints {
		_, err := New(s.config(endpoint), s.log)

		s.Require().ErrorIs(err, errEndpointParts, endpoint)
		s.NotContains(err.Error(), "pass-6d0e", "the error must not show user info")
	}
}

func (s *ConfigSuite) TestAcceptsHostWithTrailingSlash() {
	for _, endpoint := range []string{"https://s3.example.com", "https://s3.example.com:9000/"} {
		_, err := New(s.config(endpoint), s.log)

		s.Require().NoError(err, endpoint)
	}
}

func (s *ConfigSuite) TestRefusesRemotePlainHTTP() {
	for _, endpoint := range []string{"http://s3:9000", "http://s3.example.com", "http://10.0.0.5:9000"} {
		_, err := New(s.config(endpoint), s.log)

		s.ErrorIs(err, errInsecureEndpoint, endpoint)
	}
}

func (s *ConfigSuite) TestAllowInsecureAcceptsRemotePlainHTTPWithWarning() {
	_, err := New(s.config("http://s3:9000"), s.log, AllowInsecureHTTP())

	s.Require().NoError(err)
	s.Contains(s.logs.String(), "level=WARN")
	s.Contains(s.logs.String(), "plain http")
}

func (s *ConfigSuite) TestLoopbackPlainHTTPNeedsNoPermission() {
	endpoints := []string{"http://localhost:9000", "http://LOCALHOST:9000", "http://127.0.0.1:9000", "http://[::1]:9000"}

	for _, endpoint := range endpoints {
		_, err := New(s.config(endpoint), s.log)

		s.Require().NoError(err, endpoint)
	}

	s.Empty(s.logs.String(), "a loopback endpoint gives no warning")
}

func (s *ConfigSuite) TestBucketLookup() {
	cfg := s.config("https://s3.example.com")
	s.Equal(minio.BucketLookupPath, bucketLookup(cfg))

	cfg.PathStyle = false
	s.Equal(minio.BucketLookupDNS, bucketLookup(cfg), "false must select the virtual host style for each host")
}

func (s *ConfigSuite) TestPartSizeIsTheS3Minimum() {
	s.Equal(5<<20, partSize)
	s.Equal(int64(5<<20)*10_000, MaxObjectSize)
}

// --- helpers ---

func (s *ConfigSuite) config(endpoint string) *Config {
	return &Config{
		Endpoint:  endpoint,
		Bucket:    fakeBucket,
		Region:    "us-east-1",
		AccessKey: "test-access",
		SecretKey: "test-secret",
		PathStyle: true,
	}
}

// ResponseSuite runs the Store against a local server that gives fixed S3 status codes.
type ResponseSuite struct {
	suite.Suite

	server       *httptest.Server
	bucketStatus atomic.Int32
	objectStatus atomic.Int32
	// createStatus and createCode give the response to PUT /bucket; createCalls counts them.
	createStatus atomic.Int32
	createCode   atomic.Value
	createCalls  atomic.Int32
	store        *Store
}

func TestResponseSuite(t *testing.T) {
	suite.Run(t, new(ResponseSuite))
}

func (s *ResponseSuite) SetupTest() {
	s.bucketStatus.Store(http.StatusOK)
	s.objectStatus.Store(http.StatusOK)
	s.createStatus.Store(http.StatusOK)
	s.createCode.Store("")
	s.createCalls.Store(0)
	s.server = httptest.NewServer(http.HandlerFunc(s.serve))

	cfg := Config{
		Endpoint:  s.server.URL,
		Bucket:    fakeBucket,
		Region:    "us-east-1",
		AccessKey: "test-access",
		SecretKey: "test-secret",
		PathStyle: true,
	}

	var err error

	s.store, err = newStore(&cfg, slog.New(slog.DiscardHandler), clientLimits{
		dial: time.Second, tlsHandshake: time.Second, responseHeader: 5 * time.Second, attempts: 1,
	})
	s.Require().NoError(err)
}

func (s *ResponseSuite) TearDownTest() {
	s.server.Close()
}

func (s *ResponseSuite) TestEnsureBucketExists() {
	created, err := s.store.EnsureBucket(s.T().Context())

	s.Require().NoError(err)
	s.False(created)
	s.Zero(s.createCalls.Load(), "an existing bucket must not be created again")
}

func (s *ResponseSuite) TestEnsureBucketCheckDenied() {
	s.bucketStatus.Store(http.StatusForbidden)

	_, err := s.store.EnsureBucket(s.T().Context())

	s.Require().Error(err)
	s.Contains(err.Error(), "s3 bucket check")
	s.Zero(s.createCalls.Load(), "a failed check must not create the bucket")
}

func (s *ResponseSuite) TestEnsureBucketCreatesMissing() {
	s.bucketStatus.Store(http.StatusNotFound)

	created, err := s.store.EnsureBucket(s.T().Context())

	s.Require().NoError(err)
	s.True(created)
	s.Equal(int32(1), s.createCalls.Load())
}

func (s *ResponseSuite) TestEnsureBucketCreateDenied() {
	s.bucketStatus.Store(http.StatusNotFound)
	s.createStatus.Store(http.StatusForbidden)
	s.createCode.Store("AccessDenied")

	created, err := s.store.EnsureBucket(s.T().Context())

	s.Require().ErrorIs(err, errBucketCreate)
	s.False(created)
	s.Contains(err.Error(), "s3:CreateBucket")
	s.Contains(err.Error(), fakeBucket)
}

// TestEnsureBucketCreatedByAnotherInstance: a different instance made the bucket after the
// check, so the create request gets BucketAlreadyOwnedByYou.
func (s *ResponseSuite) TestEnsureBucketCreatedByAnotherInstance() {
	s.bucketStatus.Store(http.StatusNotFound)
	s.createStatus.Store(http.StatusConflict)
	s.createCode.Store(bucketOwnedByYou)

	created, err := s.store.EnsureBucket(s.T().Context())

	s.Require().NoError(err)
	s.False(created)
}

func (s *ResponseSuite) TestStatObjectExists() {
	s.NoError(s.store.Stat(s.T().Context(), fakeObjectKey))
}

func (s *ResponseSuite) TestStatObjectMissing() {
	s.objectStatus.Store(http.StatusNotFound)

	s.ErrorIs(s.store.Stat(s.T().Context(), fakeObjectKey), domain.ErrNotFound)
}

func (s *ResponseSuite) TestStatBucketMissingIsNotAMissingFile() {
	s.objectStatus.Store(http.StatusNotFound)
	s.bucketStatus.Store(http.StatusNotFound)

	err := s.store.Stat(s.T().Context(), fakeObjectKey)

	s.Require().ErrorIs(err, errBucketMissing)
	s.NotErrorIs(err, domain.ErrNotFound)
}

func (s *ResponseSuite) TestStatBucketCheckFails() {
	s.objectStatus.Store(http.StatusNotFound)
	s.bucketStatus.Store(http.StatusForbidden)

	err := s.store.Stat(s.T().Context(), fakeObjectKey)

	s.Require().Error(err)
	s.Require().NotErrorIs(err, domain.ErrNotFound)
	s.Contains(err.Error(), "bucket check")
}

func (s *ResponseSuite) TestStatAccessDenied() {
	s.objectStatus.Store(http.StatusForbidden)

	err := s.store.Stat(s.T().Context(), fakeObjectKey)

	s.Require().Error(err)
	s.Require().NotErrorIs(err, domain.ErrNotFound)
	s.NotContains(err.Error(), fakeObjectKey)
}

// --- helpers ---

// serve answers HEAD /bucket/ with bucketStatus, PUT /bucket/ with createStatus and HEAD
// /bucket/key with objectStatus.
func (s *ResponseSuite) serve(w http.ResponseWriter, r *http.Request) {
	path := strings.Trim(r.URL.Path, "/")

	switch {
	case r.Method == http.MethodHead && path == fakeBucket:
		w.WriteHeader(int(s.bucketStatus.Load()))
	case r.Method == http.MethodPut && path == fakeBucket:
		s.createCalls.Add(1)
		s.writeCreateResponse(w)
	case r.Method == http.MethodHead && path == fakeBucket+"/"+fakeObjectKey:
		w.Header().Set("Last-Modified", time.Now().UTC().Format(http.TimeFormat))
		w.Header().Set("ETag", `"0"`)
		w.WriteHeader(int(s.objectStatus.Load()))
	default:
		w.WriteHeader(http.StatusNotImplemented)
	}
}

// writeCreateResponse writes createStatus with an S3 error body that has createCode.
func (s *ResponseSuite) writeCreateResponse(w http.ResponseWriter) {
	status := int(s.createStatus.Load())
	code, _ := s.createCode.Load().(string)

	if code == "" {
		w.WriteHeader(status)

		return
	}

	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(status)
	_, _ = fmt.Fprintf(w, "<Error><Code>%s</Code><Message>test</Message><BucketName>%s</BucketName></Error>",
		code, fakeBucket)
}
