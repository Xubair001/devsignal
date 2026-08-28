import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { adminApi } from '@/lib/api/admin';
import { qk } from '@/lib/queryKeys';
import { relativeTime } from '@/lib/format';
import { Card } from '@/components/ui/Card';
import { Pill } from '@/components/ui/Pill';
import { Button } from '@/components/ui/Button';
import { useToast } from '@/components/ui/Toast';
import { EmptyState, ErrorState, SectionHead, SkeletonCards } from '@/components/ui/States';

const REASON_LABEL: Record<string, string> = {
  scam_or_fraud: 'Scam or fraud',
  not_a_real_job: 'Not a real job',
  misleading_pay: 'Misleading pay information',
  discriminatory: 'Discriminatory content',
  expired: 'No longer open',
  duplicate: 'Duplicate listing',
  other: 'Something else',
};

export function FlagsPage() {
  const qc = useQueryClient();
  const toast = useToast();

  const flags = useQuery({ queryKey: qk.flags('open'), queryFn: () => adminApi.flags('open') });

  const resolve = useMutation({
    mutationFn: (v: { id: string; status: 'upheld' | 'rejected' }) =>
      adminApi.resolveFlag(v.id, v.status),
    onSuccess: (_d, v) => {
      /* Upholding a flag deliberately does NOT close the posting: closure has
         exactly one cause, and the copy is explicit so nobody expects otherwise. */
      toast(
        v.status === 'upheld'
          ? 'Upheld — recorded. The posting stays open until a poll says otherwise.'
          : 'Rejected — the reporter is not notified.',
      );
      void qc.invalidateQueries({ queryKey: qk.flags() });
    },
    onError: () => toast('Could not resolve that report', 'bad'),
  });

  return (
    <>
      <SectionHead
        title="Reported listings"
        hint="Oldest first — a report that has sat a day is more urgent than one raised a minute ago."
      />

      <div className="grid grid-cols-1 gap-3 md:grid-cols-2 xl:grid-cols-3">
        {flags.isPending && <SkeletonCards count={3} height="h-[180px]" />}

        {flags.isError && <ErrorState error={flags.error} onRetry={() => void flags.refetch()} />}

        {flags.isSuccess && flags.data.flags.length === 0 && (
          <EmptyState
            title="The review queue is clear"
            icon={
              <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round" className="size-8">
                <path d="M20 6 9 17l-5-5" />
              </svg>
            }
          >
            Nothing has been reported. Users can flag a listing from its detail page, and scam reports
            land here first.
          </EmptyState>
        )}

        {flags.data?.flags.map((f) => (
          <Card key={f.id} as="article" lift pad="tight" className="flex flex-col gap-2.5">
            <div className="flex items-start justify-between gap-2.5">
              <div className="min-w-0">
                <p className="text-body font-semibold">{REASON_LABEL[f.reason] ?? f.reason}</p>
                <p className="truncate text-meta text-ink-3">
                  {f.title}
                  {f.company_name && ` · ${f.company_name}`}
                </p>
              </div>
              {/* A pile-up on one listing is a different signal from reports
                  spread across many, so the count is on the card. */}
              <Pill tone={f.flags_on_posting > 1 ? 'breached' : 'at_risk'}>
                {f.flags_on_posting} report{f.flags_on_posting > 1 ? 's' : ''}
              </Pill>
            </div>

            {f.detail && (
              <blockquote className="rounded-r-md border-l-2 border-line-strong bg-raised px-3 py-2.5 text-meta italic text-ink-2">
                {f.detail}
              </blockquote>
            )}

            <div className="mt-auto flex flex-wrap items-center gap-1.5 pt-1">
              <span className="font-mono text-label text-ink-3">{relativeTime(f.created_at)}</span>
              {f.posting_closed && <Pill tone="neutral">Posting already closed</Pill>}
              <span className="flex-1" />
              <Button
                disabled={resolve.isPending}
                onClick={() => resolve.mutate({ id: f.id, status: 'upheld' })}
              >
                Uphold
              </Button>
              <Button
                variant="ghost"
                disabled={resolve.isPending}
                onClick={() => resolve.mutate({ id: f.id, status: 'rejected' })}
              >
                Reject
              </Button>
            </div>
          </Card>
        ))}
      </div>

      <p className="mt-4 max-w-[70ch] text-meta leading-relaxed text-ink-3">
        <b className="font-semibold text-ink-2">Upholding a report does not close the posting.</b>{' '}
        Closure has exactly one cause — a successful poll in which the posting was absent — and a
        second path to it would make the liveness guarantee unverifiable. To act on a bad source,
        quarantine or purge it from{' '}
        <a href="/sources" className="text-brand-ink underline decoration-line-strong underline-offset-2">
          Sources
        </a>
        .
      </p>
    </>
  );
}
