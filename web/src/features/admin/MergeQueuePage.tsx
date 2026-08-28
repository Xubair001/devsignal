import { useState } from 'react';
import { Link } from 'react-router-dom';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { adminApi } from '@/lib/api/admin';
import { qk } from '@/lib/queryKeys';
import { Card } from '@/components/ui/Card';
import { Pill } from '@/components/ui/Pill';
import { Button } from '@/components/ui/Button';
import { Input } from '@/components/ui/Field';
import { EmptyState, ErrorState, SkeletonCards } from '@/components/ui/States';
import { PageHeader } from '@/components/ui/PageHeader';
import { useToast } from '@/components/ui/Toast';
import { relativeTime } from '@/lib/format';

/**
 * Merges dedup declined to make automatically.
 *
 * The asymmetry between the two buttons is the point. Confirming a merge hides
 * one of the two postings behind the other; rejecting one records that a person
 * judged them separate, and dedup then never proposes it again. A false merge
 * hides a real job and is otherwise invisible, which is why these are withheld
 * for review instead of applied on a confidence threshold.
 */
export function MergeQueuePage() {
  const qc = useQueryClient();
  const toast = useToast();
  const [notes, setNotes] = useState<Record<string, string>>({});

  const queue = useQuery({
    queryKey: qk.mergeCandidates(),
    queryFn: () => adminApi.mergeCandidates(),
  });

  const resolve = useMutation({
    mutationFn: (v: { id: string; resolution: 'merged' | 'rejected' }) =>
      adminApi.resolveMerge(v.id, v.resolution, notes[v.id] ?? ''),
    onSuccess: (_d, v) => {
      void qc.invalidateQueries({ queryKey: qk.mergeCandidates() });
      toast(
        v.resolution === 'merged'
          ? 'Recorded as the same role'
          : 'Recorded as different roles. Dedup will not propose this pair again.',
      );
    },
    onError: () => toast('Could not record the decision', 'bad'),
  });

  return (
    <div className="flex flex-col gap-4 sm:gap-5">
      <PageHeader
        title="Merge review"
        subtitle="Pairs dedup thought might be duplicates but would not merge on its own. A false merge hides a real job and is otherwise invisible, so these wait for a person."
        aside={
          queue.isSuccess ? (
            <Pill tone={queue.data.candidates.length > 0 ? 'at_risk' : 'met'}>
              <span className="num">{queue.data.candidates.length}</span> waiting
            </Pill>
          ) : undefined
        }
      />

      {queue.isPending && <SkeletonCards count={2} height="h-[150px]" />}
      {queue.isError && <ErrorState error={queue.error} onRetry={() => void queue.refetch()} />}

      {queue.isSuccess && queue.data.candidates.length === 0 && (
        <EmptyState title="Nothing waiting for review">
          Dedup either merged confidently or found nothing similar enough to ask about. This queue
          filling up is normal as the corpus grows — the same role cross-posted to two boards is
          the usual cause.
        </EmptyState>
      )}

      {queue.isSuccess &&
        queue.data.candidates.map((c) => (
          <Card key={c.id} className="flex flex-col gap-3.5">
            <div className="flex flex-wrap items-center gap-2">
              <Pill tone="neutral">{c.reason}</Pill>
              <Pill tone="neutral">
                confidence <span className="num">{c.confidence.toFixed(2)}</span>
              </Pill>
              <span className="text-label text-ink-3">
                queued {relativeTime(c.created_at)}
              </span>
            </div>

            <div className="grid gap-3 sm:grid-cols-2">
              <Side title={c.left_title} id={c.left_opportunity_id} label="A" />
              <Side title={c.right_title} id={c.right_opportunity_id} label="B" />
            </div>

            <div className="rounded-[10px] border border-line bg-raised/60 px-3 py-2.5">
              <p className="text-label font-semibold uppercase tracking-[0.06em] text-ink-3">
                Why it was withheld
              </p>
              <p className="mt-1 text-meta leading-relaxed text-ink-2">
                {c.withheld_because}
              </p>
            </div>

            <Input
              placeholder="Note for the audit log (optional)"
              value={notes[c.id] ?? ''}
              onChange={(e) => setNotes({ ...notes, [c.id]: e.target.value })}
              aria-label="Decision note"
            />

            <div className="flex flex-wrap items-center gap-2">
              <Button
                variant="primary"
                disabled={resolve.isPending}
                onClick={() => resolve.mutate({ id: c.id, resolution: 'merged' })}
              >
                Same role — merge
              </Button>
              <Button
                disabled={resolve.isPending}
                onClick={() => resolve.mutate({ id: c.id, resolution: 'rejected' })}
              >
                Different roles
              </Button>
              <p className="ml-auto text-label text-ink-3">
                Both decisions are written to the hash-chained audit log.
              </p>
            </div>
          </Card>
        ))}
    </div>
  );
}

function Side({ title, id, label }: { title: string; id: string; label: string }) {
  return (
    <div className="rounded-[10px] border border-line px-3 py-2.5">
      <p className="text-micro font-bold uppercase tracking-[0.08em] text-ink-3">
        Posting {label}
      </p>
      <Link
        to={`/app/browse/${id}`}
        className="mt-1 block text-body font-semibold leading-snug decoration-line-strong underline-offset-2 hover:underline"
      >
        {title}
      </Link>
      <p className="mt-1 truncate font-mono text-micro text-ink-3">{id}</p>
    </div>
  );
}
