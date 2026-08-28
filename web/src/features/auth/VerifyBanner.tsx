import { useMutation } from '@tanstack/react-query';
import { authApi } from '@/lib/api/auth';
import { Button } from '@/components/ui/Button';
import { useToast } from '@/components/ui/Toast';
import { useSession } from './useSession';

/**
 * An unverified address, surfaced where it matters.
 *
 * The digest never mails an unverified address — that is how a sending domain's
 * reputation is lost, and it may not even be the user's address — so an
 * unverified account can configure the digest perfectly and receive nothing.
 * Without this banner that is a silent dead end; with it, it is a fixable prompt.
 *
 * Deliberately not a modal and not blocking: everything else in the console
 * works fine unverified. Interrupting the whole app for something that affects
 * one feature would be the wrong trade.
 */
export function VerifyBanner() {
  const { signedIn, emailVerified } = useSession();
  const toast = useToast();

  const resend = useMutation({
    mutationFn: () => authApi.resendVerification(),
    onSuccess: () => toast('Sent. Check your inbox — the link works once.'),
    onError: () => toast('Could not send the email just now', 'bad'),
  });

  if (!signedIn || emailVerified) return null;

  return (
    <div className="mb-4 flex flex-wrap items-center gap-x-3 gap-y-2 rounded-[var(--radius-lg)] border border-warn/25 bg-warn-wash px-4 py-3">
      <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2"
        strokeLinecap="round" aria-hidden className="size-4 shrink-0 text-warn">
        <path d="m3 7 9 6 9-6" />
        <rect x="3" y="5" width="18" height="14" rx="2" />
      </svg>
      <p className="min-w-0 flex-1 text-meta leading-relaxed">
        <b className="font-semibold">Your email address is not confirmed.</b> Everything here
        works, but the daily digest will not send to an unverified address — so you can set it up
        and still receive nothing.
      </p>
      <Button onClick={() => resend.mutate()} disabled={resend.isPending}>
        {resend.isPending ? 'Sending…' : 'Resend the link'}
      </Button>
    </div>
  );
}
