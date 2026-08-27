import { http } from './client';
import type { OpportunityPage, OpportunityDetail, SavedResponse } from './types';

export type BrowseFilters = {
  role_family?: string;
  work_mode?: string;
  country?: string;
  cursor?: string;
  page_size?: number;
};

function qs(f: BrowseFilters): string {
  const p = new URLSearchParams();
  // Only send what is set. An empty string is a filter value to the API, not an
  // absence, and sending one silently returns nothing.
  if (f.role_family) p.set('role_family', f.role_family);
  if (f.work_mode) p.set('work_mode', f.work_mode);
  if (f.country) p.set('country', f.country);
  if (f.cursor) p.set('cursor', f.cursor);
  if (f.page_size) p.set('page_size', String(f.page_size));
  const s = p.toString();
  return s ? `?${s}` : '';
}

export const opportunitiesApi = {
  /** Keyset pagination via an opaque cursor. Never construct one client-side. */
  list: (f: BrowseFilters) => http.get<OpportunityPage>(`/api/v1/opportunities${qs(f)}`),
  get: (id: string) => http.get<OpportunityDetail>(`/api/v1/opportunities/${id}`),
  /* Mounted under /engagement, not /feed: the feed is a ranking, the engagement
     log is what the user did to it. Verified against the router. */
  saved: () => http.get<SavedResponse>('/api/v1/engagement/saved'),
};
