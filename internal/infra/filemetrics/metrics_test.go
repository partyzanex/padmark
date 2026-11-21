package filemetrics

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/suite"

	"github.com/partyzanex/padmark/internal/infra/linkmetrics"
	"github.com/partyzanex/padmark/internal/usecases/files"
)

type MetricsSuite struct {
	suite.Suite

	metrics *Metrics
}

func TestMetricsSuite(t *testing.T) {
	suite.Run(t, new(MetricsSuite))
}

func (s *MetricsSuite) SetupTest() {
	s.metrics = New()
}

func (s *MetricsSuite) TestCounters() {
	s.metrics.IncUploads()
	s.metrics.IncUploads()
	s.metrics.IncDownloads()

	s.InDelta(2, s.value(s.metrics.uploads), 0)
	s.InDelta(1, s.value(s.metrics.downloads), 0)
}

func (s *MetricsSuite) TestDeletedByReason() {
	s.metrics.IncDeleted(files.DeleteReasonBurn)
	s.metrics.IncDeleted(files.DeleteReasonExpired)
	s.metrics.IncDeleted(files.DeleteReasonExpired)

	s.InDelta(0, s.value(s.metrics.deleted.WithLabelValues("user")), 0)
	s.InDelta(1, s.value(s.metrics.deleted.WithLabelValues("burn")), 0)
	s.InDelta(2, s.value(s.metrics.deleted.WithLabelValues("expired")), 0)
}

func (s *MetricsSuite) TestStorageErrorsByOp() {
	s.metrics.IncStorageErrors(files.StorageOpDelete)

	s.InDelta(1, s.value(s.metrics.storageErrors.WithLabelValues("delete")), 0)
	s.InDelta(0, s.value(s.metrics.storageErrors.WithLabelValues("put")), 0)
}

func (s *MetricsSuite) TestUploadBytesBuckets() {
	s.Equal([]float64{
		16 << 10, 64 << 10, 256 << 10, 1 << 20, 4 << 20, 16 << 20, 64 << 20, 256 << 20, 1 << 30,
	}, uploadBuckets())

	s.metrics.ObserveUploadBytes(3 << 20)
	s.metrics.ObserveUploadBytes(10 << 10)

	reg := prometheus.NewRegistry()
	s.Require().NoError(reg.Register(s.metrics.uploadBytes))

	families, err := reg.Gather()
	s.Require().NoError(err)
	s.Require().Len(families, 1)

	histogram := families[0].GetMetric()[0].GetHistogram()
	s.Equal(uint64(2), histogram.GetSampleCount())

	buckets := histogram.GetBucket()
	s.Require().Len(buckets, len(uploadBuckets()))
	s.Equal(uint64(1), buckets[0].GetCumulativeCount(), "10 KiB is in the 16 KiB bucket")
	s.Equal(uint64(1), buckets[3].GetCumulativeCount(), "3 MiB is above the 1 MiB bucket")
	s.Equal(uint64(2), buckets[4].GetCumulativeCount(), "3 MiB is in the 4 MiB bucket")
}

func (s *MetricsSuite) TestNewRegisteredShowsAllNames() {
	reg := prometheus.NewRegistry()

	_, err := NewRegistered(reg)
	s.Require().NoError(err)

	// The label series exist at zero, so the counter vectors are in the output before any event.
	families, err := reg.Gather()
	s.Require().NoError(err)

	series := make(map[string]int, len(families))
	for _, family := range families {
		series[family.GetName()] = len(family.GetMetric())
	}

	s.Equal(map[string]int{
		"padmark_files_uploads_total":        1,
		"padmark_files_downloads_total":      1,
		"padmark_files_deleted_total":        len(deleteReasons()),
		"padmark_files_upload_bytes":         1,
		"padmark_files_storage_errors_total": len(storageOps()),
	}, series)
}

func (s *MetricsSuite) TestNewRegisteredWithLinkMetricsHasNoConflict() {
	reg := prometheus.NewRegistry()

	_, err := linkmetrics.NewRegistered(reg)
	s.Require().NoError(err)

	_, err = NewRegistered(reg)
	s.Require().NoError(err)
}

func (s *MetricsSuite) TestNewRegisteredTwiceFails() {
	reg := prometheus.NewRegistry()

	_, err := NewRegistered(reg)
	s.Require().NoError(err)

	_, err = NewRegistered(reg)
	s.Require().Error(err, "a second registration must fail, not make duplicates")
}

// --- helpers ---

// value returns the current value of a counter.
func (s *MetricsSuite) value(counter prometheus.Counter) float64 {
	var metric dto.Metric

	s.Require().NoError(counter.Write(&metric))

	return metric.GetCounter().GetValue()
}
