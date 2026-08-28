import { Navigate, Outlet, useLocation } from 'react-router-dom';
import { useSession } from './useSession';

/**
 * The authentication gate for the console.
 *
 * Redirects rather than rendering a login form in place, and carries the
 * attempted path in location state so signing in returns you where you were
 * going. A deep link that silently dumps you on the overview is a small thing
 * that makes shared links useless.
 */
export function RequireAuth() {
  const { loading, signedIn } = useSession();
  const location = useLocation();

  if (loading) {
    return (
      <div className="grid min-h-dvh place-items-center">
        <p className="text-body text-ink-3">Checking your session…</p>
      </div>
    );
  }
  if (!signedIn) {
    return (
      <Navigate to="/login" replace state={{ from: location.pathname + location.search }} />
    );
  }
  return <Outlet />;
}

/**
 * The authorization gate for the operations surface.
 *
 * Client-side and therefore NOT a security boundary — the server gates
 * /internal/admin independently and answers 404 to a non-admin. This exists so a
 * non-admin never sees a link to a page that would only show them an error, and
 * so a bookmarked admin URL degrades into a clear message instead of four failed
 * queries.
 *
 * It answers with the same"does not exist" language the API uses, deliberately:
 * telling someone a page exists but is forbidden is information they did not
 * have, and the two surfaces should not disagree about what they reveal.
 */
export function RequireAdmin() {
  const { loading, isAdmin } = useSession();

  if (loading) return null;
  if (!isAdmin) {
    return (
      <div className="grid place-items-center py-20 text-center">
        <div className="max-w-[44ch]">
          <p className="font-mono text-body text-ink-3">404</p>
          <h1 className="mt-2 text-title font-bold tracking-[-0.02em]">
            That page does not exist
          </h1>
          <p className="mt-2 text-body leading-relaxed text-ink-3">
            The operations surfaces — sources, merge review and the flag queue — are available to
            accounts with the operator role. Ask whoever runs this instance; it is granted from
            the binary, never from a session, so a compromised login cannot mint one.
          </p>
        </div>
      </div>
    );
  }
  return <Outlet />;
}
