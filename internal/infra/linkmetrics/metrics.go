// Package linkmetrics provides a Prometheus-backed implementation of links.Metrics — the
// counters documented in TODO.md's Phase 3 section: padmark_links_created_total,
// padmark_links_redirects_total, padmark_links_collisions_total. Naming follows this project's
// {namespace}_{subsystem}_{metric_name}_{unit} convention (see ~/.claude/docs/observability.md):
// namespace "padmark", subsystem "links".
package linkmetrics

import (
	"fmt"

	"github.com/prometheus/client_golang/prometheus"
)

const (
	namespace = "padmark"
	subsystem = "links"
)

// Metrics is a Prometheus-backed implementation of links.Metrics.
type Metrics struct {
	linksCreated prometheus.Counter
	redirects    prometheus.Counter
	collisions   prometheus.Counter
}

// New creates a Metrics with its three counters. The counters are not yet visible on any
// registry — use NewRegistered to create and register in one step, the usual case; New alone is
// for tests that want a Metrics without a live registry.
func New() *Metrics {
	return &Metrics{
		linksCreated: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: namespace,
			Subsystem: subsystem,
			Name:      "created_total",
			Help:      "Total number of short links created.",
			// Unit / ConstLabels zero = no custom unit annotation, no static labels.
			Unit:        "",
			ConstLabels: nil,
		}),
		redirects: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace:   namespace,
			Subsystem:   subsystem,
			Name:        "redirects_total",
			Help:        "Total number of successful short-link redirects served via GET /r/{id}.",
			Unit:        "",
			ConstLabels: nil,
		}),
		collisions: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: namespace,
			Subsystem: subsystem,
			Name:      "collisions_total",
			Help: "Total number of random-slug generation collisions during link creation " +
				"(a rejected custom alias does not count — see links.Metrics' doc comment).",
			Unit:        "",
			ConstLabels: nil,
		}),
	}
}

// NewRegistered creates a Metrics and registers its counters with reg, so they appear on
// whatever /metrics endpoint reg is exposed through.
func NewRegistered(reg prometheus.Registerer) (*Metrics, error) {
	m := New()

	for _, c := range []prometheus.Collector{m.linksCreated, m.redirects, m.collisions} {
		err := reg.Register(c)
		if err != nil {
			return nil, fmt.Errorf("register metric: %w", err)
		}
	}

	return m, nil
}

// IncLinksCreated implements links.Metrics.
func (m *Metrics) IncLinksCreated() { m.linksCreated.Inc() }

// IncRedirects implements links.Metrics.
func (m *Metrics) IncRedirects() { m.redirects.Inc() }

// IncCollisions implements links.Metrics.
func (m *Metrics) IncCollisions() { m.collisions.Inc() }
