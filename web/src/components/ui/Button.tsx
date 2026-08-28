import type { AnchorHTMLAttributes, ButtonHTMLAttributes, ReactNode } from 'react';
import { cn } from './cn';

type Variant = 'primary' | 'secondary' | 'ghost' | 'danger';

const VARIANT: Record<Variant, string> = {
  primary:
    'bg-brand text-white border-transparent hover:brightness-110 hover:shadow-[0_4px_12px_var(--color-brand-wash)]',
  secondary: 'bg-surface text-ink-2 border-line hover:bg-raised hover:text-ink hover:border-line-strong',
  ghost: 'bg-transparent text-ink-2 border-transparent hover:bg-raised hover:text-ink',
  danger: 'bg-transparent text-ink-3 border-transparent hover:bg-bad-wash hover:text-bad',
};

const BASE =
  'inline-flex h-[31px] cursor-pointer items-center gap-1.5 rounded-md border px-[11px] ' +
  'text-meta font-medium no-underline transition-all duration-200 ease-out-quart ' +
  'hover:-translate-y-px disabled:pointer-events-none disabled:opacity-40 ' +
  'aria-disabled:pointer-events-none aria-disabled:opacity-40 ' +
  'aria-pressed:border-transparent aria-pressed:bg-brand-wash aria-pressed:text-brand-ink';

type Common = { variant?: Variant; children: ReactNode };

/**
 * Renders an `<a>` when `as="a"`, and that distinction is not cosmetic: a
 * control that navigates must be a real link so it is middle-clickable,
 * copyable and announced as a link. A button with an onClick that calls
 * `window.open` is a worse version of the same thing.
 */
type Props =
  | (Common & { as?: 'button' } & ButtonHTMLAttributes<HTMLButtonElement>)
  | (Common & { as: 'a' } & AnchorHTMLAttributes<HTMLAnchorElement>);

export function Button(props: Props) {
  const { variant = 'secondary', className, children } = props;

  if (props.as === 'a') {
    const { as: _as, variant: _v, className: _c, children: _ch, ...rest } = props;
    return (
      <a {...rest} className={cn(BASE, VARIANT[variant], className)}>
        {children}
      </a>
    );
  }

  const { as: _as, variant: _v, className: _c, children: _ch, ...rest } = props;
  return (
    <button {...rest} className={cn(BASE, VARIANT[variant], className)}>
      {children}
    </button>
  );
}

export function IconButton({
  label,
  children,
  className,
  ...rest
}: ButtonHTMLAttributes<HTMLButtonElement> & { label: string; children: ReactNode }) {
  return (
    <button
      {...rest}
      aria-label={label}
      className={cn(
        'relative grid size-[34px] shrink-0 cursor-pointer place-items-center rounded-[10px]',
        'border border-transparent text-ink-2 transition-colors duration-200 ease-out-quart',
        'hover:border-line hover:bg-raised hover:text-ink',
        className,
      )}
    >
      {children}
    </button>
  );
}
