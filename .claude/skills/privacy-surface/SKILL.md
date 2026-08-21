---
name: privacy-surface
description: Use when a change touches user-derived data in DevSignal — resumes, profiles, embeddings derived from a profile, engagement history, exports, caches, analytics copies — or when sending any user data to an external model. Also use when implementing or reviewing account deletion, data export, or the erasure completeness script. Enforces the erasure inventory, PII minimization before external calls, and the audit trail.
---

# Touching user-derived data

Two things go wrong here, both quietly. A new derived artifact gets created and never added to the
erasure path, so a deletion request leaves it behind. Or user data goes to an external model with
more of the document attached than the task needed.

## Rule 1 — new derived artifact, same-change inventory update

Deleting the Postgres rows is the easy 60%. The parts that survive a naive implementation:

| Location | Typically forgotten? |
|----------|---------------------|
| Postgres rows | No |
| S3 raw + parsed objects | Sometimes |
| **Embedding / vector rows** | **Almost always** |
| **Search index documents** | **Almost always** |
| Redis caches | Often |
| Cached LLM extractions keyed by content hash | Often |
| Analytics / warehouse copies | Often |
| Exports the user generated earlier | Often |
| Backups | Per the stated policy — see below |

If your change stores anything derived from a user, it goes in the inventory **in the same change**,
and the completeness script must still pass:

```bash
make check-erasure     # asserts a deleted identifier appears in no location
```

The erasure job is modelled as tracked work, not a `DELETE` statement:

```
erasure_request(user_id, requested_at, completed_at)
erasure_step(request_id, location, status, completed_at)
```

Each location reports its own completion, so a partial erasure is visible rather than assumed. A
deletion promise that leaves a resume-derived vector in a live index is not a deletion, and it is the
specific gap that turns a routine request into a reportable incident.

**Backups** are a decided policy, not an oversight — either crypto-shredding with per-user keys or a
documented maximum retention window that we actually state to users. This is an open item in
blueprint §33.3; if your change forces the question, raise it rather than inventing an answer.

## Rule 2 — minimize before any external call

Never send a whole resume to extract skills from one section.

```
BEFORE sending to a model:
  strip contact blocks, names, addresses, phone numbers, national IDs
  send only the section the task needs
  record: model_id, version, inference_region, field_set_sent
```

Resume parsing means names, employment history and sometimes nationality or date of birth leaving our
boundary. The blueprint's rule is that we define what may leave — being specific is what makes that
promise real. Pin the inference region and confirm the provider's retention terms in writing.

Note the extraction cache is keyed on `content_hash`, so **cached extractions are themselves
user-derived data** when the input was a resume. They belong in the inventory above.

## Rule 3 — the audit log is append-only

```
profile.updated | resume.uploaded | resume.deleted
account.deleted | application.created | admin.*
```

`UPDATE` and `DELETE` are revoked for the application role; entries are hash-chained or shipped to
write-once storage. An audit log that the application credentials can rewrite is useless during the
one investigation it exists for.

## Rule 4 — never log PII

Not in errors, not at debug level, not "temporarily while I debug this". Log the `user_id`; log
nothing about the person.

## Rule 5 — the privacy surface is a product feature

These must actually work, not merely exist as endpoints:

- Download my data (real export, all locations)
- Delete my data / delete resume (inventoried, verified)
- Disconnect GitHub
- Disable personalization
- Don't use my profile for model training

Each is only credible if specific. "We take privacy seriously" is not a feature; "your resume is sent
to <provider> in <region>, with contact details stripped, and is not retained" is.

## Storage rules

- Resumes and raw documents live in private object storage, reachable only by short-lived signed URL.
  Never a public bucket, never a permanent URL.
- Every user-owned table carries `tenant_id` and is scoped in one enforcement point, not per query.
- Encryption in transit and at rest, both stated and verified.

## Verify

```bash
make check-erasure
go test ./internal/profile/... ./internal/auth/...
make test -run TestNoPIIInLogs
```

Manual check that finds the real gap: create a user, upload a resume, let it be parsed, embedded and
indexed, request deletion, then grep every store for the identifier — including the vector table and
the extraction cache.

## Done means

- [ ] Every new derived artifact added to the erasure inventory
- [ ] `make check-erasure` passes
- [ ] External calls send the minimum field set, with contact data stripped
- [ ] `model_id`, version, region and field set recorded per call
- [ ] Audit entry emitted for the lifecycle event
- [ ] No PII in any log line
- [ ] Objects private, signed URLs short-lived
- [ ] `tenant_id` present and scoped
