import { useState } from 'react';
import { useMutation, useQuery } from '@tanstack/react-query';
import { listingsApi } from '@/lib/api/listings';
import { Modal } from '@/components/ui/Modal';
import { Button } from '@/components/ui/Button';
import { Field, Input, Select } from '@/components/ui/Field';
import { useToast } from '@/components/ui/Toast';

/**
 * Reporting a listing.
 *
 * A user action, not an operator one: the person who spots a scam posting is the
 * person reading it. The reason set comes from the server rather than being
 * hardcoded, because it is a closed vocabulary the queue is triaged by.
 *
 * The copy is deliberate about what a report does and does not do. It does NOT
 * remove the posting — an operator reviews it — and saying so prevents the
 * report being read as a delete button that appears not to work.
 */
export function ReportListing({
  opportunityID,
  title,
  onDone,
}: {
  opportunityID: string;
  title: string;
  onDone?: () => void;
}) {
  const [open, setOpen] = useState(false);
  const [reason, setReason] = useState('');
  const [detail, setDetail] = useState('');
  const toast = useToast();

  const reasons = useQuery({
    queryKey: ['listing-flag-reasons'],
    queryFn: () => listingsApi.reasons(),
    staleTime: Infinity,
    enabled: open,
  });

  const submit = useMutation({
    mutationFn: () =>
      listingsApi.flag(opportunityID, { reason, detail: detail.trim() || undefined }),
    onSuccess: () => {
      setOpen(false);
      setReason('');
      setDetail('');
      toast('Reported. An operator will review it.');
      onDone?.();
    },
    onError: () => toast('Could not send the report', 'bad'),
  });

  return (
    <>
      <Button variant="ghost" onClick={() => setOpen(true)}>
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2"
          strokeLinecap="round" strokeLinejoin="round" aria-hidden className="size-3.5">
          <path d="M5 21V4h9l-1 3 1 3H5" />
        </svg>
        Report
      </Button>

      <Modal
        open={open}
        onClose={() => setOpen(false)}
        title="Report this listing"
        description={
          <>
            Reporting does not remove the posting — an operator reviews it, and upholding a report
            marks the listing rather than deleting it. Your report is kept even if you later close
            your account, because it is about the posting.
          </>
        }
        footer={
          <>
            <Button onClick={() => setOpen(false)}>Cancel</Button>
            <Button
              variant="primary"
              disabled={!reason || submit.isPending}
              onClick={() => submit.mutate()}
            >
              {submit.isPending ? 'Sending…' : 'Send report'}
            </Button>
          </>
        }
      >
        <div className="flex flex-col gap-4">
          <p className="truncate rounded-[var(--radius-md)] border border-line bg-raised px-3 py-2 text-meta font-medium">
            {title}
          </p>

          <Field label="What is wrong with it?" htmlFor="flag-reason">
            <Select
              id="flag-reason"
              value={reason}
              onChange={(e) => setReason(e.target.value)}
              disabled={reasons.isPending}
            >
              <option value="">Choose a reason…</option>
              {reasons.data?.reasons.map((r) => (
                <option key={r.value} value={r.value}>
                  {r.label}
                </option>
              ))}
            </Select>
          </Field>

          <Field
            label="Anything else"
            htmlFor="flag-detail"
            hint="Optional. What an operator would need to verify it."
          >
            <Input
              id="flag-detail"
              value={detail}
              onChange={(e) => setDetail(e.target.value)}
              placeholder="The salary in the posting contradicts the description"
            />
          </Field>
        </div>
      </Modal>
    </>
  );
}
