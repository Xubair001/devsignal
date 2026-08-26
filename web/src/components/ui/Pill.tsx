import type { ReactNode } from 'react';
import { cn } from './cn';

export type Tone = 'met' | 'at_risk' | 'breached' | 'no_data' | 'unmeasurable' | 'neutral' | 'brand';

/**
 * A glyph travels with every tone. Meaning is never encoded in colour alone —
 * the glyph and the label carry the same information for anyone who cannot
 * distinguish the hues, and for a screen reader.
 */
const GLYPH: Record<Tone, string> = {
  met: '✓',
  at_risk: '◐',
  breached: '✕',
  no_data: '—',
  unmeasurable: '?',
  neutral: '',
  brand: '',
};

const TONE: Record<Tone, string> = {
  met: 'bg-good-wash text-good border-transparent',
  at_risk: 'bg-warn-wash text-warn border-transparent',
  breached: 'bg-bad-wash text-bad border-transparent',
  no_data: 'bg-transparent text-ink-3 border-line-strong',
  // Dashed, unfilled: a gap in what we can measure, not a severity.
  unmeasurable: 'bg-transparent text-null border-line border-dashed',
  neutral: 'bg-raised text-ink-2 border-transparent',
  brand: 'bg-brand-wash text-brand-ink border-transparent',
};

export function Pill({
  tone,
  children,
  title,
}: {
  tone: Tone;
  children: ReactNode;
  title?: string;
}) {
  const glyph = GLYPH[tone];
  return (
    <span
      title={title}
      className={cn(
        'inline-flex shrink-0 items-center gap-1 whitespace-nowrap rounded-full border',
        'px-[7px] py-[1.5px] text-[11px] font-semibold',
        TONE[tone],
      )}
    >
      {glyph && (
        <span aria-hidden className="text-[10px] leading-none">
          {glyph}
        </span>
      )}
      {children}
    </span>
  );
}
