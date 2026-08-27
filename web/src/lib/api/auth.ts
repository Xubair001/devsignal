import { http, setToken, clearToken } from './client';
import type { LoginResponse, Session } from './types';

export const authApi = {
  login: async (email: string, password: string) => {
    const res = await http.post<LoginResponse>('/api/v1/auth/login', { email, password });
    // Stored immediately so every subsequent request carries it. The refresh
    // token is deliberately NOT persisted: it is a longer-lived credential and
    // this console has no rotation flow, so keeping it in storage would widen
    // the blast radius of an XSS for no capability we use.
    setToken(res.session_token);
    return res;
  },

  register: (email: string, password: string) =>
    http.post<unknown>('/api/v1/auth/register', { email, password }),

  logout: async () => {
    // Best effort: the server-side session is what matters, but a failed call
    // must not leave the client holding a token it thinks is good.
    try {
      await http.post<unknown>('/api/v1/auth/logout');
    } finally {
      clearToken();
    }
  },

  me: () => http.get<Session>('/api/v1/me'),

  /** Consumes a link from an email. Unauthenticated: the token is the authority. */
  verifyEmail: (token: string) => http.post<void>('/api/v1/auth/verify', { token }),

  /**
   * Issues a fresh link for the CALLER's own address.
   *
   * Takes no email parameter, deliberately: an endpoint that accepted one would
   * be an enumeration oracle and a way to have us mail strangers on request.
   */
  resendVerification: () => http.post<void>('/api/v1/account/resend-verification'),
};
