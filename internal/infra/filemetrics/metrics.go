// Package filemetrics implements files.Metrics with Prometheus. The names obey the
// {namespace}_{subsystem}_{metric_name}_{unit} rule with namespace "padmark" and subsystem
// "files".
package filemetrics

import (
	"fmt"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/partyzanex/padmark/internal/usecases/files"
)

const (
	namespace = "padmark"
	subsystem = "files"
	mib       = 1 << 20
	// bucketFactor and bucketCount give six upload buckets from 1 MiB, each four times larger.
	bucketFactor = 4
	bucketCount  = 6
)

// uploadBuckets returns the upload size limits in bytes: 1, 4, 16, 64 and 256 MiB, and 1 GiB.
// The default buckets are for durations, so they do not fit sizes.
func uploadBuckets() []float64 {
	return prometheus.ExponentialBuckets(mib, bucketFactor, bucketCount)
}

// deleteReasons and storageOps return all label values. New creates their series at zero, so a
// dashboard shows a rate for each value before its first event.
func deleteReasons() []files.DeleteReason {
	return []files.DeleteReason{files.DeleteReasonUser, files.DeleteReasonBurn, files.DeleteReasonExpired}
}

func storageOps() []files.StorageOp {
	return []files.StorageOp{files.StorageOpPut, files.StorageOpStat, files.StorageOpGet, files.StorageOpDelete}
}

// Metrics implements files.Metrics with Prometheus collectors. It is safe for concurrent use.
type Metrics struct {
	uploads       prometheus.Counter
	downloads     prometheus.Counter
	deleted       *prometheus.CounterVec
	uploadBytes   prometheus.Histogram
	storageErrors *prometheus.CounterVec
}

// New returns a Metrics that is not registered. Use NewRegistered for the server; New is for
// tests that do not need a registry.
func New() *Metrics {
	metrics := &Metrics{
		uploads:   prometheus.NewCounter(counterOpts("uploads_total", "Total number of uploaded files.")),
		downloads: prometheus.NewCounter(counterOpts("downloads_total", "Total number of prepared file downloads.")),
		deleted: prometheus.NewCounterVec(
			counterOpts("deleted_total", "Total number of deleted files by reason: user, burn or expired."),
			[]string{"reason"}),
		uploadBytes: prometheus.NewHistogram(uploadBytesOpts()),
		storageErrors: prometheus.NewCounterVec(
			counterOpts("storage_errors_total",
				"Total number of object storage failures by operation: put, stat, get or delete."),
			[]string{"op"}),
	}

	for _, reason := range deleteReasons() {
		metrics.deleted.WithLabelValues(string(reason))
	}

	for _, op := range storageOps() {
		metrics.storageErrors.WithLabelValues(string(op))
	}

	return metrics
}

// NewRegistered returns a Metrics with its collectors registered in reg. It returns an error
// when reg already has a collector with the same name.
func NewRegistered(reg prometheus.Registerer) (*Metrics, error) {
	metrics := New()

	for _, collector := range metrics.collectors() {
		err := reg.Register(collector)
		if err != nil {
			return nil, fmt.Errorf("register file metric: %w", err)
		}
	}

	return metrics, nil
}

// IncUploads implements files.Metrics.
func (m *Metrics) IncUploads() { m.uploads.Inc() }

// ObserveUploadBytes implements files.Metrics.
func (m *Metrics) ObserveUploadBytes(size int64) { m.uploadBytes.Observe(float64(size)) }

// IncDownloads implements files.Metrics.
func (m *Metrics) IncDownloads() { m.downloads.Inc() }

// IncDeleted implements files.Metrics.
func (m *Metrics) IncDeleted(reason files.DeleteReason) {
	m.deleted.WithLabelValues(string(reason)).Inc()
}

// IncStorageErrors implements files.Metrics.
func (m *Metrics) IncStorageErrors(op files.StorageOp) {
	m.storageErrors.WithLabelValues(string(op)).Inc()
}

func (m *Metrics) collectors() []prometheus.Collector {
	return []prometheus.Collector{m.uploads, m.downloads, m.deleted, m.uploadBytes, m.storageErrors}
}

// counterOpts uses new() and field assignments: the other options keep their zero values, so
// there is no unit annotation and no constant label.
func counterOpts(name, help string) prometheus.CounterOpts {
	opts := new(prometheus.CounterOpts)
	opts.Namespace = namespace
	opts.Subsystem = subsystem
	opts.Name = name
	opts.Help = help

	return *opts
}

// uploadBytesOpts uses new() for the same reason as counterOpts: the native histogram options
// stay off.
func uploadBytesOpts() prometheus.HistogramOpts {
	opts := new(prometheus.HistogramOpts)
	opts.Namespace = namespace
	opts.Subsystem = subsystem
	opts.Name = "upload_bytes"
	opts.Help = "Size of uploaded files in bytes."
	opts.Buckets = uploadBuckets()

	return *opts
}
