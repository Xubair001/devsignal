package telemetry

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// Metrics are the instruments the SLOs are computed from.
//
// Hard rule 12 is the whole shape of this file: metrics carry BOUNDED labels
// only. Every label below has a small, fixed set of possible values — a route
// template, a status class, a pipeline stage, a source id. user_id and
// opportunity_id are deliberately absent: one time series per opportunity is
// unbounded cardinality, and it takes down the metrics backend long before
// anyone reads the dashboard. Those belong in trace attributes and log fields,
// which are sampled and indexed differently for exactly this reason.
//
// The route TEMPLATE, not the path. /api/v1/feed/{id}/explanation is one series;
// the raw path would be one series per opportunity, which is the same mistake by
// a different route.
type Metrics struct {
	// requestDuration answers the latency and availability objectives.
	requestDuration metric.Float64Histogram
	// pipelineStageDuration is per-stage work time, for finding which stage is
	// responsible for a freshness miss.
	pipelineStageDuration metric.Float64Histogram
	// pipelineOutcome counts stage results by status, for parse yield and
	// extraction validity.
	pipelineOutcome metric.Int64Counter
	// sourceFetch counts polls by outcome class.
	sourceFetch metric.Int64Counter
}

// Instruments returns the metric set, or a no-op set when metrics are disabled.
//
// Never nil: a caller should not have to check before recording. A nil-check at
// every call site is a nil-check someone forgets, and the panic lands in the
// request path rather than in a test.
func Instruments() *Metrics { return instruments.Metrics }

var instruments = newMetrics(otel.GetMeterProvider().Meter("devsignal.noop"))

// InitMetrics wires the real meter. Called from Init once the provider exists.
func InitMetrics(m metric.Meter) error {
	set := newMetrics(m)
	if set.err != nil {
		return set.err
	}
	instruments = set
	return nil
}

func newMetrics(m metric.Meter) *metricsSet {
	s := &metricsSet{Metrics: &Metrics{}}

	var err error
	// Explicit bucket boundaries in milliseconds, chosen around the objectives
	// rather than left to the default exponential buckets. The targets are 300ms,
	// 500ms and 800ms, so there are boundaries at and just below each: a histogram
	// whose nearest bucket edge is 250 and 1000 cannot tell you whether a 300ms
	// p95 was met.
	s.requestDuration, err = m.Float64Histogram(
		"http.server.request.duration",
		metric.WithUnit("ms"),
		metric.WithDescription("HTTP request duration"),
		metric.WithExplicitBucketBoundaries(
			5, 10, 25, 50, 100, 200, 300, 400, 500, 650, 800, 1000, 2000, 5000, 10000),
	)
	s.record(err)

	// Pipeline stages run in seconds to minutes, not milliseconds, so the buckets
	// are a different shape. Reusing the HTTP boundaries would put every stage in
	// the overflow bucket.
	s.pipelineStageDuration, err = m.Float64Histogram(
		"pipeline.stage.duration",
		metric.WithUnit("s"),
		metric.WithDescription("Time to process one record in a pipeline stage"),
		metric.WithExplicitBucketBoundaries(0.01, 0.05, 0.1, 0.5, 1, 5, 15, 60, 300),
	)
	s.record(err)

	s.pipelineOutcome, err = m.Int64Counter(
		"pipeline.stage.outcome",
		metric.WithDescription("Pipeline stage results by status"),
	)
	s.record(err)

	s.sourceFetch, err = m.Int64Counter(
		"source.fetch",
		metric.WithDescription("Source polls by outcome"),
	)
	s.record(err)

	return s
}

type metricsSet struct {
	*Metrics
	err error
}

func (s *metricsSet) record(err error) {
	if err != nil && s.err == nil {
		s.err = fmt.Errorf("telemetry: creating instrument: %w", err)
	}
}

// statusClass buckets a status code. The class, not the code: 2xx/4xx/5xx is four
// series, while every distinct code is dozens and answers no question the class
// does not.
func statusClass(code int) string {
	switch {
	case code >= 500:
		return "5xx"
	case code >= 400:
		return "4xx"
	case code >= 300:
		return "3xx"
	default:
		return "2xx"
	}
}

// RecordRequest records one HTTP request.
func (m *Metrics) RecordRequest(ctx context.Context, route, method string, code int, d time.Duration) {
	if m == nil || m.requestDuration == nil {
		return
	}
	m.requestDuration.Record(ctx, float64(d.Microseconds())/1000.0,
		metric.WithAttributes(
			attribute.String("http.route", route),
			attribute.String("http.request.method", method),
			attribute.String("http.response.status_class", statusClass(code)),
		))
}

// RecordStage records one pipeline stage outcome.
func (m *Metrics) RecordStage(ctx context.Context, stage, status string, d time.Duration) {
	if m == nil || m.pipelineOutcome == nil {
		return
	}
	attrs := metric.WithAttributes(
		attribute.String("pipeline.stage", stage),
		attribute.String("status", status),
	)
	m.pipelineOutcome.Add(ctx, 1, attrs)
	if m.pipelineStageDuration != nil {
		m.pipelineStageDuration.Record(ctx, d.Seconds(), attrs)
	}
}

// RecordSourceFetch records one poll.
//
// source_id is a bounded label: there are hundreds of sources, not millions, and
// per-source health is the largest ongoing operational cost in the product
// (blueprint §29). It is the one high-ish cardinality label that earns its place.
func (m *Metrics) RecordSourceFetch(ctx context.Context, sourceID, outcome string) {
	if m == nil || m.sourceFetch == nil {
		return
	}
	m.sourceFetch.Add(ctx, 1, metric.WithAttributes(
		attribute.String("source_id", sourceID),
		attribute.String("status", outcome),
	))
}

// MetricsMiddleware records duration and status for every request.
//
// Placed so it sees the chi route pattern, which is only populated after routing.
// Reading it before then yields an empty string and collapses every endpoint into
// one series.
func MetricsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}

		next.ServeHTTP(rec, r)

		// The template, resolved after the handler ran.
		route := "unmatched"
		if rc := chi.RouteContext(r.Context()); rc != nil && rc.RoutePattern() != "" {
			route = rc.RoutePattern()
		}
		Instruments().RecordRequest(r.Context(), route, r.Method, rec.status, time.Since(start))
	})
}

// statusRecorder captures the status code, which net/http otherwise does not
// expose after the fact.
type statusRecorder struct {
	http.ResponseWriter
	status  int
	written bool
}

func (s *statusRecorder) WriteHeader(code int) {
	if !s.written {
		s.status = code
		s.written = true
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	// An implicit 200 from a handler that never called WriteHeader.
	s.written = true
	return s.ResponseWriter.Write(b)
}

// Flush and Unwrap keep the wrapper transparent to streaming handlers and to
// anything that needs the underlying writer.
func (s *statusRecorder) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }

// StatusClassOf is exported for tests and for handlers that classify a code
// themselves.
func StatusClassOf(code int) string { return statusClass(code) }

// ParseStatus is a small helper for reading a status out of a string label.
func ParseStatus(s string) (int, error) { return strconv.Atoi(s) }
