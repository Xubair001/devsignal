import { http } from './client';
import type {
  ChoicesResponse,
  DismissReason,
  ExcludedResponse,
  FeedResponse,
  FitView,
} from './types';

export const feedApi = {
  /** The closed set of dismissal reasons, from the server that owns it. */
  dismissReasons: () => http.get<ChoicesResponse>('/api/v1/engagement/dismiss-reasons'),

  list: (limit = 7) => http.get<FeedResponse>(`/api/v1/feed?limit=${limit}`),
  excluded: () => http.get<ExcludedResponse>('/api/v1/feed/excluded'),
  explanation: (id: string) => http.get<FitView>(`/api/v1/feed/${id}/explanation`),

  save: (id: string) => http.post<void>(`/api/v1/engagement/saved/${id}`),
  unsave: (id: string) => http.del<void>(`/api/v1/engagement/saved/${id}`),
  apply: (id: string) => http.post<void>(`/api/v1/engagement/applied/${id}`),
  open: (id: string) => http.post<void>(`/api/v1/engagement/opened/${id}`),

  /**
   * The reason is required by the server and must reach it. A dismissal whose
   * reason is dropped is a lost training label, and those are not recoverable.
   */
  dismiss: (id: string, reason: DismissReason) =>
    http.post<void>(`/api/v1/engagement/dismissed/${id}`, { reason }),
};
