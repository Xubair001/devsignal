/**
 * The one place a request is made.
 *
 * Errors are mapped into a typed shape rather than thrown as bare `Error`, so a
 * component can distinguish "you are not signed in" from "the server broke"
 * without string-matching a message.
 */
export class ApiError extends Error {
  /* Explicit fields rather than constructor parameter properties: the latter
     emit runtime code, which `erasableSyntaxOnly` rules out so the type layer
     stays strippable by any bundler. */
  readonly status: number;
  readonly body: string;

  constructor(status: number, body: string) {
    super(`${status}: ${body || 'request failed'}`);
    this.name = 'ApiError';
    this.status = status;
    this.body = body;
  }

  get unauthorized(): boolean {
    return this.status === 401;
  }
  /** The admin surface answers 404 for a non-admin, on purpose. */
  get notFound(): boolean {
    return this.status === 404;
  }
  get conflict(): boolean {
    return this.status === 409;
  }
}

const BASE = import.meta.env.VITE_API_BASE ?? '';

const TOKEN_KEY = 'ds-token';

/**
 * The session token, sent as a bearer header.
 *
 * A header rather than a cookie, deliberately. The API accepts only
 * `Authorization: Bearer`, and adding cookie auth to match the client would
 * bring a CSRF surface with it: a cookie is attached by the browser to
 * cross-site requests automatically, and this console issues state-changing
 * POSTs (save, apply, dismiss, quarantine, purge). A token read from storage is
 * never sent by anyone but us, so there is nothing to forge.
 *
 * `VITE_DEV_TOKEN` is a development convenience so a local console works without
 * pasting anything. It is only ever read when storage is empty, and it must not
 * be set in a deployed build — Vite inlines env values into the bundle.
 */
export function token(): string | null {
  try {
    const stored = localStorage.getItem(TOKEN_KEY);
    if (stored) return stored;
  } catch {
    // Private mode, or storage blocked. Fall through to the dev value.
  }
  return (import.meta.env.VITE_DEV_TOKEN as string | undefined) ?? null;
}

export function setToken(value: string): void {
  try {
    localStorage.setItem(TOKEN_KEY, value.trim());
  } catch {
    // Nothing useful to do: the next request will 401 and say so.
  }
}

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const t = token();
  const res = await fetch(BASE + path, {
    ...init,
    headers: {
      Accept: 'application/json',
      ...(init?.body ? { 'Content-Type': 'application/json' } : {}),
      ...(t ? { Authorization: `Bearer ${t}` } : {}),
      ...init?.headers,
    },
    // Deliberately omitted: see token() above. The session travels in the
    // header, so no cookie is sent and there is no CSRF surface to defend.
    credentials: 'omit',
  });

  if (!res.ok) {
    throw new ApiError(res.status, await res.text().catch(() => ''));
  }
  if (res.status === 204) return undefined as T;
  return (await res.json()) as T;
}

export const http = {
  get: <T>(path: string) => request<T>(path),
  post: <T>(path: string, body?: unknown) =>
    request<T>(path, { method: 'POST', body: body ? JSON.stringify(body) : undefined }),
  del: <T>(path: string) => request<T>(path, { method: 'DELETE' }),
};
