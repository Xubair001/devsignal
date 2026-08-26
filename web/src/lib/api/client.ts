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

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(BASE + path, {
    ...init,
    headers: {
      Accept: 'application/json',
      ...(init?.body ? { 'Content-Type': 'application/json' } : {}),
      ...init?.headers,
    },
    // The session is a server-side record, so the cookie has to travel.
    credentials: 'include',
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
