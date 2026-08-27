import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { opportunitiesApi } from '@/lib/api/opportunities';
import { feedApi } from '@/lib/api/feed';
import { qk } from '@/lib/queryKeys';
import { Card } from '@/components/ui/Card';
import { Pill } from '@/components/ui/Pill';
import { Button } from '@/components/ui/Button';
import { EmptyState, ErrorState, SkeletonCards } from '@/components/ui/States';
import { PostingRow } from '@/components/ui/PostingRow';
import { PageHeader } from '@/components/ui/PageHeader';
import { useToast } from '@/components/ui/Toast';
import { relativeTime } from '@/lib/format';

/**
 * Saved postings.
 *
 * A save is a decision revisited days later, which is exactly when liveness
 * matters most: the interesting question here is not"what did I save" but
 *"which of these is still open". So each row carries its verified-open state
 * and its check recency, and postings that closed since being saved are counted
 * rather than silently missing.
 */
export function SavedPage() {
  const qc = useQueryClient();
  const toast = useToast();

  const saved = useQuery({ queryKey: qk.saved(), queryFn: () => opportunitiesApi.saved() });

  const unsave = useMutation({
    mutationFn: (id: string) => feedApi.unsave(id),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: qk.saved() });
      void qc.invalidateQueries({ queryKey: ['feed'] });
      // Un-saving APPENDS an event rather than deleting one: the engagement log
      // is the behavioural evaluation set, and a save the user took back is a
      // different label from one they kept.
      toast('Removed from saved');
    },
  });

  const apply = useMutation({
    mutationFn: (id: string) => feedApi.apply(id),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: qk.saved() });
      toast('Recorded that you applied');
    },
  });

  return (
    <div className="flex flex-col gap-4 sm:gap-5">
      <PageHeader
        title="Saved"
        subtitle="Roles you kept. Liveness is shown on every row because a save is revisited days later, which is when it matters most."
        aside={
          saved.isSuccess ? (
            <Pill tone="neutral">
              <span className="num">{saved.data.items.length}</span> saved
            </Pill>
          ) : undefined
        }
      />

      {saved.isSuccess && saved.data.closed_since_saved > 0 && (
        <div className="flex items-start gap-2.5 rounded-[12px] border border-warn/25 bg-warn-wash px-3.5 py-3">
          <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2"
            strokeLinecap="round" aria-hidden className="mt-px size-4 shrink-0 text-warn">
            <path d="M12 9v4M12 17h.01" />
            <circle cx="12" cy="12" r="9" />
          </svg>
          <p className="text-meta leading-relaxed">
            <b className="font-semibold">
              {saved.data.closed_since_saved} saved role
              {saved.data.closed_since_saved === 1 ? '' : 's'} no longer listed.
            </b>{' '}
            Closed by the employer, merged into another posting, or purged with its source. We
            never infer a closure from a failed fetch, so this reflects a poll in which the
            posting was genuinely absent.
          </p>
        </div>
      )}

      {saved.isPending && <SkeletonCards count={1} height="h-[300px]" />}
      {saved.isError && <ErrorState error={saved.error} onRetry={() => void saved.refetch()} />}

      {saved.isSuccess && saved.data.items.length === 0 && (
        <EmptyState title="Nothing saved yet">
          Saving from the feed keeps a role here with its liveness state, so you can come back to
          it and still know whether it is open.
        </EmptyState>
      )}

      {saved.isSuccess && saved.data.items.length > 0 && (
        <Card pad="none" className="overflow-hidden">
          <ul>
            {saved.data.items.map((s) => (
              <PostingRow
                key={s.opportunity_id}
                p={s.posting}
                right={
                  <>
                    <span className="hidden text-label text-ink-3 sm:block">
                      saved {relativeTime(s.saved_at)}
                    </span>
                    <Button onClick={() => apply.mutate(s.opportunity_id)}>I applied</Button>
                    <Button
                      variant="ghost"
                      onClick={() => unsave.mutate(s.opportunity_id)}
                      aria-label="Remove from saved"
                    >
                      Remove
                    </Button>
                  </>
                }
              />
            ))}
          </ul>
        </Card>
      )}
    </div>
  );
}
