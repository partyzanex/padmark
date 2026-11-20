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
// in memory for each upload, so this value limits memory use. 16 MiB with the S3 limit of
// 10 000 parts permits objects up to 160 GiB.
const partSize = 16 << 20

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
	errBucketMissing  = errors.New("s3 bucket does not exist")
)

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

// Store implements files.ObjectStore with minio-go. It is safe for concurrent use.
type Store struct {
	client *minio.Client
	bucket string
}

// New returns a Store for cfg. It does not connect to the server; use Ping at startup.
func New(cfg *Config, log *slog.Logger) (*Store, error) {
	return newStore(cfg, log, defaultLimits())
}

// newStore is New with explicit limits; tests use short limits.
func newStore(cfg *Config, log *slog.Logger, limits clientLimits) (*Store, error) {
	endpoint, err := url.Parse(cfg.Endpoint)
	if err != nil || endpoint.Host == "" || (endpoint.Scheme != "https" && endpoint.Scheme != "http") {
		return nil, errEndpointScheme
	}

	if endpoint.Scheme == "http" && !isLoopback(endpoint.Hostname()) {
		log.Warn("s3 endpoint uses plain http: file content is encrypted, but request signatures "+
			"and metadata cross the network without TLS", "host", endpoint.Host)
	}

	// new() and field assignments: the other minio options keep their zero values, so the
	// library defaults apply.
	opts := new(minio.Options)
	opts.Creds = credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, "")
	opts.Secure = endpoint.Scheme == "https"
	opts.Region = cfg.Region
	opts.BucketLookup = minio.BucketLookupAuto
	opts.MaxRetries = limits.attempts

	if cfg.PathStyle {
		opts.BucketLookup = minio.BucketLookupPath
	}

	opts.Transport, err = newTransport(opts.Secure, limits)
	if err != nil {
		return nil, err
	}

	client, err := minio.New(endpoint.Host, opts)
	if err != nil {
		return nil, fmt.Errorf("new s3 client: %w", err)
	}

	return &Store{client: client, bucket: cfg.Bucket}, nil
}

// Ping makes sure that the bucket exists and that the credentials give access to it.
func (s *Store) Ping(ctx context.Context) error {
	exists, err := s.client.BucketExists(ctx, s.bucket)
	if err != nil {
		return fmt.Errorf("s3 bucket check: %w", err)
	}

	if !exists {
		return fmt.Errorf("%w: %q", errBucketMissing, s.bucket)
	}

	return nil
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
func (s *Store) Stat(ctx context.Context, key string) error {
	var opts minio.StatObjectOptions

	_, err := s.client.StatObject(ctx, s.bucket, key, opts)
	if err != nil {
		return mapObjectError("s3 stat object", key, err)
	}

	return nil
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

func isLoopback(host string) bool {
	if host == "localhost" {
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
