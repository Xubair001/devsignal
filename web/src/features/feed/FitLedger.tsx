import type { FactorView, FitView } from '@/lib/api/types';
import { cn } from '@/components/ui/cn';

/**
 * The explanation, rendered as the arithmetic that produced it.
 *
 * There is deliberately no gauge, ring or meter here. A progress ring IS a
 * percentage, and a percentage implies a probability we have not calibrated —
 * blueprint §3 forbids it. What the server sends is earned points out of
 * achievable points, per factor, and that is exactly what is drawn.
 *
 * Nothing on this component computes a score. `points`, `max_points` and `band`
 * all arrive from the API; if the client derived any of them, two clients would
 * disagree and the explanation would stop matching the backend's decision log.
 */
export function FitLedger({ fit }: { fit: FitView }) {
  return (
    <div className="flex flex-col gap-2 px-4 pb-3 pt-2.5">
      {fit.factors.map((f) => (
        <LedgerRow key={f.factor} factor={f} />
      ))}

      {/* A table fallback for screen readers: the breakdown is the product's
          core claim, so it has to be readable as text, not only as bars. */}
      <table className="sr-only">
        <caption>Fit breakdown, {fit.points} of a possible {fit.max_points} points</caption>
        <thead>
          <tr>
            <th>Factor</th>
            <th>Points earned</th>
            <th>Points achievable</th>
            <th>Note</th>
          </tr>
        </thead>
        <tbody>
          {fit.factors.map((f) => (
            <tr key={f.factor}>
              <td>{f.factor}</td>
              <td>{f.scored ? f.points : 'not scored'}</td>
              <td>{f.scored ? f.max_points : 'not applicable'}</td>
              <td>{f.reason ?? ''}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function LedgerRow({ factor: f }: { factor: FactorView }) {
  const label = f.factor.replace(/_/g, ' ');

  /* An unscored factor is recessed and labelled in words."We could not read
     this posting's required skills" and"you match none of them" are opposite
     statements, and they must not look alike. */
  if (!f.scored) {
    return (
      <div className="grid grid-cols-[1fr_auto] items-center gap-x-2.5 gap-y-1">
        <span className="text-meta text-ink-3">{label}</span>
        <span className="text-meta font-medium italic text-ink-3">not scored</span>
        {f.reason && <span className="col-span-full text-label text-null">{f.reason}</span>}
      </div>
    );
  }

  const pct = f.max_points > 0 ? (f.points / f.max_points) * 100 : 0;

  return (
    <div className="grid grid-cols-[1fr_auto] items-center gap-x-2.5 gap-y-1">
      <span className="text-meta text-ink-2">{label}</span>
      <span className="font-mono text-meta font-semibold num">
        +{f.points} of {f.max_points}
      </span>
      <span
        aria-hidden
        className="col-span-full h-1 overflow-hidden rounded-sm bg-raised"
      >
        <span
          className={cn(
            'block h-full rounded-sm transition-[width] duration-600 ease-out-quart',
            pct === 0 ? 'bg-line-strong' : 'bg-brand',
          )}
          style={{ width: `${Math.max(pct, pct === 0 ? 0 : 2)}%` }}
        />
      </span>
      {f.reason && <span className="col-span-full text-label text-ink-3">{f.reason}</span>}
    </div>
  );
}
