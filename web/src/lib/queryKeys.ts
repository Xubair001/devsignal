/**
 * Every cache key, in one place.
 *
 * Hierarchical on purpose: invalidating `['feed']` must also drop
 * `['feed', params]`. Inline array literals scattered through components turn
 * invalidation into guesswork, and the symptom is a stale row rendered next to
 * a fresh one.
 */
export const qk = {
  feed: (params?: Record<string, unknown>) => ['feed', params ?? {}] as const,
  feedExcluded: () => ['feed', 'excluded'] as const,
  explanation: (id: string) => ['feed', 'explanation', id] as const,

  slo: () => ['admin', 'slo'] as const,
  sources: () => ['admin', 'sources'] as const,
  sourceHealth: (id: string, days: number) => ['admin', 'sources', id, 'health', days] as const,
  flags: (status?: string) => ['admin', 'flags', status ?? 'open'] as const,
  mergeCandidates: () => ['admin', 'merge-candidates'] as const,

  session: () => ['session'] as const,

  profile: () => ['profile'] as const,
  resumes: () => ['profile', 'resumes'] as const,

  notifications: () => ['notifications'] as const,
  digestHistory: () => ['notifications', 'history'] as const,

  /* Browse keys carry their filters, so two filter sets never share a cache
     entry — and invalidating ['opportunities'] still drops all of them. */
  opportunities: (filters: Record<string, unknown>) => ['opportunities', filters] as const,
  opportunity: (id: string) => ['opportunities', 'detail', id] as const,
  saved: () => ['saved'] as const,
} as const;
