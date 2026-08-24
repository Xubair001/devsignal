package matching

import (
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Xubair001/devsignal/internal/store"
)

func failedOn(e Eligibility, check string) bool {
	return slices.ContainsFunc(e.Failed, func(f Failure) bool { return f.Check == check })
}

// The single most consequential decision in the gate: silence passes.
//
// Most postings state no employment type, no language, no salary and no timezone
// band. If unstated data were treated as a mismatch, the gate would exclude most
// of a real corpus and present that to the user as "nothing matches you".
func TestUnstatedPostingDataPasses(t *testing.T) {
	p := prof(func(p *Profile) {
		p.Profile.TargetCountries = []string{"DE"}
		p.Profile.TargetEmploymentTypes = []string{"full_time"}
		p.Profile.Languages = []string{"en"}
		p.Profile.MinSalaryMinor = i64(80000_00)
	})
	// A posting that states nothing at all beyond being open.
	c := Candidate{Opportunity: store.Opportunity{}}

	e := CheckEligibility(p, c)
	if !e.Eligible {
		t.Errorf("a posting stating nothing was excluded on %v", e.FailedChecks())
	}
}

// A profile that constrains nothing must exclude nothing.
func TestEmptyProfileExcludesNothing(t *testing.T) {
	c := cand()
	if e := CheckEligibility(Profile{}, c); !e.Eligible {
		t.Errorf("an unconstrained profile excluded a posting on %v", e.FailedChecks())
	}
}

// Liveness is the product's central claim: a closed posting is not a weak match,
// it is not a posting.
func TestClosedAndMergedPostingsAreIneligible(t *testing.T) {
	closed := cand(func(c *Candidate) {
		c.Opportunity.ClosedAt = pgtype.Timestamptz{Time: time.Now(), Valid: true}
	})
	if e := CheckEligibility(prof(), closed); e.Eligible {
		t.Error("a closed posting passed the gate")
	} else if !failedOn(e, CheckLiveness) {
		t.Errorf("closed posting failed on %v, want liveness", e.FailedChecks())
	}

	merged := cand(func(c *Candidate) {
		c.Opportunity.MergedInto = pgtype.UUID{Bytes: [16]byte{1}, Valid: true}
	})
	if e := CheckEligibility(prof(), merged); e.Eligible {
		t.Error("a merged posting passed the gate")
	}
}

// Every failing check must be reported, not just the first. Telling a user half
// the reason invites them to fix the wrong thing.
func TestAllFailuresAreReportedNotJustTheFirst(t *testing.T) {
	p := prof(func(p *Profile) {
		p.Profile.TargetCountries = []string{"DE"}
		p.Profile.TargetEmploymentTypes = []string{"full_time"}
		p.Profile.Languages = []string{"en"}
	})
	c := cand(func(c *Candidate) {
		c.Opportunity.WorkMode = str("onsite")
		c.Opportunity.LocationCountry = str("JP")
		c.Opportunity.EmploymentType = str("contract")
		c.Opportunity.Language = str("ja")
	})

	e := CheckEligibility(p, c)
	if e.Eligible {
		t.Fatal("expected exclusion")
	}
	for _, want := range []string{CheckGeography, CheckEmploymentType, CheckLanguage} {
		if !failedOn(e, want) {
			t.Errorf("missing failure for %s; got %v", want, e.FailedChecks())
		}
	}
	// Reasons are written for the person excluded, so each must be non-empty.
	for _, r := range e.Reasons() {
		if r == "" {
			t.Error("an empty reason was returned")
		}
	}
}

// A remote posting's stated country is a formality. Excluding it would hide
// exactly the roles a location-constrained user most wants.
func TestRemotePostingSurvivesACountryConstraint(t *testing.T) {
	p := prof(func(p *Profile) { p.Profile.TargetCountries = []string{"DE"} })

	remote := cand(func(c *Candidate) {
		c.Opportunity.WorkMode = str("remote")
		c.Opportunity.LocationCountry = str("US")
	})
	if e := CheckEligibility(p, remote); !e.Eligible {
		t.Errorf("a remote posting was excluded on geography: %v", e.FailedChecks())
	}

	onsite := cand(func(c *Candidate) {
		c.Opportunity.WorkMode = str("onsite")
		c.Opportunity.LocationCountry = str("US")
	})
	if e := CheckEligibility(p, onsite); e.Eligible {
		t.Error("an onsite posting in the wrong country passed")
	}
}

// visa_sponsorship is 'unknown' on nearly every posting, and unknown must pass.
// Only an explicit "no" plus a user who explicitly needs sponsorship excludes.
func TestWorkAuthorizationOnlyExcludesOnAnExplicitNo(t *testing.T) {
	needs := prof(func(p *Profile) { p.NeedsSponsorship = true })

	unknown := cand(func(c *Candidate) {
		c.Opportunity.VisaSponsorship = "unknown"
		c.Opportunity.LocationCountry = str("US")
	})
	if e := CheckEligibility(needs, unknown); !e.Eligible {
		t.Errorf("unknown sponsorship excluded a user who needs it: %v", e.FailedChecks())
	}

	no := cand(func(c *Candidate) {
		c.Opportunity.VisaSponsorship = "no"
		c.Opportunity.LocationCountry = str("US")
	})
	if e := CheckEligibility(needs, no); e.Eligible {
		t.Error("a no-sponsorship posting passed for a user who needs sponsorship")
	} else if !failedOn(e, CheckWorkAuth) {
		t.Errorf("failed on %v, want work authorization", e.FailedChecks())
	}

	// Already authorized there: no sponsorship needed, so it passes.
	authorized := prof(func(p *Profile) {
		p.NeedsSponsorship = true
		p.WorkAuthCountries = []string{"US"}
	})
	if e := CheckEligibility(authorized, no); !e.Eligible {
		t.Errorf("a user already authorized in US was excluded: %v", e.FailedChecks())
	}
}

// A user who never said they need sponsorship must not be assumed to. Assuming it
// would silently hide most of the corpus from them.
func TestUnknownSponsorshipNeedIsNotTreatedAsNeeding(t *testing.T) {
	c := cand(func(c *Candidate) {
		c.Opportunity.VisaSponsorship = "no"
		c.Opportunity.LocationCountry = str("US")
	})
	if e := CheckEligibility(prof(), c); !e.Eligible {
		t.Errorf("a user who said nothing about sponsorship was excluded: %v", e.FailedChecks())
	}
}

// Excluding a real job because of a number WE guessed is precisely what hard
// rule 3 forbids.
func TestEstimatedSalaryNeverGates(t *testing.T) {
	p := prof(func(p *Profile) { p.Profile.MinSalaryMinor = i64(100000_00) })
	c := cand(func(c *Candidate) {
		c.Opportunity.SalaryMaxMinor = i64(50000_00)
		c.Opportunity.SalaryIsEstimated = true
	})
	if e := CheckEligibility(p, c); !e.Eligible {
		t.Errorf("an estimated salary gated a posting: %v", e.FailedChecks())
	}
}

func TestDisclosedSalaryBelowTheFloorIsExcluded(t *testing.T) {
	p := prof(func(p *Profile) { p.Profile.MinSalaryMinor = i64(100000_00) })
	c := cand(func(c *Candidate) { c.Opportunity.SalaryMaxMinor = i64(50000_00) })
	e := CheckEligibility(p, c)
	if e.Eligible {
		t.Error("a posting paying below the user's hard floor passed")
	} else if !failedOn(e, CheckSalaryFloor) {
		t.Errorf("failed on %v, want salary floor", e.FailedChecks())
	}
}

// Comparing 60000 EUR/year against a 5000 USD/month floor is arithmetic on unlike
// things. Without a recorded fx rate the honest move is not to gate.
func TestSalaryFloorDoesNotGateAcrossCurrenciesOrPeriods(t *testing.T) {
	p := prof(func(p *Profile) {
		p.Profile.MinSalaryMinor = i64(100000_00)
		p.Profile.SalaryCurrency = str("USD")
	})
	diffCurrency := cand(func(c *Candidate) {
		c.Opportunity.SalaryMaxMinor = i64(50000_00)
		c.Opportunity.SalaryCurrency = str("EUR")
	})
	if e := CheckEligibility(p, diffCurrency); !e.Eligible {
		t.Errorf("gated across currencies without an fx rate: %v", e.FailedChecks())
	}

	p2 := prof(func(p *Profile) {
		p.Profile.MinSalaryMinor = i64(5000_00)
		p.Profile.SalaryPeriod = str("month")
	})
	diffPeriod := cand(func(c *Candidate) {
		c.Opportunity.SalaryMaxMinor = i64(50000_00)
		c.Opportunity.SalaryPeriod = str("year")
	})
	if e := CheckEligibility(p2, diffPeriod); !e.Eligible {
		t.Errorf("gated across periods: %v", e.FailedChecks())
	}
}

func TestTimezoneGateOnlyFiresOnAStatedBand(t *testing.T) {
	// User targets Germany (UTC+1).
	p := prof(func(p *Profile) { p.Profile.TargetCountries = []string{"DE"} })

	noBand := cand(func(c *Candidate) { c.Opportunity.WorkMode = str("remote") })
	if e := CheckEligibility(p, noBand); !e.Eligible {
		t.Errorf("a remote posting with no stated band was excluded: %v", e.FailedChecks())
	}

	overlapping := cand(func(c *Candidate) {
		c.Opportunity.WorkMode = str("remote")
		c.Opportunity.RemoteTimezoneMin = i16(0)
		c.Opportunity.RemoteTimezoneMax = i16(3)
	})
	if e := CheckEligibility(p, overlapping); !e.Eligible {
		t.Errorf("an overlapping band excluded the user: %v", e.FailedChecks())
	}

	disjoint := cand(func(c *Candidate) {
		c.Opportunity.WorkMode = str("remote")
		c.Opportunity.RemoteTimezoneMin = i16(-8)
		c.Opportunity.RemoteTimezoneMax = i16(-5)
	})
	e := CheckEligibility(p, disjoint)
	if e.Eligible {
		t.Error("a band with no overlap passed")
	} else if !failedOn(e, CheckTimezone) {
		t.Errorf("failed on %v, want timezone", e.FailedChecks())
	}
}

// An unknown country cannot be excluded on a guess. The offset table is coarse
// and deliberately errs toward passing.
func TestUnknownCountryNeverGatesOnTimezone(t *testing.T) {
	p := prof(func(p *Profile) { p.Profile.TargetCountries = []string{"ZZ"} })
	c := cand(func(c *Candidate) {
		c.Opportunity.WorkMode = str("remote")
		c.Opportunity.RemoteTimezoneMin = i16(-8)
		c.Opportunity.RemoteTimezoneMax = i16(-5)
	})
	if e := CheckEligibility(p, c); !e.Eligible {
		t.Errorf("an unrecognised country was gated on a guessed offset: %v", e.FailedChecks())
	}
}

// The gate is boolean. An ineligible posting must never arrive as a low score,
// because a scored-down ineligible role stays in the feed and wastes attention.
func TestIneligibleIsNeverScoredDownInstead(t *testing.T) {
	p := prof(func(p *Profile) { p.Profile.TargetCountries = []string{"DE"} })
	c := cand(func(c *Candidate) {
		c.Opportunity.WorkMode = str("onsite")
		c.Opportunity.LocationCountry = str("JP")
	})

	e := CheckEligibility(p, c)
	if e.Eligible {
		t.Fatal("expected exclusion")
	}
	// The fit score is still computable — it is simply never consulted for an
	// excluded posting. This asserts the two stages stay independent, so nobody
	// can later "just rank it lower" instead of excluding it.
	if f := ComputeFit(p, c); f.Score < 0 || f.Score > 100 {
		t.Errorf("fit on an ineligible posting was %d, outside 0-100", f.Score)
	}
	if len(e.FailedChecks()) == 0 {
		t.Error("an exclusion with no named check cannot be explained to a user")
	}
}

// Case should not decide eligibility: 'DE' and 'de' are the same country.
func TestComparisonsAreCaseInsensitive(t *testing.T) {
	p := prof(func(p *Profile) {
		p.Profile.TargetCountries = []string{"de"}
		p.Profile.Languages = []string{"EN"}
		p.Profile.TargetEmploymentTypes = []string{"Full_Time"}
	})
	c := cand(func(c *Candidate) {
		c.Opportunity.WorkMode = str("onsite")
		c.Opportunity.LocationCountry = str("DE")
		c.Opportunity.Language = str("en")
		c.Opportunity.EmploymentType = str("full_time")
	})
	if e := CheckEligibility(p, c); !e.Eligible {
		t.Errorf("case differences excluded a matching posting: %v", e.FailedChecks())
	}
}
