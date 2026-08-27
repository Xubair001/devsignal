import { useState } from 'react';
import { Link, useNavigate, useLocation } from 'react-router-dom';
import { useQueryClient } from '@tanstack/react-query';
import { authApi } from '@/lib/api/auth';
import { ApiError } from '@/lib/api/client';
import { Field, Input } from '@/components/ui/Field';
import { Button } from '@/components/ui/Button';
import { ThemeToggle } from '@/features/shell/ThemeToggle';
import { Mark } from '@/components/ui/Mark';

type Mode = 'login' | 'register';

/**
 * Sign in and sign up, as two routes over one form.
 *
 * Two routes rather than a tab, because they are two different intentions and a
 * person arriving at a link should land on the one they were sent to — and
 * because a browser password manager keys on the URL. The form is shared so the
 * two cannot drift apart in validation or error handling.
 */
export function AuthPage({ mode }: { mode: Mode }) {
  const qc = useQueryClient();
  const nav = useNavigate();
  const { state } = useLocation() as { state?: { from?: string } };

  const [email, setEmail] = useState('');
  const [password, setPassword] = useState('');
  const [error, setError] = useState<string | null>(null);
  const [pending, setPending] = useState(false);

  const isRegister = mode === 'register';

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    setError(null);
    setPending(true);
    try {
      if (isRegister) await authApi.register(email, password);
      await authApi.login(email, password);
      // Reset, not invalidate: the cache may hold the previous user's data, and
      // a feed rendered for someone else is the worst possible first screen.
      await qc.resetQueries();
      nav(state?.from ?? '/app', { replace: true });
    } catch (err) {
      setError(describe(err, isRegister));
    } finally {
      setPending(false);
    }
  }

  return (
    <div className="relative grid min-h-dvh place-items-center px-5 py-12">
      <div className="absolute right-4 top-4 sm:right-6 sm:top-6">
        <ThemeToggle />
      </div>

      <main className="w-full max-w-[400px] rise">
        <Link to="/" className="mb-8 flex flex-col items-center gap-3 text-center">
          <Mark size={44} />
          <span className="text-display font-bold tracking-[-0.024em]">DevSignal</span>
        </Link>

        <form
          onSubmit={submit}
          className="flex flex-col gap-5 rounded-[18px] border border-line bg-surface p-6 shadow-[var(--shadow-raise)] sm:p-7"
        >
          <div>
            <h1 className="text-title font-bold tracking-[-0.02em]">
              {isRegister ? 'Create your account' : 'Sign in'}
            </h1>
            <p className="mt-1 text-body text-ink-3">
              {isRegister
                ? 'Then tell us what you are looking for, and the feed follows.'
                : 'Welcome back.'}
            </p>
          </div>

          <Field label="Email" htmlFor="email">
            <Input
              id="email"
              type="email"
              autoComplete="email"
              autoFocus
              required
              value={email}
              onChange={(e) => setEmail(e.target.value)}
              placeholder="you@example.com"
            />
          </Field>

          <Field
            label="Password"
            htmlFor="password"
            hint={isRegister ? 'At least 12 characters. The server enforces it, not the form.' : undefined}
          >
            <Input
              id="password"
              type="password"
              autoComplete={isRegister ? 'new-password' : 'current-password'}
              required
              minLength={isRegister ? 12 : undefined}
              value={password}
              onChange={(e) => setPassword(e.target.value)}
            />
          </Field>

          {error && (
            <p
              role="alert"
              className="rounded-[10px] border border-bad/25 bg-bad-wash px-3 py-2.5 text-meta font-medium leading-relaxed text-bad"
            >
              {error}
            </p>
          )}

          <Button
            type="submit"
            variant="primary"
            disabled={pending || !email || !password}
            className="h-10 w-full justify-center text-body"
          >
            {pending ? 'Working…' : isRegister ? 'Create account' : 'Sign in'}
          </Button>

          <p className="text-center text-meta text-ink-3">
            {isRegister ? 'Already have an account? ' : 'No account yet? '}
            <Link
              to={isRegister ? '/login' : '/register'}
              className="font-medium text-brand underline decoration-brand/35 underline-offset-2 hover:decoration-brand"
            >
              {isRegister ? 'Sign in' : 'Create one'}
            </Link>
          </p>
        </form>

        <p className="mt-6 text-center text-meta leading-relaxed text-ink-3">
          Most fit bands read <b className="font-semibold text-ink-2">Stretch</b> or{' '}
          <b className="font-semibold text-ink-2">Not enough information</b> on a new profile.
          That is the model being honest about what it can observe, not a fault.
        </p>
      </main>
    </div>
  );
}

function describe(err: unknown, isRegister: boolean): string {
  if (!(err instanceof ApiError)) {
    return 'Could not reach the API. Check that it is running.';
  }
  if (err.unauthorized) {
    return 'That email and password do not match an account.';
  }
  if (err.conflict) {
    return isRegister
      ? 'An account with that email already exists. Sign in instead.'
      : 'That request conflicted with the current state.';
  }
  try {
    const parsed = JSON.parse(err.body) as { error?: string };
    if (parsed.error) return parsed.error;
  } catch {
    /* not JSON */
  }
  return `Something went wrong (${err.status}). Try again.`;
}
