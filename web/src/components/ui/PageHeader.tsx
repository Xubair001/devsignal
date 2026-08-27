import type { ReactNode } from 'react';

/**
 * One header shape for every page.
 *
 * The subtitle is not decoration: each of these screens shows numbers whose
 * meaning is easy to misread, and one line saying what the page is measuring is
 * the cheapest place to prevent that.
 */
export function PageHeader({
  title,
  subtitle,
  aside,
}: {
  title: string;
  subtitle?: ReactNode;
  aside?: ReactNode;
}) {
  return (
    <header className="flex flex-wrap items-start justify-between gap-3">
      <div className="min-w-0">
        <h1 className="text-[19px] font-bold tracking-[-0.022em]">{title}</h1>
        {subtitle && (
          <p className="mt-1 max-w-[68ch] text-[12.5px] leading-relaxed text-ink-3">
            {subtitle}
          </p>
        )}
      </div>
      {aside && <div className="flex shrink-0 items-center gap-2">{aside}</div>}
    </header>
  );
}
