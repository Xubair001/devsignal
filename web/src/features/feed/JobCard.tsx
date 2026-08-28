import { useState } from 'react';
import { useQuery } from '@tanstack/react-query';
import type { FeedItem, DismissReason } from '@/lib/api/types';
import { feedApi } from '@/lib/api/feed';
import { ReportListing } from './ReportListing';
import { formatMoney, formatLocation, relativeTime } from '@/lib/format';
import { Card } from '@/components/ui/Card';
import { Pill } from '@/components/ui/Pill';
import { Button } from '@/components/ui/Button';
import { cn } from '@/components/ui/cn';
import { BandHeader } from './BandHeader';
import { FitLedger } from './FitLedger';

type Props = {
  item: FeedItem;
  onSave: (id: string, saved: boolean) => void;
  onApply: (id: string) => void;
  onDismiss: (id: string, reason: DismissReason) => void;
};

/** Ghost risk is an observable signal, so its reasons travel with the band. */
const GHOST_LABEL = { elevated: 'Ghost risk: elevated', high: 'Ghost risk: high' } as const;

export function JobCard({ item, onSave, onApply, onDismiss }: Props) {
  const [picking, setPicking] = useState(false);

  /* The reason set comes from the SERVER, not a client copy.
     A dismissal reason is a training label, and the label vocabulary belongs to
     whatever will learn from it — a hardcoded list drifts the moment the set
     changes, and the symptom is a reason the server rejects or, worse, stores as
     something else. Fetched only when the picker opens, and cached forever
     because a closed set does not change within a session. */
  const reasons = useQuery({
    queryKey: ['dismiss-reasons'],
    queryFn: () => feedApi.dismissReasons(),
    staleTime: Infinity,
    enabled: picking,
  });
  const p = item.posting;
  const live = p.liveness;
  const where = formatLocation(p.location);
  const ghost = p.signals.ghost_risk;

  return (
    <Card as="article" lift pad="none" className="flex flex-col overflow-hidden">
      <div className="flex flex-col gap-2.5 p-4 pb-3">
        <h3 className="text-base font-semibold leading-snug">{item.title}</h3>

        <p className="flex flex-wrap items-center gap-x-1.5 text-meta text-ink-3">
          <span className="font-medium text-ink-2">{p.company.name}</span>
          {/* An unconfirmed domain means the identity came from a board token,
              not from the company. Saying so is cheaper than being wrong. */}
          {!p.company.domain_confirmed && (
            <span
              title="Company identified from its job-board token; we have not confirmed a domain"
              className="text-ink-3"
            >
              (unconfirmed)
            </span>
          )}
          {where && (
            <>
              <span aria-hidden>·</span>
              <span>{where}</span>
            </>
          )}
        </p>

        {/* Liveness is the product's central claim, so it is a first-class row
            rather than a timestamp the reader has to interpret. The server drops
            an item it cannot describe, so this is never absent. */}
        <p className="flex flex-wrap items-center gap-x-1.5 gap-y-1 text-label font-medium">
          <span
            aria-hidden
            className={cn(
              'size-1.5 shrink-0 rounded-full',
              live.verified_open
                ? 'bg-good shadow-[0_0_0_3px_var(--color-good-wash)]'
                : 'bg-warn shadow-[0_0_0_3px_var(--color-warn-wash)]',
            )}
          />
          <span className={live.verified_open ? 'text-good' : 'text-warn'}>
            {live.verified_open ? 'Verified open' : 'Not verified recently'}
          </span>
          <span className="font-normal text-ink-3">· checked {relativeTime(live.checked_at)}</span>
          {/* Our own first sighting, which is the only age we can vouch for. The
              employer's claimed post date is deliberately not shown here. */}
          {live.days_open > 0 && (
            <span className="font-normal text-ink-3">
              · open {live.days_open}d since we first saw it
            </span>
          )}
        </p>

        {ghost !== 'normal' && (
          <Pill tone={ghost === 'high' ? 'breached' : 'at_risk'}>
            {GHOST_LABEL[ghost]}
            {p.signals.ghost_risk_reasons.length > 0 &&
              ` — ${p.signals.ghost_risk_reasons.join('; ')}`}
          </Pill>
        )}

        <p className="flex flex-wrap items-center gap-2 text-meta">
          {/* `salary: null` is its own state. Defaulting it to"Competitive" is
              exactly the invented field the display rules forbid. */}
          {p.salary ? (
            <>
              <span className="font-mono font-medium text-ink-2">{formatMoney(p.salary)}</span>
              {p.salary.is_estimated && <Pill tone="at_risk">Our estimate, not the employer's</Pill>}
            </>
          ) : (
            <span className="text-ink-3">Salary not disclosed</span>
          )}
          {item.state.applied && (
            <Pill tone="brand">You told us you applied {relativeTime(item.state.applied_at)}</Pill>
          )}
        </p>
      </div>

      <BandHeader fit={item.fit} />
      <FitLedger fit={item.fit} />

      <div className="mt-auto flex items-center gap-1.5 border-t border-line px-4 py-2.5">
        {/* A real link when we have one:"Open role" that opens nothing is the
            kind of small dishonesty that costs trust cheaply. noreferrer as well
            as noopener so the employer's page learns nothing about the user. */}
        <Button
          as={p.apply_url ? 'a' : 'button'}
          variant="primary"
          {...(p.apply_url
            ? { href: p.apply_url, target: '_blank', rel: 'noopener noreferrer' }
            : { disabled: true, title: 'No application link was published for this role' })}
          onClick={() => onApply(item.opportunity_id)}
        >
          <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.2" strokeLinecap="round" strokeLinejoin="round" aria-hidden className="size-3.5">
            <path d="M7 17 17 7" />
            <path d="M8 7h9v9" />
          </svg>
          {p.apply_url ? 'Open role' : 'No link published'}
        </Button>

        <Button
          aria-pressed={item.state.saved}
          onClick={() => onSave(item.opportunity_id, !item.state.saved)}
        >
          <svg
            viewBox="0 0 24 24"
            fill={item.state.saved ? 'currentColor' : 'none'}
            stroke="currentColor"
            strokeWidth="2"
            strokeLinecap="round"
            strokeLinejoin="round"
            aria-hidden
            className="size-3.5"
          >
            <path d="m19 21-7-5-7 5V5a2 2 0 0 1 2-2h10a2 2 0 0 1 2 2Z" />
          </svg>
          {item.state.saved ? 'Saved' : 'Save'}
        </Button>

        <div className="ml-auto flex items-center gap-1">
          <ReportListing opportunityID={item.opportunity_id} title={item.title} />
        </div>

        <div className="relative">
          <Button variant="ghost" aria-expanded={picking} onClick={() => setPicking((p) => !p)}>
            <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" aria-hidden className="size-3.5">
              <path d="M18 6 6 18M6 6l12 12" />
            </svg>
            Dismiss
          </Button>

          {/* A dismissal always asks for a reason. Dropping it loses a training
              label, and those are not recoverable later. */}
          {picking && (
            <div
              role="menu"
              aria-label="Why is this not a fit?"
              className="absolute bottom-[calc(100%+8px)] right-0 z-70 w-[240px] rounded-[14px] border border-line bg-surface p-1.5 shadow-float"
            >
              <p className="px-2.5 py-1.5 text-label font-semibold uppercase tracking-wider text-ink-3">
                Why is this not a fit?
              </p>
              {reasons.isPending && (
                <p className="px-2.5 py-2 text-meta text-ink-3">Loading reasons…</p>
              )}
              {reasons.data?.reasons.map((r) => (
                <button
                  key={r.value}
                  role="menuitem"
                  onClick={() => {
                    setPicking(false);
                    onDismiss(item.opportunity_id, r.value as DismissReason);
                  }}
                  className="flex w-full cursor-pointer rounded-md px-2.5 py-2 text-left text-body text-ink-2 transition-colors hover:bg-raised hover:text-ink"
                >
                  {r.label}
                </button>
              ))}
            </div>
          )}
        </div>
      </div>
    </Card>
  );
}
