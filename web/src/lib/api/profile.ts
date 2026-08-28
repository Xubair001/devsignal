import { http } from './client';
import type {
  Profile,
  ProfileInput,
  ResumesResponse,
  NotificationSettings,
  DigestHistory,
} from './types';

export const profileApi = {
  get: () => http.get<Profile>('/api/v1/profile'),
  save: (body: ProfileInput) => http.put<Profile>('/api/v1/profile', body),

  resumes: () => http.get<ResumesResponse>('/api/v1/profile/resumes'),
  deleteResume: (id: string) => http.del<void>(`/api/v1/profile/resumes/${id}`),

  /**
   * Erasure. Not a soft delete and not reversible — the server inventories every
   * derived artifact and verifies the result, so this genuinely removes the
   * account. The UI must make that unambiguous before calling it.
   */
  eraseAccount: () => http.del<unknown>('/api/v1/profile'),
};

export const notificationsApi = {
  get: () => http.get<NotificationSettings>('/api/v1/notifications'),
  save: (body: {
    timezone: string;
    quiet_start: number;
    quiet_end: number;
    digest_enabled: boolean;
    max_per_week: number;
    min_band: 'strong' | 'worth_a_look';
    send_when_empty: boolean;
  }) => http.put<NotificationSettings>('/api/v1/notifications', body),
  /** The wording version is required: consent without it is not evidence. */
  consent: (wording_version: string) =>
    http.post<NotificationSettings>('/api/v1/notifications/consent', { wording_version }),
  withdraw: () => http.del<NotificationSettings>('/api/v1/notifications/consent'),
  history: () => http.get<DigestHistory>('/api/v1/notifications/history'),
};
