import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { feedApi } from '@/lib/api/feed';
import { qk } from '@/lib/queryKeys';
import type { DismissReason, FeedResponse } from '@/lib/api/types';
import { Card } from '@/components/ui/Card';
import { Pill } from '@/components/ui/Pill';
import { useToast } from '@/components/ui/Toast';
import { EmptyState, ErrorState, SectionHead, SkeletonCards } from '@/components/ui/States';
import { JobCard } from './JobCard';

export function FeedPage() {
  const qc = useQueryClient();
  const toast = useToast();

  const feed = useQuery({
    queryKey: qk.feed({ limit: 7 }),
    queryFn: () => feedApi.list(7),
  });

  const excluded = useQuery({
    queryKey: qk.feedExcluded(),
    queryFn: () => feedApi.excluded(),
    // A diagnostic, so it must never block the feed rendering.
    retry: false,
  });

  /* Optimistic, because the feed must feel instant — with a rollback path,
     which is the half people skip. */
  const save = useMutation({
    mutationFn: ({ id, saved }: { id: string; saved: boolean }) =>
      saved ? feedApi.save(id) : feedApi.unsave(id),
    onMutate: async ({ id, saved }) => {
      const key = qk.feed({ limit: 7 });
      await qc.cancelQueries({ queryKey: key });
      const previous = qc.getQueryData<FeedResponse>(key);
      qc.setQueryData<FeedResponse>(key, (old) =>
        old
          ? {
              ...old,
              items: old.items.map((i) =>
                i.opportunity_id === id ? { ...i, state: { ...i.state, saved } } : i,
              ),
            }
          : old,
      );
      return { previous };
    },
    onError: (_e, _v, ctx) => {
      if (ctx?.previous) qc.setQueryData(qk.feed({ limit: 7 }), ctx.previous);
      toast('Could not save that. Try again.', 'bad');
    },
    onSuccess: (_d, v) => toast(v.saved ? 'Saved to your list' : 'Removed from saved'),
    onSettled: () => void qc.invalidateQueries({ queryKey: qk.feed() }),
  });

  const dismiss = useMutation({
    mutationFn: ({ id, reason }: { id: string; reason: DismissReason }) => feedApi.dismiss(id, reason),
    onSuccess: () => {
      toast('Dismissed — the ranking will learn from that');
      void qc.invalidateQueries({ queryKey: qk.feed() });
      void qc.invalidateQueries({ queryKey: qk.feedExcluded() });
    },
    onError: () => toast('Could not record that dismissal', 'bad'),
  });

  const apply = useMutation({
    mutationFn: (id: string) => feedApi.apply(id),
    onSuccess: () => {
      toast('Recorded that you applied');
      void qc.invalidateQueries({ queryKey: qk.feed() });
    },
  });

  const d = feed.data?.diagnostics;
  const thin = d && d.eligible_after_predicates > 0 && (feed.data?.items.length ?? 0) === 0;

  return (
    <>
      <SectionHead
        title="Today's feed"
        hint="Seven roles, ordered by priority. Priority orders the list and is never shown as a match."
      />

      {/* The state of the corpus, stated up front. A card reading"Not enough
          information" is correct rather than broken, and saying so here is
          cheaper than letting every reader wonder. */}
      <Card pad="tight" className="mb-3.5 flex gap-2.5 border-transparent bg-warn-wash">
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden className="mt-0.5 size-4 shrink-0 text-warn">
          <path d="M10.3 3.9 1.8 18a2 2 0 0 0 1.7 3h17a2 2 0 0 0 1.7-3L13.7 3.9a2 2 0 0 0-3.4 0Z" />
          <path d="M12 9v4" />
          <path d="M12 17h.01" />
        </svg>
        <p className="text-meta leading-relaxed">
          <b className="font-semibold">Most roles read “Not enough information” right now.</b> Skill
          extraction has not run against a live model, so required and preferred skills — 45 of the
          model's 100 points — cannot be scored. The band is correct, not broken: we will not call
          something a strong fit on evidence we do not have.
        </p>
      </Card>

      {d && (
        <div className="mb-3.5 flex flex-wrap items-center gap-2 text-meta text-ink-3">
          <Pill tone="neutral">{d.eligible_after_predicates} passed your filters</Pill>
          <Pill tone="neutral">{d.retrieved} retrieved</Pill>
          <Pill tone={d.excluded_by_gate > 0 ? 'at_risk' : 'neutral'}>
            {d.excluded_by_gate} excluded by the gate
          </Pill>
          {d.retrieval_truncated && <Pill tone="at_risk">Candidate set was capped</Pill>}
          {/* Ranked, then closed before the page was written. Worth showing:
              a feed shorter than requested otherwise looks like a thin market. */}
          {d.closed_since_scoring > 0 && (
            <Pill tone="at_risk">{d.closed_since_scoring} closed since scoring</Pill>
          )}
        </div>
      )}

      <div className="grid grid-cols-1 gap-3 md:grid-cols-2 xl:grid-cols-3">
        {feed.isPending && <SkeletonCards count={3} height="h-[340px]" />}

        {feed.isError && <ErrorState error={feed.error} onRetry={() => void feed.refetch()} />}

        {feed.isSuccess && feed.data.items.length === 0 && (
          <EmptyState title="Nothing met your bar today">
            {thin
              ? `The market was quiet. ${d?.eligible_after_predicates} postings matched your filters, but none cleared the gate — check"Not in your feed" below for the specific reason.`
              : 'No postings matched your filters. Loosening your target countries or work mode is usually the fastest fix.'}
          </EmptyState>
        )}

        {feed.data?.items.map((item) => (
          <JobCard
            key={item.opportunity_id}
            item={item}
            onSave={(id, saved) => save.mutate({ id, saved })}
            onApply={(id) => apply.mutate(id)}
            onDismiss={(id, reason) => dismiss.mutate({ id, reason })}
          />
        ))}
      </div>

      <SectionHead title="Not in your feed" hint="The gate excluded these, with the specific reason." />

      <Card pad="none" className="overflow-hidden">
        {excluded.isPending && <div className="p-4"><SkeletonCards count={1} height="h-16" /></div>}
        {excluded.isError && <div className="p-4"><ErrorState error={excluded.error} /></div>}
        {excluded.isSuccess &&
          (excluded.data.items.length === 0 ? (
            <p className="px-4 py-6 text-center text-body text-ink-3">
              Nothing was excluded. Every posting that matched your filters reached the feed.
            </p>
          ) : (
            <div className="overflow-x-auto">
              <table className="w-full min-w-[640px] border-collapse">
                <caption className="sr-only">Postings excluded by the eligibility gate</caption>
                <thead>
                  <tr className="border-b border-line bg-raised">
                    <th className="px-3.5 py-2.5 text-left text-label font-semibold uppercase tracking-wider text-ink-3">Role</th>
                    <th className="px-3.5 py-2.5 text-left text-label font-semibold uppercase tracking-wider text-ink-3">Failed check</th>
                    <th className="px-3.5 py-2.5 text-left text-label font-semibold uppercase tracking-wider text-ink-3">Why</th>
                  </tr>
                </thead>
                <tbody>
                  {excluded.data.items.map((x) => (
                    <tr key={x.opportunity_id} className="border-b border-line transition-colors last:border-0 hover:bg-hover">
                      <td className="px-3.5 py-2.5 text-body font-medium">{x.title}</td>
                      <td className="px-3.5 py-2.5">
                        <span className="flex flex-wrap gap-1">
                          {x.failed_checks.map((c) => (
                            <Pill key={c} tone="breached">{c.replace(/_/g, ' ')}</Pill>
                          ))}
                        </span>
                      </td>
                      <td className="px-3.5 py-2.5 text-meta text-ink-3">{x.reasons.join(' ')}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          ))}
      </Card>
    </>
  );
}
