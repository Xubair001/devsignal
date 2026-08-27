import { useEffect, useState } from 'react';
import { Link, useSearchParams } from 'react-router-dom';
import { useQueryClient } from '@tanstack/react-query';
import { authApi } from '@/lib/api/auth';
import { ApiError } from '@/lib/api/client';
import { Button } from '@/components/ui/Button';
import { Mark } from '@/components/ui/Mark';
import { ThemeToggle } from '@/features/shell/ThemeToggle';
import { cn } from '@/components/ui/cn';

type State = 'working' | 'done' | 'invalid' | 'missing' | 'error';

/**
 * The destination of a verification link.
 *
 * Public, because the link is followed in whatever browser opened the email and
 * that browser may hold no session. The token is the authorization.
 *
 * Consumed exactly once on mount, guarded against React's double-invoke in
 * development — a second call would spend a token that had already worked and
 * show the user a failure for something that succeeded.
 */
export function VerifyPage() {
  const [params] = useSearchParams();
  const qc = useQueryClient();
  const token = params.get('token') ?? '';
  const [state, setState] = useState<State>(token ? 'working' : 'missing');

  useEffect(() => {
    if (!token) return;
    let cancelled = false;

    void (async () => {
      try {
        await authApi.verifyEmail(token);
        if (cancelled) return;
        // The session's email_verified changes, so drop it.
        await qc.invalidateQueries({ queryKey: ['session'] });
        setState('done');
      } catch (err) {
        if (cancelled) return;
        setState(err instanceof ApiError && err.status === 400 ? 'invalid' : 'error');
      }
    })();

    return () => {
      cancelled = true;
    };
  }, [token, qc]);

  return (
    <div className="relative grid min-h-dvh place-items-center px-5 py-12">
      <div className="absolute right-4 top-4 sm:right-6 sm:top-6">
        <ThemeToggle />
      </div>

      <main className="w-full max-w-[420px] text-center rise">
        <Link to="/" className="mb-7 inline-flex min-h-[36px] items-center gap-2.5">
          <Mark size={32} />
          <span className="text-display font-bold tracking-[-0.024em]">DevSignal</span>
        </Link>

        <div className="rounded-[var(--radius-xl)] border border-line bg-surface p-7 shadow-[var(--shadow-raise)]">
          <Glyph state={state} />
          <h1 className="mt-4 text-title font-bold tracking-[-0.02em]">{TITLE[state]}</h1>
          <p className="mx-auto mt-2 max-w-[42ch] text-body leading-relaxed text-ink-2">
            {BODY[state]}
          </p>

          <div className="mt-6 flex flex-wrap justify-center gap-2">
            {state === 'done' && (
              <Button as="a" variant="primary" href="/app/profile" className="h-10">
                Set up your profile
              </Button>
            )}
            {(state === 'invalid' || state === 'error' || state === 'missing') && (
              <>
                <Button as="a" variant="primary" href="/app/settings" className="h-10">
                  Send a new link
                </Button>
                <Button as="a" href="/login" className="h-10">
                  Sign in
                </Button>
              </>
            )}
          </div>
        </div>
      </main>
    </div>
  );
}

const TITLE: Record<State, string> = {
  working: 'Confirming your address…',
  done: 'Address confirmed',
  invalid: 'This link no longer works',
  missing: 'No token in this link',
  error: 'Something went wrong',
};

/* The invalid case deliberately does not say WHICH — expired, already used, or
   never real. Distinguishing them tells a caller whether an address is
   registered, and the API answers the same way for all three. */
const BODY: Record<State, string> = {
  working: 'One moment.',
  done:
    'Thanks — that is the address the daily digest will go to. Nothing is sent ' +
    'until you opt in, and you can withdraw that at any time.',
  invalid:
    'A verification link works once and expires after 48 hours. Sign in and ' +
    'request a new one; it takes a second.',
  missing: 'The address in your browser has no token on it. Check the link in your email.',
  error: 'We could not confirm the address just now. Try the link again in a moment.',
};

function Glyph({ state }: { state: State }) {
  const tone =
    state === 'done'
      ? 'border-good/30 bg-good-wash text-good'
      : state === 'working'
        ? 'border-brand-edge bg-brand-wash text-brand'
        : 'border-warn/30 bg-warn-wash text-warn';

  return (
    <span
      aria-hidden
      className={cn('mx-auto grid size-12 place-items-center rounded-[16px] border', tone)}
    >
      {state === 'done' ? (
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.4"
          strokeLinecap="round" strokeLinejoin="round" className="size-6">
          <path d="m4 12.5 5 5L20 6.5" />
        </svg>
      ) : state === 'working' ? (
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.2"
          strokeLinecap="round" className="size-6 animate-spin">
          <path d="M12 3a9 9 0 1 0 9 9" />
        </svg>
      ) : (
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.2"
          strokeLinecap="round" className="size-6">
          <path d="M12 8v5M12 16.5h.01" />
          <circle cx="12" cy="12" r="9" />
        </svg>
      )}
    </span>
  );
}
