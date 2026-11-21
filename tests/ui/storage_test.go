package ui_test

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// The S3 mock storage of the file scenarios. The values are for the throwaway container only:
// adobe/s3mock creates the bucket at startup and does not check the credentials.
const (
	s3Image      = "adobe/s3mock:3.12.0"
	s3Port       = "9090/tcp"
	s3Bucket     = "padmark-ui"
	s3AccessKey  = "padmark-ui-access-key"
	s3SigningKey = "padmark-ui-signing-key"
	// maxFileSize is the upload limit of the file servers. It is small, so that a test file can
	// be larger than the limit without a long upload.
	maxFileSize = 1 << 20
	// s3StartTimeout bounds the first start, which can also pull the image.
	s3StartTimeout = 3 * time.Minute
)

// objectStorage is one S3 mock container for all file scenarios of a run. The servers share one
// bucket: each file gets a random object key, so parallel servers do not touch the same object.
// The container starts at the first file scenario, so a run without file scenarios needs no
// Docker. Testcontainers removes the container also when the test process stops unexpectedly.
type objectStorage struct {
	once      sync.Once
	container testcontainers.Container
	endpoint  string
	err       error
}

// start starts the container once and returns its endpoint, for example http://localhost:55001.
func (storage *objectStorage) start() (string, error) {
	storage.once.Do(func() {
		// The container lives for the full run, so its context is not the context of one test.
		ctx, cancel := context.WithTimeout(context.Background(), s3StartTimeout)
		defer cancel()

		storage.container, storage.err = testcontainers.Run(ctx, s3Image,
			testcontainers.WithEnv(map[string]string{"initialBuckets": s3Bucket}),
			testcontainers.WithExposedPorts(s3Port),
			testcontainers.WithWaitStrategy(wait.ForListeningPort(s3Port).WithStartupTimeout(s3StartTimeout)),
		)
		if storage.err != nil {
			storage.err = fmt.Errorf("start %s: %w", s3Image, storage.err)

			return
		}

		storage.endpoint, storage.err = storage.container.PortEndpoint(ctx, s3Port, "http")
		if storage.err != nil {
			storage.err = fmt.Errorf("read the %s endpoint: %w", s3Image, storage.err)
		}
	})

	return storage.endpoint, storage.err
}

// stop removes the container if a file scenario started it.
func (storage *objectStorage) stop() error {
	if storage.container == nil {
		return nil
	}

	err := testcontainers.TerminateContainer(storage.container)
	if err != nil {
		return fmt.Errorf("stop %s: %w", s3Image, err)
	}

	return nil
}
