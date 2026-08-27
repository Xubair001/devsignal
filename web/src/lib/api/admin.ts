import { http } from './client';
import type {
  FlagsResponse,
  HealthResponse,
  SloResponse,
  SourcesResponse,
} from './types';

export const adminApi = {
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
