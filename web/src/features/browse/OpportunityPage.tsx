import { useParams, Link } from 'react-router-dom';
import { useQuery } from '@tanstack/react-query';
import { opportunitiesApi } from '@/lib/api/opportunities';
import { qk } from '@/lib/queryKeys';
import { Card } from '@/components/ui/Card';
import { Pill } from '@/components/ui/Pill';
import { Button } from '@/components/ui/Button';
import { ErrorState, SkeletonCards } from '@/components/ui/States';
import { formatMoney, formatLocation, relativeTime } from '@/lib/format';
import { cn } from '@/components/ui/cn';

/**
 * One posting in full.
 *
 * The signals block is the part worth care: every value in it is observed, and
 * there is deliberately no competitiveness estimate anywhere — we have no
 * applicant counts, and one invented number discredits the honest ones beside
 * it. `open_similar_roles_at_company` is the closest honest proxy and is
 * labelled as what it is.
 */
export function OpportunityPage() {
  const { id = '' } = useParams();
  const q = useQuery({
    queryKey: qk.opportunity(id),
    queryFn: () => opportunitiesApi.get(id),
    enabled: id !== '',
  });

  if (q.isPending) return <SkeletonCards count={1} height="h-[520px]" />;
  if (q.isError) return <ErrorState error={q.error} onRetry={() => void q.refetch()} />;

  const p = q.data;
  const where = formatLocation(p.location);

  return (
    <div className="flex flex-col gap-4 rise">
      <Link
        to="/browse"
        className="inline-flex w-fit items-center gap-1.5 text-[12.5px] font-medium text-ink-3 transition-colors hover:text-ink"
      >
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.2"
          strokeLinecap="round" strokeLinejoin="round" aria-hidden className="size-3.5">
          <path d="m15 18-6-6 6-6" />
        </svg>
        Back to the corpus
      </Link>

      <Card className="flex flex-col gap-4">
        <div className="flex flex-wrap items-start justify-between gap-3">
          <div className="min-w-0">
            <h1 className="text-[20px] font-bold leading-tight tracking-[-0.022em]">{p.title}</h1>
            <p className="mt-1.5 flex flex-wrap items-center gap-x-2 text-[13px] text-ink-2">
              <span className="font-semibold">{p.company.name}</span>
              {!p.company.domain_confirmed && (
                <span
                  className="text-[11.5px] text-ink-3"
                  title="Identified from the job-board token; no domain confirmed"
                >
                  (unconfirmed company)
                </span>
              )}
              {where && (
                <>
                  <span aria-hidden>·</span>
                  <span>{where}</span>
                </>
              )}
            </p>
          </div>

          {p.apply_url ? (
            <Button as="a" variant="primary" href={p.apply_url} target="_blank" rel="noopener noreferrer" className="h-9">
              Open role
              <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.2"
                strokeLinecap="round" strokeLinejoin="round" aria-hidden className="size-3.5">
                <path d="M7 17 17 7" /><path d="M8 7h9v9" />
              </svg>
            </Button>
          ) : (
            <Pill tone="no_data">No application link published</Pill>
          )}
        </div>

        {/* Liveness, first-class. Ours and theirs are labelled separately so the
            employer's claimed date is never read as our observation. */}
        <div className="flex flex-wrap items-center gap-x-4 gap-y-2 rounded-[10px] border border-line bg-raised/60 px-3.5 py-3">
          <span className="flex items-center gap-2 text-[12.5px] font-semibold">
            <span
              aria-hidden
              className={cn(
                'size-2 rounded-full',
                p.liveness.verified_open
                  ? 'bg-good shadow-[0_0_0_4px_var(--color-good-wash)]'
                  : 'bg-warn shadow-[0_0_0_4px_var(--color-warn-wash)]',
              )}
            />
            <span className={p.liveness.verified_open ? 'text-good' : 'text-warn'}>
              {p.liveness.verified_open ? 'Verified open' : 'Not verified recently'}
            </span>
          </span>
          <Fact label="Checked" value={relativeTime(p.liveness.checked_at)} />
          <Fact label="We first saw it" value={relativeTime(p.liveness.first_seen_at)} />
          <Fact label="Days open (ours)" value={`${p.liveness.days_open}d`} />
          {p.liveness.source_claims_posted_at && (
            <Fact
              label="Source claims posted"
              value={relativeTime(p.liveness.source_claims_posted_at)}
              muted
            />
          )}
        </div>

        <div className="flex flex-wrap items-center gap-2 text-[13px]">
          {p.salary ? (
            <>
              <span className="num font-mono font-semibold">{formatMoney(p.salary)}</span>
              {p.salary.is_estimated && <Pill tone="at_risk">Our estimate, not the employer&apos;s</Pill>}
            </>
          ) : (
            <span className="text-ink-3">Salary not disclosed</span>
          )}
          {p.role.family && <Pill tone="neutral">{p.role.family}</Pill>}
          {p.role.seniority && <Pill tone="neutral">{p.role.seniority}</Pill>}
          {p.role.is_management && <Pill tone="brand">People leadership</Pill>}
          {p.language && <Pill tone="neutral">{p.language}</Pill>}
          <Pill tone="neutral">Visa: {p.visa_sponsorship}</Pill>
        </div>
      </Card>

      <Card>
        <h2 className="text-[14px] font-semibold">Observable signals</h2>
        <p className="mt-1 text-[12px] leading-relaxed text-ink-3">
          Facts only. There is no competitiveness estimate here because we have no applicant
          counts, and one invented figure would discredit every honest one beside it.
        </p>
        <dl className="mt-3.5 grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
          <Stat
            label="Ghost risk"
            value={p.signals.ghost_risk}
            tone={p.signals.ghost_risk === 'normal' ? 'good' : p.signals.ghost_risk === 'high' ? 'bad' : 'warn'}
          />
          <Stat label="Times refreshed" value={String(p.signals.times_refreshed)} />
          <Stat label="Sources seen on" value={String(p.signals.sources_seen_on)} />
          <Stat
            label="Open similar roles here"
            value={String(p.open_similar_roles_at_company)}
            hint="A competition proxy, not an applicant count"
          />
        </dl>
        {p.signals.ghost_risk_reasons.length > 0 && (
          <ul className="mt-3 flex flex-wrap gap-1.5">
            {p.signals.ghost_risk_reasons.map((r) => (
              <li key={r}>
                <Pill tone="at_risk">{r}</Pill>
              </li>
            ))}
          </ul>
        )}
      </Card>

      {p.description_html && (
        <Card>
          <h2 className="text-[14px] font-semibold">Description</h2>
          <div
            className={cn(
              'prose-sm mt-3 max-w-[72ch] text-[13.5px] leading-relaxed text-ink-2',
              '[&_a]:text-brand [&_a]:underline [&_a]:underline-offset-2',
              '[&_h2]:mt-4 [&_h2]:text-[14px] [&_h2]:font-semibold [&_h2]:text-ink',
              '[&_h3]:mt-3 [&_h3]:text-[13.5px] [&_h3]:font-semibold [&_h3]:text-ink',
              '[&_li]:my-1 [&_ol]:my-2 [&_ol]:list-decimal [&_ol]:pl-5',
              '[&_p]:my-2 [&_strong]:font-semibold [&_strong]:text-ink',
              '[&_ul]:my-2 [&_ul]:list-disc [&_ul]:pl-5',
            )}
            /* The body comes from the employer's own posting. It is sanitized
               server-side on ingest; rendering it raw here without that would be
               a stored-XSS hole. */
            dangerouslySetInnerHTML={{ __html: p.description_html }}
          />
        </Card>
      )}
    </div>
  );
}

function Fact({ label, value, muted }: { label: string; value: string; muted?: boolean }) {
  return (
    <span className="flex items-baseline gap-1.5 text-[12px]">
      <span className="text-ink-3">{label}</span>
      <span className={muted ? 'text-ink-3 italic' : 'font-medium text-ink-2'}>{value}</span>
    </span>
  );
}

function Stat({
  label,
  value,
  hint,
  tone,
}: {
  label: string;
  value: string;
  hint?: string;
  tone?: 'good' | 'warn' | 'bad';
}) {
  return (
    <div className="rounded-[10px] border border-line bg-raised/50 px-3 py-2.5">
      <dt className="text-[11px] font-semibold uppercase tracking-[0.06em] text-ink-3">{label}</dt>
      <dd
        className={cn(
          'mt-1 text-[15px] font-semibold capitalize',
          tone === 'good' && 'text-good',
          tone === 'warn' && 'text-warn',
          tone === 'bad' && 'text-bad',
        )}
      >
        {value}
      </dd>
      {hint && <p className="mt-0.5 text-[11px] leading-snug text-ink-3">{hint}</p>}
    </div>
  );
}
