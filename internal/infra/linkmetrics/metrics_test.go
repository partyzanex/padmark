package linkmetrics

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/require"
)

func counterValue(t *testing.T, c prometheus.Counter) float64 {
	t.Helper()

	var metric dto.Metric

	require.NoError(t, c.Write(&metric))

	return metric.GetCounter().GetValue()
}

func TestMetrics_IncLinksCreated(t *testing.T) {
	t.Parallel()

	m := New()
	m.IncLinksCreated()
	m.IncLinksCreated()

	require.InDelta(t, 2, counterValue(t, m.linksCreated), 0)
	require.InDelta(t, 0, counterValue(t, m.redirects), 0, "unrelated counters must stay at zero")
}

func TestMetrics_IncRedirects(t *testing.T) {
	t.Parallel()

	m := New()
	m.IncRedirects()

	require.InDelta(t, 1, counterValue(t, m.redirects), 0)
}

func TestMetrics_IncCollisions(t *testing.T) {
	t.Parallel()

	m := New()
	m.IncCollisions()
	m.IncCollisions()
	m.IncCollisions()

	require.InDelta(t, 3, counterValue(t, m.collisions), 0)
}

func TestNewRegistered_RegistersAllThreeCounters(t *testing.T) {
	t.Parallel()

	reg := prometheus.NewRegistry()

	m, err := NewRegistered(reg)
	require.NoError(t, err)

	m.IncLinksCreated()
	m.IncRedirects()
	m.IncCollisions()

	families, err := reg.Gather()
	require.NoError(t, err)

	names := make(map[string]bool, len(families))
	for _, f := range families {
		names[f.GetName()] = true
	}

	require.True(t, names["padmark_links_created_total"])
	require.True(t, names["padmark_links_redirects_total"])
	require.True(t, names["padmark_links_collisions_total"])
}

func TestNewRegistered_DoubleRegistration_Errors(t *testing.T) {
	t.Parallel()

	reg := prometheus.NewRegistry()

	_, err := NewRegistered(reg)
	require.NoError(t, err)

	_, err = NewRegistered(reg)
	require.Error(t, err, "registering a second Metrics on the same registry must fail, not silently duplicate")
}
