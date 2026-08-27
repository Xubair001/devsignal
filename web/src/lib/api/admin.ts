import { http } from './client';
import type {
  FlagsResponse,
  HealthResponse,
  MergeCandidatesResponse,
  ProvenanceResponse,
  PurgePlan,
  SloResponse,
  SourcesResponse,
} from './types';

export const adminApi = {
  /* --- provenance and merges ------------------------------------------- */

  /** Every source row on a posting, plus what was merged into it. */
  provenance: (id: string) =>
    http.get<ProvenanceResponse>(`/internal/admin/opportunities/${id}/sources`),

  /**
   * Reverses a merge, restoring the exact source rows it moved.
   *
   * Not a flag flip: it restores data, clears merged_into and stamps
   * unmerged_at, and dedup then skips the pair forever — a human said these are
   * different roles and a simhash does not overrule that.
   */
  unmerge: (id: string, note: string) =>
    http.post<unknown>(`/internal/admin/opportunities/${id}/unmerge`, { note }),

  requeueOpportunity: (id: string, note: string) =>
    http.post<unknown>(`/internal/admin/opportunities/${id}/requeue`, { note }),

  /* --- source purge ----------------------------------------------------- */

  /**
   * Deletes a source's contribution. `confirm` must equal the plan's
   * will_be_deleted — the server checks it, so a stale plan cannot authorise a
   * larger delete than the operator saw.
   */
  purgeSource: (id: string, confirm: number, note: string) =>
    http.post<unknown>(`/internal/admin/sources/${id}/purge`, { confirm, note }),

  mergeCandidates: () =>
    http.get<MergeCandidatesResponse>('/internal/admin/merge-candidates'),
  /**
   * Resolving a candidate records a human judgement on a merge dedup declined to
   * make automatically. Only 'merged' and 'rejected' are accepted — verified
   * against admin.MergeConfirmed / MergeRejected, not guessed.
   */
  resolveMerge: (id: string, resolution: 'merged' | 'rejected', note: string) =>
    http.post<unknown>(`/internal/admin/merge-candidates/${id}/resolve`, { resolution, note }),

  slo: () => http.get<SloResponse>('/internal/admin/slo'),
  sources: () => http.get<SourcesResponse>('/internal/admin/sources'),
  health: (id: string, days = 30) =>
    http.get<HealthResponse>(`/internal/admin/sources/${id}/health?days=${days}`),
  flags: (status = 'open') => http.get<FlagsResponse>(`/internal/admin/flags?status=${status}`),

  setStatus: (id: string, status: 'active' | 'quarantined' | 'retired', note: string) =>
    http.post<{ id: string; name: string; status: string }>(
      `/internal/admin/sources/${id}/status`,
      { status, note },
    ),

  requeueSource: (id: string, targetState: string, note: string) =>
    http.post<{ requeued: number; target_state: string }>(
      `/internal/admin/sources/${id}/requeue`,
      { target_state: targetState, note },
    ),

  /** Counts first. The returned number is the confirmation token for a purge. */
  purgePlan: (id: string) => http.get<PurgePlan>(`/internal/admin/sources/${id}/purge-plan`),

  resolveFlag: (id: string, status: 'upheld' | 'rejected' | 'duplicate', note?: string) =>
    http.post<void>(`/internal/admin/flags/${id}/resolve`, { status, note: note ?? null }),
};
