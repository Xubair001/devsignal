import type { FitView } from '@/lib/api/types';
import { cn } from '@/components/ui/cn';

/**
 * The band is the headline, and it comes from the server. Four values, and the
 * fourth is the honest one: "Not enough information" is a statement about our
 * evidence, not about the user, so it gets a neutral tone rather than the
 * warning colour a weak match would earn.
 */
const TONE: Record<FitView['band'], { text: string; glyph: React.ReactNode }> = {
  'Strong fit': {
    text: 'text-good',
    glyph: <path d="m4 12 5 5L20 6" />,
  },
  'Worth a look': {
    text: 'text-brand-ink',
    glyph: (
      <>
        <circle cx="12" cy="12" r="9" />
        <path d="M12 8v8M8 12h8" />
      </>
    ),
  },
  Stretch: {
    text: 'text-warn',
    glyph: <path d="M12 19V5M5 12l7-7 7 7" />,
  },
  'Not enough information': {
    text: 'text-null',
    glyph: (
      <>
        <circle cx="12" cy="12" r="9" />
        <path d="M12 16v.01" />
        <path d="M12 8a2 2 0 0 1 1 3.7c-.6.4-1 .8-1 1.3" />
      </>
    ),
  },
};

export function BandHeader({ fit }: { fit: FitView }) {
  const tone = TONE[fit.band] ?? TONE['Not enough information'];
  const partial = fit.max_points < 100;

  return (
    <div className="flex items-center justify-between gap-2.5 border-y border-line bg-raised px-4 py-2.5">
      <span className={cn('flex items-center gap-1.5 text-[13px] font-semibold', tone.text)}>
        <svg
          viewBox="0 0 24 24"
          fill="none"
          stroke="currentColor"
          strokeWidth="2.2"
          strokeLinecap="round"
          strokeLinejoin="round"
          aria-hidden
          className="size-[15px] shrink-0"
        >
          {tone.glyph}
        </svg>
        {fit.band}
      </span>

      {/* Earned out of achievable — never a percentage, and the "possible"
          wording is what makes a partial model legible. */}
      <span className="whitespace-nowrap font-mono text-[12px] text-ink-2 num">
        <b className="font-semibold text-ink">{fit.points}</b>
        {partial ? ` of a possible ${fit.max_points}` : ` of ${fit.max_points}`}
      </span>
    </div>
  );
}
