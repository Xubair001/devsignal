import type { ReactNode } from 'react';
import { ApiError } from '@/lib/api/client';
import { Card } from './Card';

/**
 * Every list needs four states. A skeleton rather than a spinner, because a
 * skeleton preserves the layout and avoids the shift when data lands.
 */
export function Skeleton({ className }: { className?: string }) {
  return (
    <div
      aria-hidden
      className={
        'animate-pulse rounded-md bg-raised ' + (className ?? 'h-4 w-full')
      }
    />
  );
}

export function SkeletonCards({ count = 4, height = 'h-[132px]' }: { count?: number; height?: string }) {
  return (
    <>
      {Array.from({ length: count }, (_, i) => (
        <Card key={i} className={'p-4 ' + height}>
          <div className="flex h-full flex-col justify-between gap-3">
            <Skeleton className="h-3 w-24" />
            <Skeleton className="h-7 w-16" />
            <Skeleton className="h-3 w-32" />
          </div>
        </Card>
      ))}
    </>
  );
}

/**
 * The empty state carries product meaning here, so it takes a written
 * explanation rather than a shrug. "Nothing met your bar today" is a feature.
 */
export function EmptyState({
  title,
  children,
  icon,
}: {
  title: string;
  children?: ReactNode;
  icon?: ReactNode;
}) {
  return (
    <Card className="col-span-full flex flex-col items-center gap-2 px-6 py-12 text-center">
      {icon && <div className="mb-1 text-ink-3">{icon}</div>}
      <p className="text-[15px] font-semibold">{title}</p>
      {children && <p className="max-w-[46ch] text-[13px] leading-relaxed text-ink-3">{children}</p>}
    </Card>
  );
}

/** Errors say what went wrong and what to do. No apology, no vagueness. */
export function ErrorState({ error, onRetry }: { error: unknown; onRetry?: () => void }) {
  const api = error instanceof ApiError ? error : null;

  const [title, detail] = api?.unauthorized
    ? ['Your session expired', 'Sign in again to continue.']
    : api?.notFound
      ? [
          'This surface is not available to your account',
          'The operations console requires an administrator role. Ask an owner to grant it with the CLI.',
        ]
      : api
        ? [`The server returned ${api.status}`, api.body.slice(0, 220) || 'No detail was included.']
        : ['Could not reach the API', 'Check that the Go service is running on port 8080.'];

  return (
    <Card className="col-span-full flex flex-col items-start gap-3 border-bad/30 p-5">
      <div>
        <p className="text-[14px] font-semibold text-bad">{title}</p>
        <p className="mt-0.5 max-w-[60ch] text-[13px] leading-relaxed text-ink-2">{detail}</p>
      </div>
      {onRetry && (
        <button
          onClick={onRetry}
          className="cursor-pointer rounded-md border border-line bg-surface px-3 py-1.5 text-[12.5px] font-medium text-ink-2 transition-colors hover:bg-raised hover:text-ink"
        >
          Try again
        </button>
      )}
    </Card>
  );
}

export function SectionHead({
  title,
  hint,
  action,
}: {
  title: string;
  hint?: string;
  action?: ReactNode;
}) {
  return (
    <div className="mb-3 mt-7 flex flex-wrap items-baseline gap-x-2.5 gap-y-1 first:mt-0">
      <h2 className="text-[15px] font-semibold">{title}</h2>
      {hint && <p className="text-[12.5px] text-ink-3">{hint}</p>}
      {action && <div className="ml-auto">{action}</div>}
    </div>
  );
}

/** A missing route. Says what to do, not just what went wrong. */
export function NotFound() {
  return (
    <div className="grid place-items-center py-20 text-center">
      <div className="max-w-[40ch]">
        <p className="font-mono text-[13px] text-ink-3">404</p>
        <h1 className="mt-2 text-[19px] font-bold tracking-[-0.02em]">
          That page does not exist
        </h1>
        <p className="mt-2 text-[13px] leading-relaxed text-ink-3">
          Press <kbd className="rounded border border-line bg-raised px-1.5 py-0.5 font-mono text-[11px]">⌘K</kbd>{' '}
          to jump to any section.
        </p>
      </div>
    </div>
  );
}
