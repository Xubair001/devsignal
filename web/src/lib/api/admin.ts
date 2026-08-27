import { http } from './client';
import type {
  FlagsResponse,
  HealthResponse,
  MergeCandidatesResponse,
  SloResponse,
  SourcesResponse,
} from './types';

export const adminApi = {
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
  purgePlan: (id: string) =>
    http.get<{
      source_id: string;
      total_attributed: number;
      merged: number;
      also_seen_elsewhere: number;
      will_be_deleted: number;
    }>(`/internal/admin/sources/${id}/purge-plan`),

  resolveFlag: (id: string, status: 'upheld' | 'rejected' | 'duplicate', note?: string) =>
    http.post<void>(`/internal/admin/flags/${id}/resolve`, { status, note: note ?? null }),
};
