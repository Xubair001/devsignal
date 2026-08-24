# Open decisions

The blueprint §33.3 items that were still open, with a recommendation for each. Two are settled
here because they are engineering choices with a defensible default. Two are not mine to settle and
say so.

Criteria applied throughout: prefer the standard, boring option; prefer free or near-free; prefer
fewer moving parts over more capable ones. Every dependency added is a thing that can break at 3am.

---

## 1. Tier-A source list — SETTLED

See [TIER-A-SOURCES.md](TIER-A-SOURCES.md). Three platforms built and verified against live
endpoints (Greenhouse, Lever, Ashby), four more reviewed and reachable but needing a different
fetch strategy, and the Tier-C exclusions restated with the reason they are permanent rather than
deferred.

No paid data source. Not only on cost: a bought corpus cannot be verified live, and verified
liveness is the product — buying the corpus means buying the ghost listings too.

---

## 2. Backup erasure: crypto-shredding vs a stated retention window — RECOMMEND STATED WINDOW

The live-store guarantee does not depend on this choice; `make check-erasure` already proves a
deleted identifier appears nowhere in Postgres or object storage. What is open is backups, and only
because a backup taken before an erasure request still contains the data.

**Recommendation: a documented maximum backup retention window of 35 days**, stated plainly in the
privacy notice, with backups encrypted at rest and no restore path that reintroduces an erased user
without replaying the erasure log.

Why this over crypto-shredding with per-user keys:

- **Cost of being wrong is asymmetric.** Per-user keys mean a key-management system in the hot path
  of every read of user data. Lose or corrupt a key and you have destroyed a live user's account,
  not just their backup. The failure mode of a retention window is "data persists 35 days longer
  than ideal"; the failure mode of key mismanagement is silent, permanent, live data loss.
- **It is the standard answer.** A stated retention window is what the overwhelming majority of
  GDPR-compliant services do, and supervisory authorities have accepted it where the window is
  short, documented, and actually enforced. Crypto-shredding is the better answer at a scale where
  restoring a whole backup is routine — which is not v1.
- **It costs nothing to implement.** It is a retention policy plus a lifecycle rule, versus a key
  service, key rotation, and a per-user key lifecycle that itself has to survive erasure.

What this obliges us to do, and these are not optional:

1. Say the window in the privacy notice, in the same paragraph as the erasure promise. Do not
   promise "deleted immediately" and mean "deleted from live storage immediately".
2. Enforce it mechanically — a lifecycle rule that expires backups, not a calendar reminder.
3. Replay the erasure log after any restore. A restore that silently resurrects erased users turns
   this from a stated limitation into a broken promise.

Revisit if we ever take on a customer whose own compliance regime forbids it, or if backup restores
stop being rare.

---

## 3. Email consent basis and sending domain — RECOMMEND SES + DOUBLE OPT-IN

**Consent basis: explicit opt-in, double-confirmed, per purpose.** The digest is marketing-adjacent
enough that legitimate interest is an argument we would rather not have to make. Separate consent
for the daily digest and for transactional mail (password reset, erasure confirmation); the latter
needs no consent and must never be bundled with the former, because a user who withdraws digest
consent still needs their password-reset mail.

Recorded per user: timestamp, IP, the exact wording consented to, and the version of that wording.
Consent you cannot evidence is consent you do not have.

**Sending: Amazon SES.** $0.10 per 1,000 emails, and 62,000/month free when sending from EC2.

Why SES over the friendlier options:

- The digest is the retention loop, so volume scales with users, and SES is roughly an order of
  magnitude cheaper per 1,000 than plan-based providers (Postmark's overage is $1.20–$1.80 per
  1,000 against SES's $0.10). At 100K users on a daily digest the difference is the largest line
  item in the product.
- We are already on AWS per blueprint §36's migration path, so it adds no new vendor.
- The cost of SES is setup effort, which is a one-time cost paid by an engineer, not a recurring
  cost paid forever.

Use **Resend for development** (3,000/month free, no domain warm-up) so nobody needs production
credentials to work on templates. One interface, two implementations — the same shape as the
extraction `Provider` interface.

**Domain: a subdomain dedicated to sending**, e.g. `mail.devsignal.example`, with SPF, DKIM and
DMARC on it, kept separate from the apex domain's reputation. Warm it before the first bulk send.
A digest that lands in spam is not a retention loop.

Blocked on nothing except owning the domain. This does not need to be settled until step 18.

---

## 4. EU AI Act classification for the recommender — NOT AN ENGINEERING DECISION

This one needs a lawyer, and saying otherwise would be pretending.

What is not in doubt: Annex III 4(a) covers AI systems intended to be used for recruitment or
selection, and its wording explicitly includes **targeted job advertisements**. A system that
ranks job postings for a candidate is close enough to that language that "obviously out of scope"
is not a position anyone should take without an opinion in writing.

What an opinion needs to address, and what is worth preparing before paying for one:

| Question | Our position, for counsel to test |
|---|---|
| Do we place targeted job advertisements? | We rank publicly posted openings for a candidate who asked to see them. No employer pays us for placement and no employer chooses who sees a posting. |
| Do we filter or score candidates on an employer's behalf? | No. The employer is not our user, receives nothing from us, and cannot see our users. |
| Is the output a decision or information? | The user sees ranked public postings with per-factor explanations and applies themselves. Nothing is decided about them. |
| Who is provider and who is deployer? | We build and operate it, so we would be the provider if it is in scope. |

Preparation that is useful regardless of the answer, and is already true of the design:

- Explanations are faithful rather than post-hoc: fit is a weighted sum of bounded factors whose
  weights total 1, so each term contributes exactly `w_i * f_i` (see CLAUDE.md, "The two numbers").
- Everything a score depends on carries its version, so any decision is reproducible after the fact.
- Nothing renders that cannot be derived from observed data — no competitiveness estimates, no bare
  match percentages implying a probability.
- The evaluation harness (step 16) produces exactly the measurements a conformity assessment would
  ask for.

None of this blocks development. It blocks an **EU launch**, and the honest way to hold it is as a
launch gate with a named owner, not as a technical unknown. The Digital Omnibus timeline moved the
relevant deadline to **2 December 2027**, so there is room to get a real opinion rather than a rushed
one.
