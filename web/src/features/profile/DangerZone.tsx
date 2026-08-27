import { useState } from 'react';
import { useMutation } from '@tanstack/react-query';
import { profileApi } from '@/lib/api/profile';
import { clearToken } from '@/lib/api/client';
import { Card } from '@/components/ui/Card';
import { Button } from '@/components/ui/Button';
import { Input } from '@/components/ui/Field';

const CONFIRM = 'delete my account';

/**
 * Account erasure.
 *
 * Typed confirmation rather than a second "are you sure" dialog, and the same
 * pattern the source purge uses: a destructive action should require the
 * operator to demonstrate they read the sentence, not just that they can click
 * twice. This one is genuinely irreversible — the server inventories every
 * derived artifact, deletes each, and then COUNTS what remains rather than
 * trusting the deletes.
 */
export function DangerZone() {
  const [open, setOpen] = useState(false);
  const [typed, setTyped] = useState('');

  const erase = useMutation({
    mutationFn: () => profileApi.eraseAccount(),
    onSuccess: () => {
      // The account is gone. Holding the token would leave the app retrying a
      // session that no longer exists.
      clearToken();
      window.location.reload();
    },
  });

  return (
    <Card className="border-bad/25">
      <h2 className="text-[15px] font-semibold text-bad">Delete account</h2>
      <p className="mt-1.5 max-w-[70ch] text-[12.5px] leading-relaxed text-ink-2">
        Removes your profile, skills, resumes and their extracted text, your saved and dismissed
        postings, your fit scores, your profile vector and your notification settings. The server
        deletes each location, then counts what is left rather than assuming the deletes worked.
      </p>
      <p className="mt-2 max-w-[70ch] text-[12px] leading-relaxed text-ink-3">
        Listing reports you filed are anonymised rather than deleted: a scam report is about the
        posting, and it must not disappear because its author closed their account.
      </p>

      {!open ? (
        <Button variant="danger" className="mt-4" onClick={() => setOpen(true)}>
          Delete my account
        </Button>
      ) : (
        <div className="mt-4 flex flex-col gap-3 rounded-[10px] border border-bad/25 bg-bad-wash p-3.5">
          <label htmlFor="confirm" className="text-[12.5px] font-medium">
            Type <code className="rounded bg-surface px-1.5 py-0.5 font-mono text-[12px]">{CONFIRM}</code> to confirm
          </label>
          <Input
            id="confirm"
            value={typed}
            onChange={(e) => setTyped(e.target.value)}
            autoComplete="off"
          />
          <div className="flex gap-2">
            <Button
              variant="danger"
              disabled={typed !== CONFIRM || erase.isPending}
              onClick={() => erase.mutate()}
              className="border-bad/40 bg-bad-wash text-bad"
            >
              {erase.isPending ? 'Deleting…' : 'Delete permanently'}
            </Button>
            <Button
              onClick={() => {
                setOpen(false);
                setTyped('');
              }}
            >
              Cancel
            </Button>
          </div>
          {erase.isError && (
            <p role="alert" className="text-[12px] font-medium text-bad">
              The erasure did not complete. Nothing has been half-deleted — the request is tracked
              server-side and stays visibly incomplete until every location reports success.
            </p>
          )}
        </div>
      )}
    </Card>
  );
}
