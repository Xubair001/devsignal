import { useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { adminApi } from '@/lib/api/admin';
import { Card } from '@/components/ui/Card';
import { Pill } from '@/components/ui/Pill';
import { Button } from '@/components/ui/Button';
import { Input } from '@/components/ui/Field';
import { useToast } from '@/components/ui/Toast';
import { relativeTime } from '@/lib/format';

/**
 * Where a posting came from, and what was merged into it.
 *
 * Every source row is kept forever — dedup moves them rather than deleting them,
 * which is what makes a merge reversible. This panel is where that becomes
 * usable: an operator can see the rows, see which arrived by merge, and reverse
 * one.
 *
 * Un-merging is not a flag flip. It restores the exact rows the merge moved,
 * clears merged_into, and stamps unmerged_at — after which dedup skips that pair
 * permanently, because a person judged them different and a simhash does not
 * overrule that. Without the permanence the operator would watch their un-merge
 * undo itself on the next sweep.
 */
export function ProvenancePanel({ opportunityID }: { opportunityID: string }) {
  const qc = useQueryClient();
  const toast = useToast();
  const [note, setNote] = useState('');

  const prov = useQuery({
    queryKey: ['provenance', opportunityID],
    queryFn: () => adminApi.provenance(opportunityID),
  });

  const unmerge = useMutation({
    mutationFn: () => adminApi.unmerge(opportunityID, note),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ['provenance', opportunityID] });
      void qc.invalidateQueries({ queryKey: ['opportunities'] });
      setNote('');
      toast('Un-merged. Dedup will not propose this pair again.');
    },
    onError: () => toast('Could not un-merge', 'bad'),
  });

  const requeue = useMutation({
    mutationFn: () => adminApi.requeueOpportunity(opportunityID, note || 'operator requeue'),
    onSuccess: () => toast('Requeued. It will be re-processed from the pipeline.'),
    onError: () => toast('Could not requeue', 'bad'),
  });

  if (prov.isPending || prov.isError) return null;
  const { sources, merged_in: mergedIn } = prov.data;

  return (
    <Card className="flex flex-col gap-4">
      <header>
        <h2 className="text-lead font-semibold">Provenance</h2>
        <p className="mt-1 text-meta leading-relaxed text-ink-3">
          Every source row this posting has ever had. Dedup moves rows rather than deleting them,
          which is what makes a merge reversible.
        </p>
      </header>

      <ul className="flex flex-col gap-2">
        {sources.map((s) => (
          <li
            key={s.id}
            className="flex flex-wrap items-start gap-x-3 gap-y-1.5 rounded-[var(--radius-md)] border border-line bg-raised/50 px-3 py-2.5"
          >
            <div className="min-w-0 flex-1">
              <p className="text-body font-medium">{s.source_name}</p>
              <p className="mt-0.5 truncate font-mono text-micro text-ink-3">
                {s.ats_type ?? '—'} · {s.ats_job_id ?? 'no ats id'}
              </p>
            </div>
            <div className="flex flex-wrap items-center gap-1.5">
              {s.merge_reason ? (
                <Pill tone="brand">
                  merged: {s.merge_reason}
                  {s.merge_confidence != null && ` (${s.merge_confidence.toFixed(2)})`}
                </Pill>
              ) : (
                <Pill tone="neutral">direct ingest</Pill>
              )}
              {s.last_seen_at && (
                <span className="text-micro text-ink-3">
                  last seen {relativeTime(s.last_seen_at)}
                </span>
              )}
            </div>
          </li>
        ))}
      </ul>

      {mergedIn.length > 0 && (
        <div className="rounded-[var(--radius-md)] border border-warn/25 bg-warn-wash px-3.5 py-3">
          <p className="text-body font-semibold text-warn">
            {mergedIn.length} posting{mergedIn.length === 1 ? '' : 's'} merged into this one
          </p>
          <ul className="mt-2 flex flex-col gap-1">
            {mergedIn.map((m) => (
              <li key={m.id} className="text-meta text-ink-2">
                {m.title}{' '}
                <span className="text-ink-3">
                  ({m.source_rows} source row{m.source_rows === 1 ? '' : 's'})
                </span>
              </li>
            ))}
          </ul>
          <p className="mt-2.5 text-meta leading-relaxed text-ink-2">
            A false merge hides a real job and is otherwise invisible. Un-merging restores those
            rows exactly and stops dedup proposing the pair again.
          </p>
        </div>
      )}

      <div className="flex flex-wrap items-center gap-2">
        <Input
          value={note}
          onChange={(e) => setNote(e.target.value)}
          placeholder="Note for the audit log"
          aria-label="Audit note"
          className="max-w-[280px]"
        />
        {mergedIn.length > 0 && (
          <Button
            variant="danger"
            disabled={unmerge.isPending}
            onClick={() => unmerge.mutate()}
          >
            {unmerge.isPending ? 'Un-merging…' : 'Un-merge'}
          </Button>
        )}
        <Button disabled={requeue.isPending} onClick={() => requeue.mutate()}>
          Requeue through the pipeline
        </Button>
        <p className="ml-auto text-micro text-ink-3">
          Both actions land in the hash-chained audit log.
        </p>
      </div>
    </Card>
  );
}
