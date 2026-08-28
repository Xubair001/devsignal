import type { ReactNode, InputHTMLAttributes, SelectHTMLAttributes } from 'react';
import { cn } from './cn';

/**
 * Form primitives.
 *
 * A label is always present and always associated — a placeholder is not a
 * label, it disappears the moment someone types, and it leaves a screen reader
 * with an unnamed control. `hint` carries the reasoning a field needs; `error`
 * replaces it, because two lines of competing guidance is worse than one.
 */
export function Field({
  label,
  hint,
  error,
  htmlFor,
  children,
  className,
}: {
  label: string;
  hint?: ReactNode;
  error?: string | null;
  htmlFor?: string;
  children: ReactNode;
  className?: string;
}) {
  return (
    <div className={cn('flex flex-col gap-1.5', className)}>
      <label
        htmlFor={htmlFor}
        className="text-label font-semibold uppercase tracking-[0.06em] text-ink-3"
      >
        {label}
      </label>
      {children}
      {error ? (
        <p className="flex items-start gap-1 text-meta font-medium text-bad">
          <span aria-hidden>✕</span>
          {error}
        </p>
      ) : hint ? (
        <p className="text-meta leading-relaxed text-ink-3">{hint}</p>
      ) : null}
    </div>
  );
}

const CONTROL =
  'w-full rounded-[10px] border border-line bg-surface px-3 py-2 text-body text-ink ' +
  'shadow-[var(--shadow-flat)] transition-all duration-[var(--dur-base)] ' +
  'ease-[var(--ease-out-quart)] placeholder:text-ink-3 ' +
  'hover:border-line-strong focus:border-brand focus:outline-none ' +
  'focus:ring-[3px] focus:ring-brand-wash disabled:opacity-50';

export function Input({ className, ...rest }: InputHTMLAttributes<HTMLInputElement>) {
  return <input {...rest} className={cn(CONTROL, className)} />;
}

export function Select({
  className,
  children,
  ...rest
}: SelectHTMLAttributes<HTMLSelectElement>) {
  return (
    <div className="relative">
      <select {...rest} className={cn(CONTROL, 'cursor-pointer appearance-none pr-9', className)}>
        {children}
      </select>
      <svg
        viewBox="0 0 24 24"
        fill="none"
        stroke="currentColor"
        strokeWidth="2.2"
        strokeLinecap="round"
        aria-hidden
        className="pointer-events-none absolute right-3 top-1/2 size-3.5 -translate-y-1/2 text-ink-3"
      >
        <path d="m6 9 6 6 6-6" />
      </svg>
    </div>
  );
}

/**
 * A labelled switch.
 *
 * A real <button role="switch">, not a styled checkbox with a div on top: the
 * state has to be announced, and aria-checked is what announces it.
 */
export function Toggle({
  checked,
  onChange,
  label,
  hint,
  disabled,
}: {
  checked: boolean;
  onChange: (v: boolean) => void;
  label: string;
  hint?: ReactNode;
  disabled?: boolean;
}) {
  return (
    <div className="flex items-start justify-between gap-4">
      <div className="min-w-0">
        <p className="text-body font-medium">{label}</p>
        {hint && <p className="mt-0.5 text-meta leading-relaxed text-ink-3">{hint}</p>}
      </div>
      <button
        type="button"
        role="switch"
        aria-checked={checked}
        aria-label={label}
        disabled={disabled}
        onClick={() => onChange(!checked)}
        className={cn(
          'relative mt-0.5 h-[22px] w-[38px] shrink-0 cursor-pointer rounded-full border',
          'transition-colors duration-[var(--dur-base)] ease-[var(--ease-out-quart)]',
          'disabled:cursor-not-allowed disabled:opacity-50',
          checked ? 'border-transparent bg-brand' : 'border-line bg-raised',
        )}
      >
        <span
          aria-hidden
          className={cn(
            'absolute top-[2px] size-[16px] rounded-full bg-white shadow-sm',
            'transition-transform duration-[var(--dur-base)] ease-[var(--ease-spring)]',
            checked ? 'translate-x-[19px]' : 'translate-x-[2px]',
          )}
        />
      </button>
    </div>
  );
}

/** A segmented choice. Better than a select for two or three options. */
export function Segmented<T extends string>({
  value,
  onChange,
  options,
  label,
}: {
  value: T;
  onChange: (v: T) => void;
  options: { value: T; label: string }[];
  label: string;
}) {
  return (
    <div
      role="radiogroup"
      aria-label={label}
      className="inline-flex gap-0.5 rounded-[10px] border border-line bg-raised p-0.5"
    >
      {options.map((o) => (
        <button
          key={o.value}
          type="button"
          role="radio"
          aria-checked={value === o.value}
          onClick={() => onChange(o.value)}
          className={cn(
            'cursor-pointer rounded-[7px] px-3 py-1.5 text-meta font-medium',
            'transition-all duration-[var(--dur-base)] ease-[var(--ease-out-quart)]',
            value === o.value
              ? 'bg-surface text-ink shadow-[var(--shadow-flat)]'
              : 'text-ink-3 hover:text-ink-2',
          )}
        >
          {o.label}
        </button>
      ))}
    </div>
  );
}
