import { useState } from 'react';
import { useQueryClient } from '@tanstack/react-query';
import { authApi } from '@/lib/api/auth';
import { ApiError } from '@/lib/api/client';
import { Field, Input } from '@/components/ui/Field';
import { Button } from '@/components/ui/Button';
import { ThemeToggle } from '@/features/shell/ThemeToggle';

/**
 * Sign in.
 *
 * Deliberately plain about what this is: an operations console over a corpus we
 * keep true, not a marketing page. The one piece of persuasion on the screen is
 * the honesty note, because the first thing a new operator needs to know is that
 * the bands will read"Not enough information" and that this is correct.
 */
export function LoginPage() {
  const qc = useQueryClient();
  const [mode, setMode] = useState<'login' | 'register'>('login');
  const [email, setEmail] = useState('');
  const [password, setPassword] = useState('');
  const [error, setError] = useState<string | null>(null);
  const [pending, setPending] = useState(false);

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    setError(null);
    setPending(true);
    try {
      if (mode === 'register') {
        await authApi.register(email, password);
      }
      await authApi.login(email, password);
      // Drop everything: the cache may hold another user's data, and a feed
      // rendered for the previous session is the worst possible first screen.
      await qc.resetQueries();
    } catch (err) {
      setError(
        err instanceof ApiError
          ? err.unauthorized
            ? 'That email and password do not match an account.'
            : readMessage(err)
          : 'Could not reach the API. Is it running?',
      );
    } finally {
      setPending(false);
    }
  }

  return (
    <div className="grid min-h-dvh place-items-center px-5 py-10">
      <div className="absolute right-5 top-5">
        <ThemeToggle />
      </div>

      <main className="w-full max-w-[380px] rise">
        <div className="mb-7 flex flex-col items-center gap-3 text-center">
          <Mark />
          <div>
            <h1 className="text-display font-bold tracking-[-0.024em]">DevSignal</h1>
            <p className="mt-1 text-body text-ink-3">
              Developer opportunity intelligence
            </p>
          </div>
        </div>

        <form
          onSubmit={submit}
          className="flex flex-col gap-4 rounded-[18px] border border-line bg-surface p-6 shadow-[var(--shadow-raise)]"
        >
          <div className="flex gap-0.5 rounded-[10px] border border-line bg-raised p-0.5">
            {(['login', 'register'] as const).map((m) => (
              <button
                key={m}
                type="button"
                aria-pressed={mode === m}
                onClick={() => {
                  setMode(m);
                  setError(null);
                }}
                className={
                  'flex-1 cursor-pointer rounded-[7px] px-3 py-1.5 text-meta font-medium ' +
                  'transition-all duration-[var(--dur-base)] ' +
                  (mode === m
                    ? 'bg-surface text-ink shadow-[var(--shadow-flat)]'
                    : 'text-ink-3 hover:text-ink-2')
                }
              >
                {m === 'login' ? 'Sign in' : 'Create account'}
              </button>
            ))}
          </div>

          <Field label="Email" htmlFor="email">
            <Input
              id="email"
              type="email"
              autoComplete="email"
              required
              value={email}
              onChange={(e) => setEmail(e.target.value)}
              placeholder="you@example.com"
            />
          </Field>

          <Field
            label="Password"
            htmlFor="password"
            hint={
              mode === 'register'
                ? 'At least 12 characters. The server enforces this, not the form.'
                : undefined
            }
          >
            <Input
              id="password"
              type="password"
              autoComplete={mode === 'login' ? 'current-password' : 'new-password'}
              required
              value={password}
              onChange={(e) => setPassword(e.target.value)}
            />
          </Field>

          {error && (
            <p
              role="alert"
              className="rounded-[10px] border border-bad/25 bg-bad-wash px-3 py-2 text-meta font-medium text-bad"
            >
              {error}
            </p>
          )}

          <Button
            type="submit"
            variant="primary"
            disabled={pending || !email || !password}
            className="h-9 w-full justify-center"
          >
            {pending ? 'Working…' : mode === 'login' ? 'Sign in' : 'Create account'}
          </Button>
        </form>

        <p className="mt-5 text-center text-meta leading-relaxed text-ink-3">
          Most fit bands currently read <b className="font-semibold">Not enough information</b>.
          That is correct, not broken — skill extraction covers only part of the corpus, so 45 of
          the model&apos;s 100 points cannot be scored yet.
        </p>
      </main>
    </div>
  );
}

function readMessage(err: ApiError): string {
  try {
    const parsed = JSON.parse(err.body) as { error?: string };
    if (parsed.error) return parsed.error;
  } catch {
    /* not JSON; fall through */
  }
  return 'Something went wrong. Try again.';
}

function Mark() {
  return (
    <div
      aria-hidden
      className="grid size-11 place-items-center rounded-[14px] border border-brand-edge bg-brand-wash"
    >
      <svg viewBox="0 0 24 24" fill="none" className="size-6 text-brand">
        <path
          d="M4 17.5 9 11l4 4 6.5-9"
          stroke="currentColor"
          strokeWidth="2.2"
          strokeLinecap="round"
          strokeLinejoin="round"
        />
        <circle cx="19.5" cy="6" r="2" fill="currentColor" />
      </svg>
    </div>
  );
}
