import { useState } from 'react';
import { useSearchParams } from 'react-router-dom';
import { useQuery } from '@tanstack/react-query';
import { opportunitiesApi, type BrowseFilters } from '@/lib/api/opportunities';
import { qk } from '@/lib/queryKeys';
import { Card } from '@/components/ui/Card';
import { Select } from '@/components/ui/Field';
import { Button } from '@/components/ui/Button';
import { EmptyState, ErrorState, SkeletonCards } from '@/components/ui/States';
import { PostingRow } from '@/components/ui/PostingRow';
import { PageHeader } from '@/components/ui/PageHeader';
import { Pill } from '@/components/ui/Pill';

const FAMILIES = [
  'backend', 'frontend', 'fullstack', 'mobile', 'data', 'ml', 'platform',
  'security', 'qa', 'design', 'product', 'sales', 'support', 'engineering',
];

/**
 * The corpus browser.
 *
 * Deliberately NOT the primary surface — the blueprint is explicit that a
 * browse screen is the easiest thing to build and turns the product into a job
 * board with extra steps. It exists here because an operator needs to see what
 * was actually ingested, which is a different question from what a user should
 * apply to.
 *
 * Filters live in the URL so a page survives a refresh and can be shared. The
 * cursor does too, which is why it must stay opaque: a client that constructs
 * one has invented a position in a list that ingestion is still changing.
 */
export function BrowsePage() {
  const [params, setParams] = useSearchParams();
  const [cursors, setCursors] = useState<string[]>([]);

  const filters: BrowseFilters = {
    role_family: params.get('family') ?? undefined,
    work_mode: params.get('mode') ?? undefined,
    country: params.get('country') ?? undefined,
    cursor: params.get('cursor') ?? undefined,
    page_size: 25,
  };

  const list = useQuery({
    queryKey: qk.opportunities(filters as Record<string, unknown>),
    queryFn: () => opportunitiesApi.list(filters),
  });

  function setFilter(key: string, value: string) {
    const next = new URLSearchParams(params);
    if (value) next.set(key, value);
    else next.delete(key);
    // A filter change invalidates the cursor: it points at a position in the
    // previous result set, and reusing it silently returns the wrong page.
    next.delete('cursor');
    setCursors([]);
    setParams(next);
  }

  function goNext(cursor: string) {
    const next = new URLSearchParams(params);
    setCursors((c) => [...c, params.get('cursor') ?? '']);
    next.set('cursor', cursor);
    setParams(next);
  }

  function goBack() {
    const prev = cursors[cursors.length - 1];
    const next = new URLSearchParams(params);
    if (prev) next.set('cursor', prev);
    else next.delete('cursor');
    setCursors((c) => c.slice(0, -1));
    setParams(next);
  }

  return (
    <div className="flex flex-col gap-4">
      <PageHeader
        title="Corpus"
        subtitle="Everything ingested and ready to serve, newest first. Closed and merged-away postings are never listed."
      />

      <Card className="flex flex-wrap items-end gap-3 p-3.5">
        <label className="flex flex-col gap-1">
          <span className="text-[11px] font-semibold uppercase tracking-[0.06em] text-ink-3">
            Family
          </span>
          <Select
            value={params.get('family') ?? ''}
            onChange={(e) => setFilter('family', e.target.value)}
            className="w-[160px]"
          >
            <option value="">All families</option>
            {FAMILIES.map((f) => (
              <option key={f} value={f}>{f}</option>
            ))}
          </Select>
        </label>

        <label className="flex flex-col gap-1">
          <span className="text-[11px] font-semibold uppercase tracking-[0.06em] text-ink-3">
            Work mode
          </span>
          <Select
            value={params.get('mode') ?? ''}
            onChange={(e) => setFilter('mode', e.target.value)}
            className="w-[140px]"
          >
            <option value="">Any</option>
            {['remote', 'hybrid', 'onsite'].map((m) => (
              <option key={m} value={m}>{m}</option>
            ))}
          </Select>
        </label>

        {(params.get('family') || params.get('mode') || params.get('country')) && (
          <Button onClick={() => setParams(new URLSearchParams())}>Clear filters</Button>
        )}
      </Card>

      {list.isPending && <SkeletonCards count={1} height="h-[420px]" />}
      {list.isError && <ErrorState error={list.error} onRetry={() => void list.refetch()} />}

      {list.isSuccess && list.data.items.length === 0 && (
        <EmptyState title="Nothing matches those filters">
          The corpus is three ATS boards, and it skews heavily to one employer. A family with no
          results here usually means nothing was ingested for it, not that the filter is broken.
        </EmptyState>
      )}

      {list.isSuccess && list.data.items.length > 0 && (
        <>
          <Card className="overflow-hidden p-0">
            <ul>
              {list.data.items.map((p) => (
                <PostingRow key={p.id} p={p} />
              ))}
            </ul>
          </Card>

          <div className="flex items-center justify-between">
            <Pill tone="neutral">{list.data.items.length} on this page</Pill>
            <div className="flex gap-2">
              <Button onClick={goBack} disabled={cursors.length === 0}>
                Previous
              </Button>
              <Button
                onClick={() => list.data.next_cursor && goNext(list.data.next_cursor)}
                disabled={!list.data.next_cursor}
              >
                Next
              </Button>
            </div>
          </div>
          {!list.data.next_cursor && (
            <p className="text-center text-[12px] text-ink-3">
              End of the corpus. A cursor is only issued for a full page, so a short page is the
              last one.
            </p>
          )}
        </>
      )}
    </div>
  );
}
