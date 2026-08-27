import { Link } from 'react-router-dom';
import type { Posting } from '@/lib/api/types';
import { formatMoney, formatLocation, relativeTime } from '@/lib/format';
import { Pill } from './Pill';
import { cn } from './cn';

/**
 * One posting, in list form. Shared by browse and saved so the two cannot
 * disagree about how a role is described.
 *
 * Liveness is present on every row for the same reason it is on a feed card: it
 * is the product's central claim, and a list that shows a role without it
 * invites the reader to assume the role is open.
 */
export function PostingRow({ p, right }: { p: Posting; right?: React.ReactNode }) {
  const where = formatLocation(p.location);
  return (
    <li
      className={cn(
        'group flex items-start gap-3.5 border-b border-line px-4 py-3.5 last:border-0',
        'transition-colors duration-[var(--dur-fast)] hover:bg-hover',
      )}
    >
      <span
        aria-hidden
        title={p.liveness.verified_open ? 'Verified open' : 'Not verified recently'}
        className={cn(
          'mt-[7px] size-1.5 shrink-0 rounded-full',
          p.liveness.verified_open
            ? 'bg-good shadow-[0_0_0_3px_var(--color-good-wash)]'
            : 'bg-warn shadow-[0_0_0_3px_var(--color-warn-wash)]',
        )}
      />

      <div className="min-w-0 flex-1">
        <Link
          to={`/browse/${p.id}`}
          className="text-[13.5px] font-semibold leading-snug decoration-line-strong underline-offset-2 hover:underline"
        >
          {p.title}
        </Link>

        <p className="mt-0.5 flex flex-wrap items-center gap-x-1.5 text-[12px] text-ink-3">
          <span className="font-medium text-ink-2">{p.company.name}</span>
          {where && (
            <>
              <span aria-hidden>·</span>
              <span>{where}</span>
            </>
          )}
          {p.role.seniority && (
            <>
              <span aria-hidden>·</span>
              <span>{p.role.seniority}</span>
            </>
          )}
        </p>

        <p className="mt-1.5 flex flex-wrap items-center gap-2 text-[12px]">
          {p.salary ? (
            <span className="num font-mono font-medium text-ink-2">{formatMoney(p.salary)}</span>
          ) : (
            <span className="text-ink-3">Salary not disclosed</span>
          )}
          <span className={p.liveness.verified_open ? 'text-good' : 'text-warn'}>
            {p.liveness.verified_open ? 'Verified open' : 'Unverified'}
          </span>
          <span className="text-ink-3">· checked {relativeTime(p.liveness.checked_at)}</span>
          {p.signals.ghost_risk !== 'normal' && (
            <Pill tone={p.signals.ghost_risk === 'high' ? 'breached' : 'at_risk'}>
              Ghost risk: {p.signals.ghost_risk}
            </Pill>
          )}
        </p>
      </div>

      {right && <div className="flex shrink-0 items-center gap-1.5">{right}</div>}
    </li>
  );
}
