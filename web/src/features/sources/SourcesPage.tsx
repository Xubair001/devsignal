import { useMemo, useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useSearchParams } from 'react-router-dom';
import { adminApi } from '@/lib/api/admin';
import { qk } from '@/lib/queryKeys';
import type { AdminSource } from '@/lib/api/types';
import { relativeTime } from '@/lib/format';
import { Card } from '@/components/ui/Card';
import { Pill } from '@/components/ui/Pill';
import { IconButton } from '@/components/ui/Button';
import { useToast } from '@/components/ui/Toast';
import { EmptyState, ErrorState, SectionHead, Skeleton } from '@/components/ui/States';
import { cn } from '@/components/ui/cn';
import { PurgeSource } from '@/features/admin/PurgeSource';

type SortKey = 'name' | 'status' | 'postings' | 'yield' | 'polled';
const PER_PAGE = 8;

/** Yield is null when nothing was seen — distinct from a yield of zero. */
function parseYield(s: AdminSource): number | null {
  return s.items_discovered > 0 ? s.items_processed / s.items_discovered : null;
}

export function SourcesPage() {
  /* Sort and page live in the URL so a refresh keeps them and a link is
     shareable — the state belongs to the address, not to a component. */
  const [params, setParams] = useSearchParams();
  const sortKey = (params.get('sort') as SortKey | null) ?? 'name';
  const dir = params.get('dir') === 'desc' ? -1 : 1;
  const page = Math.max(1, Number(params.get('page') ?? '1'));

  const qc = useQueryClient();
  const toast = useToast();
  const [busy, setBusy] = useState<string | null>(null);

  const sources = useQuery({ queryKey: qk.sources(), queryFn: adminApi.sources });

  const setStatus = useMutation({
    mutationFn: (v: { id: string; status: 'active' | 'quarantined'; name: string }) =>
      adminApi.setStatus(v.id, v.status, 'Set from the operations console'),
    onMutate: (v) => setBusy(v.id),
    onSettled: () => setBusy(null),
    onSuccess: (_d, v) => {
      /* Quarantine stops polling and nothing else — it deliberately does not
         close the source's postings, and the copy says so. */
      toast(
        v.status === 'quarantined'
          ? `Quarantined ${v.name} — its postings stay open`
          : `Reactivated ${v.name}`,
      );
      void qc.invalidateQueries({ queryKey: qk.sources() });
      void qc.invalidateQueries({ queryKey: qk.slo() });
    },
    onError: () => toast('Could not change that source', 'bad'),
  });

  const requeue = useMutation({
    mutationFn: (v: { id: string; name: string }) =>
      adminApi.requeueSource(v.id, 'normalized', 'Re-run from the operations console'),
    onMutate: (v) => setBusy(v.id),
    onSettled: () => setBusy(null),
    onSuccess: (d, v) => {
      toast(`Requeued ${d.requeued.toLocaleString()} postings from ${v.name}`);
      void qc.invalidateQueries({ queryKey: qk.slo() });
    },
    onError: () => toast('Could not requeue that source', 'bad'),
  });

  const rows = useMemo(() => {
    const all = sources.data?.sources ?? [];
    const value = (s: AdminSource): string | number =>
      ({
        name: s.name,
        status: s.status,
        postings: s.postings_attributed,
        yield: parseYield(s) ?? -1,
        polled: s.last_success_at ? Date.parse(s.last_success_at) : 0,
      })[sortKey];

    return [...all].sort((a, b) => {
      const x = value(a);
      const y = value(b);
      if (typeof x === 'string' && typeof y === 'string') return x.localeCompare(y) * dir;
      return ((x as number) - (y as number)) * dir;
    });
  }, [sources.data, sortKey, dir]);

  const pages = Math.max(1, Math.ceil(rows.length / PER_PAGE));
  const current = Math.min(page, pages);
  const start = (current - 1) * PER_PAGE;
  const shown = rows.slice(start, start + PER_PAGE);

  const setSort = (key: SortKey) => {
    const next = new URLSearchParams(params);
    if (sortKey === key) next.set('dir', dir === 1 ? 'desc' : 'asc');
    else {
      next.set('sort', key);
      next.set('dir', 'asc');
    }
    next.set('page', '1');
    setParams(next, { replace: true });
  };

  const goto = (p: number) => {
    const next = new URLSearchParams(params);
    next.set('page', String(p));
    setParams(next, { replace: true });
  };

  return (
    <>
      <SectionHead
        title="Sources"
        hint="Parse yield is per source, never aggregated — an average stays green while one board rots."
      />

      <Card pad="none" className="overflow-hidden">
        <div className="overflow-x-auto">
          <table className="w-full min-w-[940px] border-collapse">
            <caption className="sr-only">Registered sources with health and legal review state</caption>
            <thead>
              <tr className="border-b border-line bg-raised">
                <SortTh label="Source" k="name" active={sortKey} dir={dir} onSort={setSort} />
                <SortTh label="Status" k="status" active={sortKey} dir={dir} onSort={setSort} />
                <SortTh label="Postings" k="postings" active={sortKey} dir={dir} onSort={setSort} align="right" />
                <SortTh label="Parse yield" k="yield" active={sortKey} dir={dir} onSort={setSort} align="right" />
                <SortTh label="Last success" k="polled" active={sortKey} dir={dir} onSort={setSort} />
                <th className="px-3.5 py-2.5 text-left text-label font-semibold uppercase tracking-wider text-ink-3">
                  Legal review
                </th>
                <th className="px-3.5 py-2.5 text-right text-label font-semibold uppercase tracking-wider text-ink-3">
                  Actions
                </th>
              </tr>
            </thead>

            <tbody>
              {sources.isPending &&
                Array.from({ length: 4 }, (_, i) => (
                  <tr key={i} className="border-b border-line last:border-0">
                    <td colSpan={7} className="px-3.5 py-3.5">
                      <Skeleton className="h-4 w-full" />
                    </td>
                  </tr>
                ))}

              {sources.isSuccess && shown.length === 0 && (
                <tr>
                  <td colSpan={7} className="p-0">
                    <EmptyState title="No sources registered">
                      Register one with{' '}
                      <code className="font-mono text-meta">make add-source name=greenhouse:gitlab</code>{' '}
                      — the reviewable unit is the ATS platform, not the company board.
                    </EmptyState>
                  </td>
                </tr>
              )}

              {shown.map((s) => {
                const y = parseYield(s);
                const low = y !== null && y < 0.98;
                const tone =
                  s.status === 'active' ? 'met' : s.status === 'quarantined' ? 'breached' : 'no_data';
                const working = busy === s.id;

                return (
                  <tr
                    key={s.id}
                    className="group border-b border-line transition-colors last:border-0 hover:bg-hover"
                  >
                    <td className="px-3.5 py-2.5">
                      <span className="flex items-center gap-2">
                        <span
                          title={`Tier ${s.tier.toUpperCase()} — public, documented, unauthenticated`}
                          className="grid size-[18px] shrink-0 place-items-center rounded-[5px] bg-brand-wash text-micro font-bold uppercase text-brand-ink"
                        >
                          {s.tier}
                        </span>
                        <span className="font-mono text-body font-medium">{s.name}</span>
                      </span>
                      <span className="mt-0.5 block pl-[26px] font-mono text-label text-ink-3">
                        {s.type}
                      </span>
                    </td>

                    <td className="px-3.5 py-2.5">
                      <Pill tone={tone}>{s.status[0]!.toUpperCase() + s.status.slice(1)}</Pill>
                    </td>

                    <td className="px-3.5 py-2.5 text-right text-body num">
                      {s.postings_attributed.toLocaleString()}
                    </td>

                    <td className="px-3.5 py-2.5">
                      <span className="flex items-center justify-end gap-2.5">
                        <span className="font-mono text-body num">
                          {y === null ? '—' : `${(y * 100).toFixed(1)}%`}
                        </span>
                        <span aria-hidden className="h-[5px] w-[54px] shrink-0 overflow-hidden rounded-sm bg-raised">
                          <span
                            className={cn('block h-full rounded-sm', low ? 'bg-bad' : 'bg-good')}
                            style={{ width: `${y === null ? 0 : y * 100}%` }}
                          />
                        </span>
                      </span>
                    </td>

                    <td className="px-3.5 py-2.5 font-mono text-meta text-ink-3">
                      {relativeTime(s.last_success_at)}
                    </td>

                    <td className="px-3.5 py-2.5">
                      {/* A source with no recorded review is a compliance
                          problem, and it belongs on the same screen as health. */}
                      {s.reviewed_by ? (
                        <Pill
                          tone="neutral"
                          title={`Reviewed by ${s.reviewed_by}${s.terms_reviewed_at ? ` on ${s.terms_reviewed_at.slice(0, 10)}` : ''}`}
                        >
                          {s.etag_supported ? 'ETag · reviewed' : 'Reviewed'}
                        </Pill>
                      ) : (
                        <Pill tone="at_risk">Not reviewed</Pill>
                      )}
                    </td>

                    <td className="px-3.5 py-2.5">
                      <span className="flex justify-end gap-0.5 opacity-0 transition-opacity duration-150 group-hover:opacity-100 group-focus-within:opacity-100">
                        <IconButton
                          label={`Re-run ${s.name} from normalize`}
                          disabled={working}
                          onClick={() => requeue.mutate({ id: s.id, name: s.name })}
                          className="size-7"
                        >
                          <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" className="size-[15px]">
                            <path d="M3 12a9 9 0 0 1 15-6.7L21 8" />
                            <path d="M21 3v5h-5" />
                            <path d="M21 12a9 9 0 0 1-15 6.7L3 16" />
                            <path d="M3 21v-5h5" />
                          </svg>
                        </IconButton>

                        {s.status === 'active' ? (
                          <IconButton
                            label={`Quarantine ${s.name}`}
                            disabled={working}
                            onClick={() => setStatus.mutate({ id: s.id, status: 'quarantined', name: s.name })}
                            className="size-7 hover:bg-bad-wash hover:text-bad"
                          >
                            <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" className="size-[15px]">
                              <path d="M4.9 4.9l14.2 14.2" />
                              <circle cx="12" cy="12" r="9" />
                            </svg>
                          </IconButton>
                        ) : (
                          <IconButton
                            label={`Reactivate ${s.name}`}
                            disabled={working}
                            onClick={() => setStatus.mutate({ id: s.id, status: 'active', name: s.name })}
                            className="size-7 hover:bg-good-wash hover:text-good"
                          >
                            <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" className="size-[15px]">
                              <path d="m5 12 5 5L20 6" />
                            </svg>
                          </IconButton>
                        )}

                        {/* Purge is the destructive one, so it is a labelled
                            button rather than an icon: an icon is too easy to hit
                            by accident for an action that deletes postings. */}
                        <PurgeSource sourceID={s.id} sourceName={s.name} />
                      </span>
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>

        {sources.isSuccess && rows.length > 0 && (
          <div className="flex flex-wrap items-center justify-between gap-3 border-t border-line px-3.5 py-2.5">
            <span className="text-meta text-ink-3 num">
              {start + 1}–{Math.min(start + PER_PAGE, rows.length)} of {rows.length} sources
            </span>
            <span className="flex items-center gap-1">
              <PageBtn label="Previous page" disabled={current === 1} onClick={() => goto(current - 1)}>
                <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.4" strokeLinecap="round" className="size-3.5">
                  <path d="m15 18-6-6 6-6" />
                </svg>
              </PageBtn>
              {Array.from({ length: pages }, (_, i) => (
                <PageBtn key={i} label={`Page ${i + 1}`} current={current === i + 1} onClick={() => goto(i + 1)}>
                  {i + 1}
                </PageBtn>
              ))}
              <PageBtn label="Next page" disabled={current === pages} onClick={() => goto(current + 1)}>
                <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.4" strokeLinecap="round" className="size-3.5">
                  <path d="m9 6 6 6-6 6" />
                </svg>
              </PageBtn>
            </span>
          </div>
        )}
      </Card>

      {sources.isError && (
        <div className="mt-3">
          <ErrorState error={sources.error} onRetry={() => void sources.refetch()} />
        </div>
      )}
    </>
  );
}

function SortTh({
  label,
  k,
  active,
  dir,
  onSort,
  align = 'left',
}: {
  label: string;
  k: SortKey;
  active: SortKey;
  dir: number;
  onSort: (k: SortKey) => void;
  align?: 'left' | 'right';
}) {
  const on = active === k;
  return (
    <th
      aria-sort={on ? (dir === 1 ? 'ascending' : 'descending') : undefined}
      className="p-0 text-label font-semibold uppercase tracking-wider text-ink-3"
    >
      <button
        onClick={() => onSort(k)}
        className={cn(
          'flex w-full cursor-pointer items-center gap-1.5 px-3.5 py-2.5 transition-colors hover:text-ink',
          align === 'right' && 'justify-end',
        )}
      >
        {label}
        <svg
          viewBox="0 0 24 24"
          fill="none"
          stroke="currentColor"
          strokeWidth="2.4"
          strokeLinecap="round"
          aria-hidden
          className={cn(
            'size-3 transition-[opacity,transform] duration-200',
            on ? 'text-brand-ink opacity-100' : 'opacity-0',
            on && dir === -1 && 'rotate-180',
          )}
        >
          <path d="m6 15 6 6 6-6" />
        </svg>
      </button>
    </th>
  );
}

function PageBtn({
  children,
  label,
  disabled,
  current,
  onClick,
}: {
  children: React.ReactNode;
  label: string;
  disabled?: boolean;
  current?: boolean;
  onClick: () => void;
}) {
  return (
    <button
      aria-label={label}
      aria-current={current ? 'page' : undefined}
      disabled={disabled}
      onClick={onClick}
      className={cn(
        'grid h-[30px] min-w-[30px] cursor-pointer place-items-center rounded-md border px-2 text-meta font-medium num transition-colors',
        current
          ? 'border-transparent bg-brand text-white'
          : 'border-line bg-surface text-ink-2 hover:border-line-strong hover:bg-raised hover:text-ink',
        disabled && 'pointer-events-none opacity-40',
      )}
    >
      {children}
    </button>
  );
}
