import type { ReactNode } from 'react';
import { cn } from './cn';

/**
 * The one horizontal container.
 *
 * Every page used its own max-width and gutter before this, which is why the
 * landing page read as a narrow column floating in empty space on a wide screen:
 * a 1100px column on a 1920px viewport leaves ~410px of dead gutter each side.
 *
 * The gutter now GROWS with the viewport instead of the content staying fixed —
 * px-5 on a phone through px-12 past 1280px — and the ceiling is 1440px, which
 * keeps running text inside a readable measure while letting tables, grids and
 * the hero use the room they are given.
 *
 * `width` exists for the two cases that genuinely differ:
 *   page   the default. Dense content, tables, grids.
 *   prose  a single column of reading text, capped near 70 characters.
 */
type Width = 'page' | 'prose';

const WIDTH: Record<Width, string> = {
  page: 'max-w-[1440px]',
  prose: 'max-w-[72ch]',
};

/**
 * How much air sits either side.
 *
 * Two values, because the two contexts genuinely differ: a public page has the
 * whole viewport and can afford a generous gutter, while the console sits beside
 * a 236px sidebar and a wide gutter there just squeezes the content twice. An
 * `!important` override at the call site would have said the same thing far less
 * clearly.
 */
type Gutter = 'page' | 'app';

const GUTTER: Record<Gutter, string> = {
  page: 'px-5 sm:px-7 lg:px-10 xl:px-12',
  app: 'px-4 sm:px-6',
};

export function Container({
  children,
  className,
  width = 'page',
  gutter = 'page',
  as: Tag = 'div',
}: {
  children: ReactNode;
  className?: string;
  width?: Width;
  gutter?: Gutter;
  as?: 'div' | 'section' | 'header' | 'footer' | 'main' | 'nav';
}) {
  return (
    <Tag
      /* A hook for the layout test, which asserts the container uses the
         viewport it is given. Measuring a heading instead would measure whatever
         column that heading happens to sit in. */
      data-container={gutter}
      className={cn('mx-auto w-full min-w-0', GUTTER[gutter], WIDTH[width], className)}
    >
      {children}
    </Tag>
  );
}

/**
 * Vertical rhythm for a stack of page sections.
 *
 * One value, so two pages cannot disagree about how far apart their cards sit —
 * the audit found gap-4 and gap-5 used interchangeably for the same job.
 */
export function Stack({
  children,
  className,
}: {
  children: ReactNode;
  className?: string;
}) {
  /* Children get min-w-0 so a wide table inside one cannot widen the page. See
     the note in Card. */
  return (
    <div className={cn('flex min-w-0 flex-col gap-4 sm:gap-5 [&>*]:min-w-0', className)}>
      {children}
    </div>
  );
}
