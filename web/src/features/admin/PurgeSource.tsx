import { useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { adminApi } from '@/lib/api/admin';
import { qk } from '@/lib/queryKeys';
import { Modal } from '@/components/ui/Modal';
import { Button } from '@/components/ui/Button';
import { Input } from '@/components/ui/Field';
import { useToast } from '@/components/ui/Toast';

/**
 * Purging a source's contribution.
 *
 * Counts first, then requires the operator to echo the number back. That is not
 * ceremony: the count is the server's confirmation token, so a stale plan cannot
 * authorise a larger delete than the operator actually saw — if ingestion added
 * fifty postings between reading the plan and confirming it, the purge is
 * rejected rather than quietly taking fifty more.
 *
 * The plan distinguishes the three populations, because only one of them is
 * destroyed: postings only this source saw are deleted, postings another source
 * also saw survive with one less provenance row, and merged ones are handled
 * through their canonical.
 */
export function PurgeSource({
  sourceID,
  sourceName,
}: {
  sourceID: string;
  sourceName: string;
}) {
  const qc = useQueryClient();
  const toast = useToast();
  const [open, setOpen] = useState(false);
  const [typed, setTyped] = useState('');
  const [note, setNote] = useState('');

  const plan = useQuery({
    queryKey: ['purge-plan', sourceID],
    queryFn: () => adminApi.purgePlan(sourceID),
    enabled: open,
    // Never cached: a plan is a snapshot, and acting on a stale one is the
    // failure the confirmation number exists to prevent.
    staleTime: 0,
    gcTime: 0,
  });

  const purge = useMutation({
    mutationFn: () =>
      adminApi.purgeSource(sourceID, plan.data!.will_be_deleted, note || 'operator purge'),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: qk.sources() });
      void qc.invalidateQueries({ queryKey: ['opportunities'] });
      setOpen(false);
      setTyped('');
      toast(`Purged ${sourceName}`);
    },
    onError: () =>
      toast('Purge refused. The count changed — reopen to read a fresh plan.', 'bad'),
  });

  const expected = plan.data?.will_be_deleted;
  const matches = expected !== undefined && typed.trim() === String(expected);

  return (
    <>
      <Button variant="danger" onClick={() => setOpen(true)}>
        Purge
      </Button>

      <Modal
        open={open}
        onClose={() => setOpen(false)}
        title={`Purge ${sourceName}`}
        description="Deletes this source's contribution to the corpus. Irreversible."
        footer={
          <>
            <Button onClick={() => setOpen(false)}>Cancel</Button>
            <Button
              variant="danger"
              disabled={!matches || purge.isPending}
              onClick={() => purge.mutate()}
              className="border-bad/40 bg-bad-wash text-bad"
            >
              {purge.isPending ? 'Purging…' : `Delete ${expected ?? '…'} postings`}
            </Button>
          </>
        }
      >
        {plan.isPending && <p className="text-body text-ink-3">Counting…</p>}
        {plan.isError && (
          <p className="text-body text-bad">Could not read the purge plan.</p>
        )}

        {plan.isSuccess && (
          <div className="flex flex-col gap-4">
            <dl className="grid grid-cols-2 gap-2.5">
              <Stat label="Attributed to this source" value={plan.data.total_attributed} />
              <Stat label="Will be deleted" value={plan.data.will_be_deleted} danger />
              <Stat label="Also seen elsewhere — kept" value={plan.data.also_seen_elsewhere} />
              <Stat label="Merged away" value={plan.data.merged} />
            </dl>

            <p className="text-meta leading-relaxed text-ink-2">
              Only postings <b className="font-semibold">no other source saw</b> are deleted. The
              delete is scoped to the ids this source contributed — never a table-wide orphan
              sweep, which would remove unrelated postings as a side effect.
            </p>

            <div className="flex flex-col gap-1.5">
              <label htmlFor="purge-confirm" className="text-body font-medium">
                Type <code className="rounded bg-raised px-1.5 py-0.5 font-mono text-meta">{plan.data.will_be_deleted}</code> to confirm
              </label>
              <Input
                id="purge-confirm"
                value={typed}
                onChange={(e) => setTyped(e.target.value)}
                autoComplete="off"
                inputMode="numeric"
              />
              <p className="text-micro text-ink-3">
                The server checks this number too, so a plan that went stale cannot authorise a
                bigger delete than the one shown here.
              </p>
            </div>

            <Input
              value={note}
              onChange={(e) => setNote(e.target.value)}
              placeholder="Note for the audit log"
              aria-label="Audit note"
            />
          </div>
        )}
      </Modal>
    </>
  );
}

function Stat({
  label,
  value,
  danger,
}: {
  label: string;
  value: number;
  danger?: boolean;
}) {
  return (
    <div className="rounded-[var(--radius-md)] border border-line bg-raised/50 px-3 py-2">
      <dt className="text-micro font-semibold uppercase tracking-[0.06em] text-ink-3">
        {label}
      </dt>
      <dd className={`num mt-0.5 text-lead font-semibold ${danger ? 'text-bad' : ''}`}>
        {value.toLocaleString()}
      </dd>
    </div>
  );
}
