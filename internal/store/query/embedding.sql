-- name: PutOpportunityEmbedding :exec
-- Keyed by (opportunity_id, embedding_version), which is what makes a model
-- migration possible: both versions coexist while the new one backfills, then
-- reads switch by version and the old rows are dropped (blueprint M-04).
INSERT INTO opportunity_embedding (
    opportunity_id, embedding_model, embedding_version, embedding_dim, embedding
) VALUES ($1,$2,$3,$4,$5)
ON CONFLICT (opportunity_id, embedding_version) DO UPDATE SET
    embedding       = EXCLUDED.embedding,
    embedding_model = EXCLUDED.embedding_model,
    embedding_dim   = EXCLUDED.embedding_dim,
    created_at      = now();

-- name: GetOpportunityEmbedding :one
SELECT * FROM opportunity_embedding
 WHERE opportunity_id = $1 AND embedding_version = $2;

-- name: NearestOpportunities :many
-- Cosine distance against a query vector, restricted to one embedding version.
--
-- The version filter is not optional: vectors from two models are not comparable,
-- so an unfiltered search during a migration would silently mix them and every
-- distance would be meaningless.
--
-- CAVEAT: this is a filtered HNSW search. Postgres may scan the index and then
-- apply the predicates, which can return FEWER than the limit even when more
-- matches exist. Retrieval must therefore treat a short result as "the index ran
-- out", not "there are no more candidates" — and coverage is measured against the
-- eval set rather than assumed (blueprint §21).
SELECT o.id, o.title_raw, o.role_family, o.seniority_ordinal,
       o.location_country, o.work_mode,
       (e.embedding <=> sqlc.arg(query_vector)::vector) AS distance
  FROM opportunity_embedding e
  JOIN opportunity o ON o.id = e.opportunity_id
 WHERE e.embedding_version = sqlc.arg(embedding_version)
   AND o.pipeline_state = 'ready'
   AND o.closed_at IS NULL
   AND o.merged_into IS NULL
 ORDER BY e.embedding <=> sqlc.arg(query_vector)::vector
 LIMIT sqlc.arg(max_candidates)::int;

-- name: CountEmbeddingsByVersion :many
-- Migration visibility: during a dual-write both versions must be watchable, or
-- there is no way to know when the backfill is done.
SELECT embedding_version, embedding_model, count(*)::bigint AS total
  FROM opportunity_embedding
 GROUP BY 1,2 ORDER BY 1;

-- name: DeleteEmbeddingVersion :execrows
-- The last step of a migration, run only once the new version is verified
-- against the eval set. Dropping the old vectors before that is unrecoverable
-- without re-embedding the whole corpus.
DELETE FROM opportunity_embedding WHERE embedding_version = $1;

-- name: OpportunitiesMissingEmbedding :many
-- Backfill driver: rows that have no vector for the current version. Used both
-- for a model migration and to recover from an embedding outage.
SELECT o.id
  FROM opportunity o
 WHERE o.merged_into IS NULL
   AND o.closed_at IS NULL
   AND NOT EXISTS (
     SELECT 1 FROM opportunity_embedding e
      WHERE e.opportunity_id = o.id AND e.embedding_version = sqlc.arg(embedding_version))
 LIMIT sqlc.arg(batch)::int;

-- name: GetOpportunityTextForEmbedding :one
SELECT id, version, title_raw, description_text FROM opportunity WHERE id = $1;

-- name: AdvanceAfterEmbedding :execrows
UPDATE opportunity
   SET pipeline_state = sqlc.arg(next_state),
       version = version + 1,
       next_attempt_at = now(), attempts = 0, last_error = NULL, lease_until = NULL
 WHERE id = sqlc.arg(id)
   AND version = sqlc.arg(version)
   AND pipeline_state = sqlc.arg(current_state);
