import type { Money } from './api/types';

/**
 * Money is formatted at the render edge, never stored formatted and never
 * arithmetic'd beyond choosing a display range. Minor units in, string out.
 */
export function formatMoney(m: Money, locale?: string): string {
  const one = (minor: number) =>
    (minor / 100).toLocaleString(locale, {
      style: 'currency',
      currency: m.currency,
      maximumFractionDigits: 0,
    });
  const range =
    m.max_minor !== null && m.max_minor !== m.min_minor
      ? `${one(m.min_minor)}–${one(m.max_minor)}`
      : one(m.min_minor);
  return `${range} / ${m.period}`;
}

const UNITS: [Intl.RelativeTimeFormatUnit, number][] = [
  ['year', 31_536_000],
  ['month', 2_592_000],
  ['day', 86_400],
  ['hour', 3_600],
  ['minute', 60],
];

/** "12 minutes ago" — the phrasing liveness is shown with. */
export function relativeTime(iso: string | null, locale?: string): string {
  if (!iso) return 'never';
  const then = Date.parse(iso);
  if (Number.isNaN(then)) return 'unknown';
  const secs = Math.round((then - Date.now()) / 1000);
  const rtf = new Intl.RelativeTimeFormat(locale, { numeric: 'auto' });
  for (const [unit, size] of UNITS) {
    if (Math.abs(secs) >= size) return rtf.format(Math.round(secs / size), unit);
  }
  return rtf.format(secs, 'second');
}

export function hoursSince(iso: string | null): number | null {
  if (!iso) return null;
  const then = Date.parse(iso);
  if (Number.isNaN(then)) return null;
  return (Date.now() - then) / 3_600_000;
}

/** Renders an SLO's observed value in its own units. */
export function formatObserved(
  kind: 'ratio' | 'latency' | 'duration' | 'count',
  v: number | null,
): string {
  if (v === null) return '—';
  switch (kind) {
    case 'ratio':
      return `${(v * 100).toFixed(v >= 0.999 ? 1 : 2)}%`;
    case 'latency':
      return `${Math.round(v)}ms`;
    case 'duration':
      return formatDuration(v / 1e9);
    case 'count':
      return v.toLocaleString();
  }
}

export function formatDuration(seconds: number): string {
  if (seconds < 60) return `${Math.round(seconds)}s`;
  if (seconds < 3600) {
    const m = Math.floor(seconds / 60);
    const s = Math.round(seconds % 60);
    return s ? `${m}m ${s}s` : `${m}m`;
  }
  const h = Math.floor(seconds / 3600);
  const m = Math.round((seconds % 3600) / 60);
  return m ? `${h}h ${m}m` : `${h}h`;
}

/**
 * Renders the location the API actually described, and nothing more.
 *
 * All four parts are frequently absent — an ATS board commonly gives a work
 * mode and a geo scope and no city at all — so this joins what is present
 * rather than filling gaps. "Remote (US, CA)" is honest; "Remote, San
 * Francisco" invented from a scope is not.
 */
export function formatLocation(loc: {
  city: string | null;
  country: string | null;
  work_mode: string | null;
  geo_scope: string[];
}): string | null {
  const place = [loc.city, loc.country].filter(Boolean).join(', ');
  const mode = loc.work_mode
    ? loc.work_mode.charAt(0).toUpperCase() + loc.work_mode.slice(1)
    : null;

  // A geo scope only means something for remote roles, where it is the eligible
  // hiring region. Attached to an onsite role it would read as an office list.
  const scope =
    loc.work_mode === 'remote' && loc.geo_scope.length > 0
      ? `(${loc.geo_scope.slice(0, 4).join(', ')}${loc.geo_scope.length > 4 ? '…' : ''})`
      : null;

  const parts = [mode, place || null, scope].filter(Boolean);
  return parts.length ? parts.join(' · ') : null;
}
