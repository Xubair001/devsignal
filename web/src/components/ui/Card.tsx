import type { ReactNode } from 'react';
import { cn } from './cn';

/**
 * The surface every panel sits on.
 *
 * `pad` is an enumerated size rather than a free className, because the audit
 * found seven different paddings in use across the app — p-2, p-2.5, p-3, p-3.5,
 * p-4, p-5 and p-0 — which is what makes a set of cards read as unrelated
 * rectangles rather than one system. Three named sizes cover every real case:
 *
 *   none    the card is a container for its own layout (a table, a list)
 *   tight   a control strip or a dense row
 *   normal  a content panel. The default, and the right answer almost always.
 *
 * Padding tightens on small screens in one place here, so no page has to
 * remember to do it.
 */
type Pad = 'none' | 'tight' | 'normal';

const PAD: Record<Pad, string> = {
  none: '',
  tight: 'p-3 sm:p-3.5',
  normal: 'p-4 sm:p-5',
};

type Props = {
  children: ReactNode;
  className?: string;
  /** Adds the hover lift. Off for cards that are not themselves interactive. */
  lift?: boolean;
  pad?: Pad;
  as?: 'div' | 'article' | 'section' | 'li';
};

export function Card({
  children,
  className,
  lift = false,
  pad = 'normal',
  as: Tag = 'div',
}: Props) {
  return (
    <Tag
      className={cn(
        'rounded-[var(--radius-lg)] border border-line bg-surface shadow-card',
        'transition-[transform,box-shadow,border-color] duration-[var(--dur-base)]',
        'ease-[var(--ease-out-quart)]',
        PAD[pad],
        lift && 'hover:-translate-y-0.5 hover:border-line-strong hover:shadow-raise',
        className,
      )}
    >
      {children}
    </Tag>
  );
}

/**
 * A horizontal-overflow wrapper.
 *
 * Wide content — tables, wide diagnostic rows — has to scroll inside its own
 * container, never by making the page body scroll sideways. A page that pans
 * horizontally on a phone is the single most common responsive failure, and it
 * is always this.
 */
export function ScrollX({
  children,
  className,
}: {
  children: ReactNode;
  className?: string;
}) {
  return (
    <div className={cn('-mx-px overflow-x-auto overscroll-x-contain', className)}>
      {children}
    </div>
  );
}
