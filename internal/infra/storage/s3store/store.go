// Package s3store keeps file content in an S3-compatible object storage.
package s3store

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"github.com/partyzanex/padmark/internal/domain"
)

// partSize is the multipart part size for uploads of unknown length. The client keeps one part
// in memory for each upload, so this value sets the memory of each upload. 5 MiB is the S3
// minimum for a part that is not the last part.
const partSize = 5 << 20

// maxParts is the S3 limit of parts in one multipart upload.
const maxParts = 10_000

// MaxObjectSize is the largest object that Put can write: partSize times maxParts, about
// 48.8 GiB. The encrypted object is a little larger than the file, so the file size limit
// must stay below this value.
const MaxObjectSize int64 = partSize * maxParts

// redactedKey replaces the object key in error texts.
const redactedKey = "<object-key>"

// tcpKeepAlive is the keep-alive period of the S3 connections, the same as in minio-go.
const tcpKeepAlive = 30 * time.Second

// clientLimits bounds the time of one S3 request, so that a storage server that stops
// responding does not hold an HTTP request of padmark for a long time. There is no total
// request timeout: an upload or a download of a large file can take a long time when the
// storage is healthy.
type clientLimits struct {
	// dial limits the TCP connection setup.
	dial time.Duration
	// tlsHandshake limits the TLS handshake after the connection.
	tlsHandshake time.Duration
	// responseHeader limits the wait for the response headers after the request is sent.
	responseHeader time.Duration
	// attempts is the number of tries of one request. minio-go retries network errors, and
	// each try can use the full limits above.
	attempts int
}

// The production limits. The minio-go defaults are a 30 second dial, a one minute response
// header wait and 10 tries; a server that accepts connections but does not respond then holds
// a request for about 10 minutes. With these values the worst case is 3 x (10 + 10 + 30)
// seconds plus the short retry pauses.
const (
	dialTimeout           = 10 * time.Second
	tlsHandshakeTimeout   = 10 * time.Second
	responseHeaderTimeout = 30 * time.Second
	requestAttempts       = 3
)

func defaultLimits() clientLimits {
	return clientLimits{
		dial:           dialTimeout,
		tlsHandshake:   tlsHandshakeTimeout,
		responseHeader: responseHeaderTimeout,
		attempts:       requestAttempts,
	}
}

var (
	errEndpointScheme = errors.New("s3 endpoint must be an http:// or https:// URL with a host")
	// errEndpointParts does not show the endpoint: user info in the URL can be a secret.
	errEndpointParts    = errors.New("s3 endpoint must not have user info, a path, a query or a fragment")
	errInsecureEndpoint = errors.New("s3 endpoint uses plain http:// to a host that is not loopback; " +
		"use https:// or permit plain http explicitly")
	errBucketMissing = errors.New("s3 bucket does not exist")
	errBucketCreate  = errors.New("s3 bucket does not exist and the server cannot create it " +
		"(the key needs s3:CreateBucket)")
)

// bucketOwnedByYou is the S3 error code of a create request for a bucket that the same account
// already has.
const bucketOwnedByYou = "BucketAlreadyOwnedByYou"

// Config holds the connection settings. AccessKey and SecretKey are secrets: the composition
// root reads them only from environment variables.
type Config struct {
	// Endpoint is the base URL of the storage, for example https://s3.example.com:9000.
	Endpoint  string
	Bucket    string
	Region    string
	AccessKey string
	SecretKey string
	// PathStyle selects https://host/bucket/key addresses instead of https://bucket.host/key.
	// Most self-hosted servers need it.
	PathStyle bool
}

// Option changes an optional setting of New.
type Option func(*options)

type options struct {
	allowInsecure bool
}

// AllowInsecureHTTP permits a plain http:// endpoint on a host that is not loopback. Without
// it, New refuses such an endpoint, because the signed requests and the object metadata then
// cross the network without TLS. New then logs a warning.
func AllowInsecureHTTP() Option {
	return func(opts *options) { opts.allowInsecure = true }
}

// Store implements files.ObjectStore with minio-go. It is safe for concurrent use.
type Store struct {
	client *minio.Client
	bucket string
	region string
}

// New returns a Store for cfg. It does not connect to the server; use EnsureBucket at startup.
func New(cfg *Config, log *slog.Logger, opts ...Option) (*Store, error) {
	return newStore(cfg, log, defaultLimits(), opts...)
}

// newStore is New with explicit limits; tests use short limits.
func newStore(cfg *Config, log *slog.Logger, limits clientLimits, opts ...Option) (*Store, error) {
	var settings options
	for _, opt := range opts {
		opt(&settings)
	}

	endpoint, err := parseEndpoint(cfg.Endpoint)
	if err != nil {
		return nil, err
	}

	if endpoint.Scheme == "http" && !isLoopback(endpoint.Hostname()) {
		if !settings.allowInsecure {
			return nil, errInsecureEndpoint
		}

		log.Warn("s3 endpoint uses plain http: file content is encrypted, but request signatures "+
			"and metadata cross the network without TLS", "host", endpoint.Host)
	}

	// new() and field assignments: the other minio options keep their zero values, so the
	// library defaults apply.
	clientOpts := new(minio.Options)
	clientOpts.Creds = credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, "")
	clientOpts.Secure = endpoint.Scheme == "https"
	clientOpts.Region = cfg.Region
	clientOpts.BucketLookup = bucketLookup(cfg)
	clientOpts.MaxRetries = limits.attempts

	clientOpts.Transport, err = newTransport(clientOpts.Secure, limits)
	if err != nil {
		return nil, err
	}

	client, err := minio.New(endpoint.Host, clientOpts)
	if err != nil {
		return nil, fmt.Errorf("new s3 client: %w", err)
	}

	return &Store{client: client, bucket: cfg.Bucket, region: cfg.Region}, nil
}

// parseEndpoint accepts only scheme://host[:port] with an optional "/". minio-go uses only the
// host, so it ignores the other URL parts. User info in the URL is a secret that ps shows, and
// a path or a query is a configuration error that must not stay hidden.
func parseEndpoint(raw string) (*url.URL, error) {
	endpoint, err := url.Parse(raw)
	if err != nil || endpoint.Host == "" || (endpoint.Scheme != "https" && endpoint.Scheme != "http") {
		return nil, errEndpointScheme
	}

	if hasExtraParts(endpoint) {
		return nil, errEndpointParts
	}

	return endpoint, nil
}

// hasExtraParts reports whether endpoint has user info, a path other than "/", a query or a
// fragment.
func hasExtraParts(endpoint *url.URL) bool {
	hasPath := (endpoint.Path != "" && endpoint.Path != "/") || endpoint.RawPath != ""
	hasQuery := endpoint.RawQuery != "" || endpoint.ForceQuery

	return endpoint.User != nil || hasPath || hasQuery || endpoint.Fragment != ""
}

// bucketLookup returns the minio-go address style. minio.BucketLookupAuto selects the virtual
// host style only for Amazon, Google and Aliyun hosts, so PathStyle false uses
// minio.BucketLookupDNS: the operator asked for the virtual host style.
func bucketLookup(cfg *Config) minio.BucketLookupType {
	if cfg.PathStyle {
		return minio.BucketLookupPath
	}

	return minio.BucketLookupDNS
}

// EnsureBucket makes sure that the bucket exists and that the credentials give access to it.
// It creates a missing bucket in the configured region; the key then needs s3:CreateBucket.
// created is true only when this call made the bucket.
func (s *Store) EnsureBucket(ctx context.Context) (bool, error) {
	exists, err := s.client.BucketExists(ctx, s.bucket)
	if err != nil {
		return false, fmt.Errorf("s3 bucket check: %w", err)
	}

	if exists {
		return false, nil
	}

	// new() and a field assignment: the other options keep the library defaults.
	opts := new(minio.MakeBucketOptions)
	opts.Region = s.region

	err = s.client.MakeBucket(ctx, s.bucket, *opts)
	if err == nil {
		return true, nil
	}

	// Another padmark instance can create the bucket between the check and this call.
	if minio.ToErrorResponse(err).Code == bucketOwnedByYou {
		return false, nil
	}

	return false, fmt.Errorf("%w: %q: %w", errBucketCreate, s.bucket, err)
}

// Put writes body to key. The length of body is not known, so the client uses a multipart
// upload with parts of partSize.
func (s *Store) Put(ctx context.Context, key string, body io.Reader) error {
	opts := new(minio.PutObjectOptions)
	opts.ContentType = "application/octet-stream"
	opts.PartSize = partSize

	_, err := s.client.PutObject(ctx, s.bucket, key, body, -1, *opts)
	if err != nil {
		return fmt.Errorf("s3 put object: %w", redact(key, err))
	}

	return nil
}

// Stat makes sure that key exists. It returns domain.ErrNotFound when it does not.
//
// A HEAD response has no body, so minio-go gives NoSuchKey for each 404, also when the bucket
// is missing. Stat then checks the bucket once: a missing bucket is a storage failure, not a
// missing file, and the caller must not answer 404 for it.
func (s *Store) Stat(ctx context.Context, key string) error {
	var opts minio.StatObjectOptions

	_, err := s.client.StatObject(ctx, s.bucket, key, opts)
	if err == nil {
		return nil
	}

	mapped := mapObjectError("s3 stat object", key, err)
	if !errors.Is(mapped, domain.ErrNotFound) {
		return mapped
	}

	exists, bucketErr := s.client.BucketExists(ctx, s.bucket)
	if bucketErr != nil {
		return fmt.Errorf("s3 stat object: bucket check: %w", bucketErr)
	}

	if !exists {
		return fmt.Errorf("s3 stat object: %w: %q", errBucketMissing, s.bucket)
	}

	return domain.ErrNotFound
}

// WriteTo copies the content of key to dst. It returns domain.ErrNotFound when key does not
// exist.
func (s *Store) WriteTo(ctx context.Context, key string, dst io.Writer) error {
	var opts minio.GetObjectOptions

	obj, err := s.client.GetObject(ctx, s.bucket, key, opts)
	if err != nil {
		return mapObjectError("s3 get object", key, err)
	}

	_, copyErr := io.Copy(dst, obj)
	closeErr := obj.Close()

	if copyErr != nil {
		return mapObjectError("s3 read object", key, errors.Join(copyErr, closeErr))
	}

	if closeErr != nil {
		return fmt.Errorf("s3 close object: %w", redact(key, closeErr))
	}

	return nil
}

// Delete removes key. S3 does not return an error for a key that does not exist.
func (s *Store) Delete(ctx context.Context, key string) error {
	var opts minio.RemoveObjectOptions

	err := s.client.RemoveObject(ctx, s.bucket, key, opts)
	if err != nil {
		return fmt.Errorf("s3 remove object: %w", redact(key, err))
	}

	return nil
}

// isLoopback reports whether host is "localhost" or a loopback IP address. A different host
// name, also a Compose service name, needs Config.AllowInsecure for plain http.
func isLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}

	ip := net.ParseIP(host)

	return ip != nil && ip.IsLoopback()
}

// mapObjectError returns domain.ErrNotFound for a missing key. errors.As finds the S3 error
// also inside a joined read and close error.
func mapObjectError(operation, key string, err error) error {
	var resp minio.ErrorResponse
	if errors.As(err, &resp) && resp.Code == minio.NoSuchKey {
		return domain.ErrNotFound
	}

	return fmt.Errorf("%s: %w", operation, redact(key, err))
}

// newTransport returns the minio-go default transport with the timeouts of limits. The default
// transport keeps its TLS settings, the SSL_CERT_FILE support and the disabled compression.
func newTransport(secure bool, limits clientLimits) (*http.Transport, error) {
	transport, err := minio.DefaultTransport(secure)
	if err != nil {
		return nil, fmt.Errorf("s3 transport: %w", err)
	}

	dialer := new(net.Dialer)
	dialer.Timeout = limits.dial
	dialer.KeepAlive = tcpKeepAlive

	transport.DialContext = dialer.DialContext
	transport.TLSHandshakeTimeout = limits.tlsHandshake
	transport.ResponseHeaderTimeout = limits.responseHeader

	return transport, nil
}

// redactedError is an error whose text does not contain the object key. Unwrap gives the
// original error, so errors.Is and errors.As continue to operate.
type redactedError struct {
	err error
	msg string
}

func (e *redactedError) Error() string { return e.msg }

func (e *redactedError) Unwrap() error { return e.err }

// redact removes key from the text of err. A network error of minio-go contains the request
// URL, and the URL contains the object key. The callers write these errors to the logs, and
// the key with the bucket lets a reader of the logs find the content of one file.
func redact(key string, err error) error {
	if err == nil || key == "" {
		return err
	}

	msg := err.Error()
	if !strings.Contains(msg, key) {
		return err
	}

	return &redactedError{err: err, msg: strings.ReplaceAll(msg, key, redactedKey)}
}
