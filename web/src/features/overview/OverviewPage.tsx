import { useMemo } from 'react';
import { useQuery } from '@tanstack/react-query';
import { adminApi } from '@/lib/api/admin';
import { qk } from '@/lib/queryKeys';
import type { SloStatus } from '@/lib/api/types';
import { Card } from '@/components/ui/Card';
import { Pill } from '@/components/ui/Pill';
import { ErrorState, SectionHead, SkeletonCards } from '@/components/ui/States';
import { relativeTime, formatDuration } from '@/lib/format';
import { KpiCard } from './KpiCard';
import { SloRow } from './SloRow';

const stroke = {
  fill: 'none' as const,
  stroke: 'currentColor',
  strokeWidth: 2,
  strokeLinecap: 'round' as const,
  strokeLinejoin: 'round' as const,
};

/* Ordered by how much attention each state deserves. Unmeasurable sits last:
   it is a gap to close, not an incident to work. */
const ATTENTION: Record<SloStatus, number> = {
  breached: 0,
  at_risk: 1,
  no_data: 2,
  met: 3,
  unmeasurable: 4,
};

export function OverviewPage() {
  const slo = useQuery({ queryKey: qk.slo(), queryFn: adminApi.slo, refetchInterval: 60_000 });
  const sources = useQuery({ queryKey: qk.sources(), queryFn: adminApi.sources });

  const ranked = useMemo(
    () => [...(slo.data?.results ?? [])].sort((a, b) => ATTENTION[a.status] - ATTENTION[b.status]),
    [slo.data],
  );

  const active = sources.data?.sources.filter((s) => s.status === 'active') ?? [];
  const postings = active.reduce((n, s) => n + s.postings_attributed, 0);
  const live = slo.data?.liveness_verification;
  const backlog = slo.data?.pipeline_states.filter((p) => p.stranded) ?? [];

  return (
    <>
      <SectionHead
        title="Corpus health"
        hint={
          slo.data
            ? `Measured ${relativeTime(slo.data.measured_at)} · ${active.length} active source${active.length === 1 ? '' : 's'}`
            : undefined
        }
      />

      <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 xl:grid-cols-4">
        {(slo.isPending || sources.isPending) && <SkeletonCards count={4} />}

        {slo.isError && <ErrorState error={slo.error} onRetry={() => void slo.refetch()} />}

        {slo.isSuccess && sources.isSuccess && (
          <>
            <KpiCard
              label="Live postings"
              value={postings.toLocaleString()}
              note={`${active.length} active source${active.length === 1 ? '' : 's'}`}
              icon={
                <svg viewBox="0 0 24 24" {...stroke} className="size-[15px]">
                  <path d="M20 7h-9M14 17H5" />
                  <circle cx="17" cy="17" r="3" />
                  <circle cx="7" cy="7" r="3" />
                </svg>
              }
            />

            {/* Verification RECENCY, labelled as such. The accuracy objective is
                unmeasurable and this is not a stand-in for it. */}
            <KpiCard
              label="Verified within 24h"
              value={live ? (live.fraction * 100).toFixed(1) : '—'}
              unit={live ? '%' : undefined}
              note={
                live
                  ? `${live.checked_recently.toLocaleString()} of ${live.shown.toLocaleString()} shown postings`
                  : 'No visible postings to check'
              }
              icon={
                <svg viewBox="0 0 24 24" {...stroke} className="size-[15px]">
                  <path d="M22 11.1V12a10 10 0 1 1-5.9-9.1" />
                  <path d="m9 11 3 3L22 4" />
                </svg>
              }
            />

            <KpiCard
              label="Objectives breached"
              value={String(slo.data.summary.breached)}
              note={
                slo.data.summary.at_risk > 0
                  ? `${slo.data.summary.at_risk} more at risk`
                  : 'Nothing at risk'
              }
              icon={
                <svg viewBox="0 0 24 24" {...stroke} className="size-[15px]">
                  <path d="M12 8v4" />
                  <path d="M12 16h.01" />
                  <circle cx="12" cy="12" r="9" />
                </svg>
              }
            />

            <KpiCard
              label="Not measurable"
              value={String(slo.data.summary.unmeasurable)}
              note="Gaps in what we can observe, not failures"
              icon={
                <svg viewBox="0 0 24 24" {...stroke} className="size-[15px]">
                  <circle cx="12" cy="12" r="9" />
                  <path d="M12 16v.01" />
                  <path d="M12 8a2 2 0 0 1 1 3.7c-.6.4-1 .8-1 1.3" />
                </svg>
              }
            />
          </>
        )}
      </div>

      {live && (
        <p className="mt-2.5 text-[12px] leading-relaxed text-ink-3">
          <b className="font-semibold text-ink-2">On “verified within 24h”:</b> {live.note} Oldest
          check is {formatDuration(live.oldest_check_hours * 3600)} old.
        </p>
      )}

      <SectionHead
        title="Service level objectives"
        hint="Twelve targets. The ones we cannot measure say why, rather than showing green."
        action={
          slo.data && (
            <Pill tone={slo.data.summary.healthy ? 'met' : 'breached'}>
              {slo.data.summary.healthy ? 'Nothing breached' : `${slo.data.summary.breached} breached`}
            </Pill>
          )
        }
      />

      <div className="grid grid-cols-1 gap-2.5 lg:grid-cols-2 2xl:grid-cols-3">
        {slo.isPending && <SkeletonCards count={6} height="h-[76px]" />}
        {ranked.map((r) => (
          <SloRow key={r.id} result={r} />
        ))}
      </div>

      <SectionHead
        title="Pipeline"
        hint="The state distribution is the dashboard. A large count that is moving is healthy; a small one that is not is an incident."
        action={
          backlog.length > 0 ? (
            <Pill tone="breached">{backlog.length} stranded</Pill>
          ) : (
            <Pill tone="met">Nothing stranded</Pill>
          )
        }
      />

      <Card className="overflow-hidden p-0">
        <div className="overflow-x-auto">
          <table className="w-full min-w-[560px] border-collapse">
            <caption className="sr-only">Pipeline state distribution</caption>
            <thead>
              <tr className="border-b border-line bg-raised">
                <Th>Stage</Th>
                <Th align="right">Records</Th>
                <Th align="right">Oldest entry</Th>
                <Th>Status</Th>
              </tr>
            </thead>
            <tbody>
              {slo.data?.pipeline_states.map((p) => {
                const terminal = p.state === 'ready' || p.state === 'failed_permanent';
                const tone = p.records === 0 ? 'no_data' : p.stranded ? 'breached' : 'met';
                const label =
                  p.records === 0 ? 'Empty' : p.stranded ? 'Stranded' : terminal ? 'Terminal' : 'Moving';
                return (
                  <tr key={p.state} className="border-b border-line transition-colors last:border-0 hover:bg-hover">
                    <td className="px-3.5 py-2.5 font-mono text-[13px] font-medium">{p.state}</td>
                    <td className="px-3.5 py-2.5 text-right text-[13px] num">{p.records.toLocaleString()}</td>
                    <td className="px-3.5 py-2.5 text-right font-mono text-[12.5px] text-ink-3">
                      {p.records === 0 ? '—' : relativeTime(p.oldest_entered)}
                    </td>
                    <td className="px-3.5 py-2.5">
                      <Pill tone={tone}>{label}</Pill>
                    </td>
                  </tr>
                );
              })}
              {slo.isSuccess && slo.data.pipeline_states.length === 0 && (
                <tr>
                  <td colSpan={4} className="px-3.5 py-6 text-center text-[13px] text-ink-3">
                    No postings in the pipeline yet. Register a source and run an ingest.
                  </td>
                </tr>
              )}
            </tbody>
          </table>
        </div>
      </Card>
    </>
  );
}

function Th({ children, align = 'left' }: { children: React.ReactNode; align?: 'left' | 'right' }) {
  return (
    <th
      className={
        'px-3.5 py-2.5 text-[11px] font-semibold uppercase tracking-wider text-ink-3 ' +
        (align === 'right' ? 'text-right' : 'text-left')
      }
    >
      {children}
    </th>
  );
}
