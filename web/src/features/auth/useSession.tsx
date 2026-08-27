import { useQuery } from '@tanstack/react-query';
import { authApi } from '@/lib/api/auth';
import { qk } from '@/lib/queryKeys';
import { token } from '@/lib/api/client';
import { ApiError } from '@/lib/api/client';

/**
 * Whether there is a live session.
 *
 * Asks the server rather than trusting the stored token: a token in
 * localStorage proves only that we once had one. It may be expired, revoked, or
 * for a database that has since been reset — all three are normal in
 * development, and all three look identical from the client.
 */
export function useSession() {
  const q = useQuery({
    queryKey: qk.session(),
    queryFn: () => authApi.me(),
    // No token means no request. Firing one guarantees a 401 and makes the
    // login screen flash an error the user did not cause.
    enabled: token() !== null,
    retry: false,
    staleTime: 5 * 60 * 1000,
  });

  const unauthorized = q.error instanceof ApiError && q.error.unauthorized;
  return {
    session: q.data ?? null,
    loading: token() !== null && q.isLoading,
    signedIn: !!q.data,
    unauthorized: token() === null || unauthorized,
    refetch: q.refetch,
  };
}
