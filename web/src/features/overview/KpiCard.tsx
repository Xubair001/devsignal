import type { ReactNode } from 'react';
import { Card } from '@/components/ui/Card';
import { Sparkline } from '@/components/ui/Sparkline';
import { cn } from '@/components/ui/cn';

type Props = {
  label: string;
  value: string;
  unit?: string;
  note: string;
  icon: ReactNode;
  /** Omitted when there is no trend to show. A flat fake line is worse than none. */
  series?: number[];
  delta?: { pct: number; lowerIsBetter?: boolean };
};

export function KpiCard({ label, value, unit, note, icon, series, delta }: Props) {
  /* Latency falling is good and a count falling is not, so the badge tone comes
     from `lowerIsBetter` rather than from the sign of the number. */
  const tone =
    !delta || delta.pct === 0
      ? 'flat'
      : (delta.lowerIsBetter ? delta.pct < 0 : delta.pct > 0)
        ? 'up'
        : 'down';

  return (
    <Card lift className="flex flex-col gap-2.5 p-4 pb-3">
      <div className="flex items-start justify-between gap-2">
        <span className="text-[12.5px] font-medium text-ink-2">{label}</span>
        <span aria-hidden className="grid size-7 shrink-0 place-items-center rounded-lg bg-brand-wash text-brand-ink">
          {icon}
        </span>
      </div>

      <p className="text-[30px] font-semibold leading-none tracking-tight num">
        {value}
        {unit && <span className="text-[15px] font-medium tracking-normal text-ink-3">{unit}</span>}
      </p>

      {series && series.length > 1 ? (
        <Sparkline
          series={series}
          tone={tone === 'down' ? 'bad' : tone === 'flat' ? 'null' : 'good'}
          label={`${label} trend, last ${series.length} days`}
        />
      ) : (
        <span aria-hidden className="h-[30px]" />
      )}

      <div className="flex items-center justify-between gap-2.5">
        <span className="text-[11.5px] text-ink-3">{note}</span>
        {delta && (
          <span
            className={cn(
              'inline-flex items-center gap-0.5 rounded-md px-1.5 py-0.5 text-[11.5px] font-semibold num',
              tone === 'up' && 'bg-good-wash text-good',
              tone === 'down' && 'bg-bad-wash text-bad',
              tone === 'flat' && 'bg-null-wash text-null',
            )}
          >
            <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.6" strokeLinecap="round" aria-hidden className="size-[11px]">
              {delta.pct === 0 ? (
                <path d="M5 12h14" />
              ) : delta.pct > 0 ? (
                <path d="M12 19V5M5 12l7-7 7 7" />
              ) : (
                <path d="M12 5v14M5 12l7 7 7-7" />
              )}
            </svg>
            {delta.pct === 0 ? 'flat' : `${Math.abs(delta.pct).toFixed(1)}%`}
          </span>
        )}
      </div>
    </Card>
  );
}
