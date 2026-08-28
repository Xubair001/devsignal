import type { SloResult, SloStatus } from '@/lib/api/types';
import { formatObserved } from '@/lib/format';
import { Pill } from '@/components/ui/Pill';
import { cn } from '@/components/ui/cn';

const LABEL: Record<SloStatus, string> = {
  met: 'Met',
  at_risk: 'At risk',
  breached: 'Breached',
  no_data: 'No data',
  unmeasurable: 'Not measurable',
};

/**
 * A severity stripe so the row reads at a glance, plus the pill's glyph and
 * label so nothing depends on colour alone.
 *
 * The two neutral states are the point of this component. `no_data` and
 * `unmeasurable` get no severity colour and no filled background: they are gaps,
 * not incidents. A dashboard that paints them green ends the conversation about
 * what is missing, which is the opposite of what it is for.
 */
const STRIPE: Record<SloStatus, string> = {
  met: 'bg-good',
  at_risk: 'bg-warn',
  breached: 'bg-bad',
  no_data: 'bg-line-strong',
  unmeasurable: 'bg-line-strong',
};

export function SloRow({ result }: { result: SloResult }) {
  const recessed = result.status === 'no_data' || result.status === 'unmeasurable';

  return (
    <article
      className={cn(
        'flex gap-2.5 rounded-[10px] border border-line p-3 transition-[border-color,box-shadow] duration-200',
        recessed ? 'bg-transparent' : 'bg-surface shadow-card',
        'hover:border-line-strong hover:shadow-raise',
      )}
    >
      <span aria-hidden className={cn('w-[3px] shrink-0 self-stretch rounded-sm', STRIPE[result.status])} />

      <div className="min-w-0 flex-1">
        <p className="flex flex-wrap items-center gap-1.5 text-body font-medium">
          {result.description}
          <Pill tone={result.status}>{LABEL[result.status]}</Pill>
          {result.alert_severity !== 'none' && (
            <Pill tone={result.alert_severity === 'page' ? 'breached' : 'at_risk'}>
              {result.alert_severity}
            </Pill>
          )}
        </p>
        <p className="mt-0.5 text-meta leading-relaxed text-ink-3">{result.detail}</p>

        {result.burn_rate !== null && result.burn_rate > 1 && (
          <p className="mt-1 font-mono text-label text-warn num">
            burning {result.burn_rate.toFixed(1)}× of the error budget
          </p>
        )}
      </div>

      <p className="shrink-0 text-right font-mono text-body font-semibold num">
        {formatObserved(result.kind, result.observed)}
        <span className="block text-label font-medium text-ink-3">
          of {formatObserved(result.kind, result.target)}
        </span>
      </p>
    </article>
  );
}
