package matching

import (
	"math"
	"time"
)

// Priority ordering lives here and nowhere else.
//
// This is the ONLY place in the system where the current time may touch a number
// that affects what a user sees, and even here the number is never shown as a
// match. Keeping the two apart is not stylistic: putting recency inside fit makes
// the score uncacheable, irreproducible, unexplainable when it moves overnight,
// and forces recomputation across every user times every opportunity.
//
// If you are tempted to add a term here that is really about how well the posting
// suits the user, it belongs in fit.go instead. The test for which side something
// belongs on: would the user be surprised to see this number change tomorrow
// without anything about them or the job changing? If yes, it goes here.

// Priority bonus and penalty coefficients, in fit points.
//
// Deliberately small relative to fit's 0-100 range. Their job is to break ties
// and surface fresh postings among comparable matches, not to promote a weak match
// over a strong one — a feed that leads with a poor but new posting teaches users
// the ranking is arbitrary.
const (
	// RecencyBonus is the most a brand-new posting can gain.
	RecencyBonus = 6.0
	// ClosingSoonBonus applies when the employer stated a closing date.
	ClosingSoonBonus = 4.0
	// SaturationPenalty is the most a repeatedly-ignored posting loses.
	SaturationPenalty = 12.0

	// recencyHalfLife is how long a posting keeps meaningful freshness credit.
	// One week: developer hiring cycles run in weeks, and a fortnight-old posting
	// is not news.
	recencyHalfLife = 7 * 24 * time.Hour

	// closingSoonWindow is when a stated deadline starts to matter.
	closingSoonWindow = 7 * 24 * time.Hour

	// saturationFull is the number of times a user has to be shown and ignore a
	// posting before it takes the full penalty. Three is enough to distinguish
	// "not seen yet" from "actively not interested" without punishing a user who
	// scrolled past once.
	saturationFull = 3
)

// PrioritySignals are the volatile inputs. Ours, not the source's: FirstSeenAt is
// when WE first saw the posting, not the date the board claims, because boards and
// employers refresh their own timestamps so listings look fresh.
type PrioritySignals struct {
	FirstSeenAt time.Time
	// ClosesAt is the employer's stated closing date, when they gave one.
	ClosesAt *time.Time
	// TimesShownAndIgnored counts feed impressions with no engagement.
	TimesShownAndIgnored int
}

// Priority orders today's feed. Never displayed, never persisted as a match.
//
// Takes `now` as an argument rather than calling time.Now: domain logic reads time
// from an injected clock (hard rule 14), and it is the only way this is testable.
func Priority(fit int, s PrioritySignals, now time.Time) float64 {
	p := float64(fit)
	p += recencyCredit(s.FirstSeenAt, now)
	p += closingSoonCredit(s.ClosesAt, now)
	p -= saturationDebit(s.TimesShownAndIgnored)
	return clamp(p, 0, 100)
}

// recencyCredit decays exponentially from RecencyBonus at zero age.
//
// Exponential rather than linear so the difference between today and yesterday is
// larger than between day twenty and twenty-one, which is how attention actually
// works. A future FirstSeenAt (clock skew) gets the full bonus rather than an
// extrapolated one.
func recencyCredit(firstSeen, now time.Time) float64 {
	if firstSeen.IsZero() {
		return 0
	}
	age := now.Sub(firstSeen)
	if age <= 0 {
		return RecencyBonus
	}
	return RecencyBonus * math.Exp2(-float64(age)/float64(recencyHalfLife))
}

// closingSoonCredit ramps up as a stated deadline approaches.
//
// Only when the employer stated one. An inferred deadline would be an invented
// signal, and "apply soon" pressure we made up is worse than none.
func closingSoonCredit(closesAt *time.Time, now time.Time) float64 {
	if closesAt == nil {
		return 0
	}
	left := closesAt.Sub(now)
	if left <= 0 {
		// Already closed. Liveness excludes it from the feed anyway; no credit.
		return 0
	}
	if left >= closingSoonWindow {
		return 0
	}
	return ClosingSoonBonus * (1 - float64(left)/float64(closingSoonWindow))
}

// saturationDebit grows with repeated ignored impressions and then stops.
//
// Capped so a posting sinks but never becomes unreachable: the user may have been
// busy, and permanently hiding something they never explicitly dismissed would
// make the feed lie about what is available. Explicit dismissal is a different
// mechanism and belongs in engagement, not here.
func saturationDebit(timesIgnored int) float64 {
	if timesIgnored <= 0 {
		return 0
	}
	if timesIgnored >= saturationFull {
		return SaturationPenalty
	}
	return SaturationPenalty * float64(timesIgnored) / float64(saturationFull)
}
