---
name: scoring-change
description: Change anything that affects what a user sees ranked or scored in DevSignal — fit factors, weights, the eligibility gate, retrieval, candidate generation, embeddings, the explanation breakdown, or how a match is displayed. Use when the task mentions match score, fit, ranking, relevance, weights, tuning, recommendations, the feed order, calibration, NDCG, or "why did this rank here". Enforces the fit/priority split, the no-time-dependence rule, the display rules, and the eval gate.
---

# Changing scoring or ranking

Ranking changes are the easiest place in this system to do invisible damage: nothing errors, tests
pass, and the product quietly gets worse. The eval gate exists so that cannot happen silently.

## The two numbers — keep them separate in code

```
fit_score        stable    f(profile_version, opportunity_version, model_version)
                           cached; invalidated only by a version change
                           displayed, with per-factor contributions

priority_score   volatile  g(fit, age, closing_soon, saturation)
                           computed at read time; orders today's feed
                           NEVER displayed, NEVER persisted as a match
```

**If your change makes `fit_score` depend on the current time, it is wrong.** That single mistake
reintroduces four bugs at once: the score becomes uncacheable, irreproducible, unexplainable when it
moves overnight, and forces recomputation across users × opportunities. Recency belongs in
`priority_score`. There is no exception to this.

## The model shape

```
STAGE 0 — ELIGIBILITY   boolean gate; a failure is explained, never scored down
  work authorization | geography/timezone | employment type
  language | hard salary floor | liveness verified and not closed

STAGE 1 — FIT           fit = 100 * SUM(w_i * f_i),  SUM(w_i) = 1,  f_i in [0,1]
  f_required_skills   0.35
  f_semantic          0.20
  f_seniority         0.15
  f_preferred_skills  0.10
  f_domain            0.10
  f_compensation      0.10   renormalize the others when salary is undisclosed;
                             never impute a number
```

Two invariants the code must enforce, not merely document:

1. **Weights sum to 1.** Assert it at construction. When a factor is unavailable (undisclosed
   salary), renormalize the remainder — do not substitute a neutral 0.5, which silently rewards
   missing data.
2. **Keep it linear and monotone.** Each term contributes exactly `w_i * f_i`, which is why the
   displayed breakdown is faithful arithmetic rather than a post-hoc story. Do not switch to a
   multiplied form: seven bounded factors at 0.9 give 0.478, so an excellent match reports as 48/100,
   and no factor in a product has a fixed contribution — the breakdown could not be derived.

## Retrieval is part of scoring

A perfect scorer cannot rank what retrieval never returned. If you change factors, check whether
retrieval still surfaces the candidates those factors reward.

```
retrieve <= 500 candidates    hard predicates + pgvector kNN + keyword recall
then score only those
```

Never add a full-corpus pass. Re-score a user when their `profile_version` changes; match a new
posting only against saved criteria.

## Display rules (binding — blueprint §3)

| Allowed | Never |
|---------|-------|
| Bands: Strong fit / Worth a look / Stretch | A bare percentage implying a probability |
| Per-factor contributions: "+29 of 35" | A competitiveness estimate — we have no applicant counts |
| Percentile, *after* calibration is measured | An imputed salary shown as the employer's |
| "Not disclosed", plus a labelled market range | Any value not derived from observed data |

A percentage is only permitted once expected calibration error has been measured on a held-out set,
and even then prefer the percentile framing — it is honest and more decision-useful.

## Versioning

Anything a score depends on carries its version, or the cache silently serves wrong numbers:

```
weights_version, model_id, prompt_version, schema_version,
ontology_version, embedding_model, embedding_version
```

Bumping `weights_version` is what invalidates cached `fit_score` rows. **If you change a weight and
do not bump the version, users keep seeing the old score.** Changing an embedding model additionally
requires the dual-write migration: write both versions, backfill, verify recall against the eval set,
switch reads by version filter, then re-tune thresholds — they are model-specific constants.

## The gate

```bash
make eval
```

```
NDCG@10        0.71   (baseline 0.68)   +0.03
Precision@7    0.57   (baseline 0.54)   +0.03
Eligibility FP 0.00   <- a gate that admits an ineligible role is a bug, not a metric
Coverage       94%    of judged pairs returned by retrieval
```

- CI fails on NDCG@10 regression beyond noise. Do not tune to the metric alone — inspect the pairs
  that moved.
- `Eligibility FP` must be exactly 0. A hard gate that lets through an ineligible role is a
  correctness bug; it is not a tradeoff.
- `Precision@7` is measured at 7 because that is what the product promises daily.
- If `Coverage` drops, your factor change is being starved by retrieval — fix retrieval first.

If the labelled set does not cover your change, **extend the set before landing the change.** Growing
`eval_judgement` from the engagement log is normal and expected; shipping a factor with no judged
pairs is not.

## Verify

```bash
make eval                                  # the gate
go test ./internal/matching/...
make test -run TestFitReproducible         # same inputs + versions -> same number
make test -run TestWeightsSumToOne
```

Manual check that catches the classic bug: compute a fit score, wait, compute it again with no
version change, and assert the number is byte-identical.

## Done means

- [ ] `fit_score` has no time dependence; recency lives only in `priority_score`
- [ ] Weights sum to 1, asserted in code; undisclosed factors renormalize
- [ ] Model stays linear and monotone; breakdown is real arithmetic
- [ ] `weights_version` (or the relevant version) bumped, so caches invalidate
- [ ] Retrieval still returns what the new factors reward; `Coverage` did not drop
- [ ] `make eval` passes with no NDCG@10 regression; `Eligibility FP` is 0
- [ ] Labelled set extended if the change was not covered
- [ ] Nothing new rendered that isn't derived from observed data
- [ ] Decision log still records inputs, weights and every version
