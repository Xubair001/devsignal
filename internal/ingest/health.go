package ingest

import (
	"context"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Xubair001/devsignal/internal/sourcehealth"
	"github.com/Xubair001/devsignal/internal/store"
)

// BaselineWindowDays is how much of a source's own past forms the comparison
// baseline. A week smooths weekday/weekend shape without being so long that a
// slow decline becomes the baseline it is measured against.
const BaselineWindowDays = 7

// recordHealth accumulates today's counters and then judges today against the
// source's own recent past.
//
// Absolute thresholds are useless here: a source that fell from 98% to 71% field
// completeness is broken, and 71% passes any fixed floor. Only the relative
// comparison sees it (blueprint §29).
func (r *Runner) recordHealth(ctx context.Context, srcID pgtype.UUID, res Result, failed bool) {
	polls, failures, notModified := 1, 0, 0
	if failed {
		failures = 1
	}
	if res.NotModified {
		notModified = 1
	}

	if err := r.q.RecordSourceHealth(ctx, store.RecordSourceHealthParams{
		SourceID:       srcID,
		Polls:          int32(polls),
		PollFailures:   int32(failures),
		NotModified:    int32(notModified),
		PostingsSeen:   int32(res.Fetched),
		PostingsUsable: int32(res.Usable()),
		WithCompany:    int32(res.WithCompany),
		WithLocation:   int32(res.WithLocation),
		WithApplyUrl:   int32(res.WithApplyURL),
		WithLanguage:   int32(res.WithLanguage),
		// No adapter surfaces salary yet, so this stays 0. It is excluded from
		// alerting anyway: a field a source never provides cannot rot.
		WithSalary: 0,
	}); err != nil {
		r.log.Warn("recording source health", "err", err)
		return
	}

	// A failed or not-modified poll observed no postings, so there is nothing to
	// compare. Judging on it would let an outage look like parser rot.
	if failed || res.NotModified {
		return
	}
	r.evaluateHealth(ctx, srcID)
}

func (r *Runner) evaluateHealth(ctx context.Context, srcID pgtype.UUID) {
	today, err := r.q.TodaySourceHealth(ctx, srcID)
	if err != nil {
		return // no row yet; nothing to judge
	}
	base, err := r.q.BaselineSourceHealth(ctx, store.BaselineSourceHealthParams{
		SourceID: srcID, WindowDays: BaselineWindowDays,
	})
	if err != nil {
		return
	}

	verdict := sourcehealth.Compare(
		sourcehealth.Metrics{
			Postings: int(today.PostingsSeen), Usable: int(today.PostingsUsable),
			Fill: map[string]int{
				sourcehealth.FieldCompany:  int(today.WithCompany),
				sourcehealth.FieldLocation: int(today.WithLocation),
				sourcehealth.FieldApplyURL: int(today.WithApplyUrl),
				sourcehealth.FieldLanguage: int(today.WithLanguage),
			},
		},
		sourcehealth.Metrics{
			Postings: int(base.PostingsSeen), Usable: int(base.PostingsUsable),
			Fill: map[string]int{
				sourcehealth.FieldCompany:  int(base.WithCompany),
				sourcehealth.FieldLocation: int(base.WithLocation),
				sourcehealth.FieldApplyURL: int(base.WithApplyUrl),
				sourcehealth.FieldLanguage: int(base.WithLanguage),
			},
		},
	)

	switch verdict.Status {
	case sourcehealth.StatusHealthy:
		if err := r.q.ClearSourceDegraded(ctx, srcID); err != nil {
			r.log.Warn("clearing degraded state", "err", err)
		}
	case sourcehealth.StatusUnknown:
		// Not enough data to judge. Deliberately does nothing: "we cannot tell"
		// must neither clear a real degradation nor count as one.
	case sourcehealth.StatusDegraded:
		note := summarize(verdict.Reasons)
		if err := r.q.SetSourceDegraded(ctx, store.SetSourceDegradedParams{
			Note: &note, ID: srcID,
		}); err != nil {
			r.log.Warn("marking degraded", "err", err)
			return
		}
		src, err := r.q.GetSourceByID(ctx, srcID)
		if err != nil {
			return
		}
		r.log.Warn("source degraded", "source", src.Name,
			"consecutive", src.ConsecutiveDegraded, "detail", note)

		// Sustained, not transient. One bad evaluation must never take a source
		// offline — that is a silent loss of corpus coverage.
		if sourcehealth.ShouldQuarantine(int(src.ConsecutiveDegraded)) {
			r.log.Error("quarantining source after sustained degradation",
				"source", src.Name, "consecutive", src.ConsecutiveDegraded, "detail", note)
			if err := r.q.QuarantineDegradedSource(ctx, store.QuarantineDegradedSourceParams{
				Note: &note, ID: srcID,
			}); err != nil {
				r.log.Warn("quarantining", "err", err)
			}
		}
	}
}

func summarize(reasons []sourcehealth.Reason) string {
	parts := make([]string, 0, len(reasons))
	for _, r := range reasons {
		parts = append(parts, r.Detail)
	}
	out := strings.Join(parts, "; ")
	if len(out) > 500 {
		out = out[:500]
	}
	if out == "" {
		return "degraded"
	}
	return out
}
