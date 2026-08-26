import type { ReactNode } from 'react';
import { cn } from './cn';

type Props = {
  children: ReactNode;
  className?: string;
  /** Adds the hover lift. Off for cards that are not themselves interactive. */
  lift?: boolean;
  as?: 'div' | 'article' | 'section';
};

export function Card({ children, className, lift = false, as: Tag = 'div' }: Props) {
  return (
    <Tag
      className={cn(
        'rounded-[14px] border border-line bg-surface shadow-card',
        'transition-[transform,box-shadow,border-color] duration-200 ease-out-quart',
        lift && 'hover:-translate-y-0.5 hover:border-line-strong hover:shadow-raise',
        className,
      )}
    >
      {children}
    </Tag>
  );
}
