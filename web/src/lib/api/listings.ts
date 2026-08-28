import { http } from './client';
import type { ChoicesResponse, FlagInput } from './types';

/**
 * Reporting a listing.
 *
 * A USER action, not an admin one — it lives under /api/v1/listings rather than
 * behind the operator gate, because the person who spots a scam posting is the
 * person reading it.
 *
 * A report survives its author: erasure anonymises these rows rather than
 * deleting them, since a scam report is about the posting and must not vanish
 * because the reporter closed their account.
 */
export const listingsApi = {
  reasons: () => http.get<ChoicesResponse>('/api/v1/listings/flag-reasons'),
  flag: (id: string, body: FlagInput) =>
    http.post<unknown>(`/api/v1/listings/${id}/flag`, body),
};
