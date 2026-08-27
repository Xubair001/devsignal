import { useEffect, useMemo, useRef, useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { useQueryClient } from '@tanstack/react-query';
import { useToast } from '@/components/ui/Toast';
import { navFor } from './nav';
import { useSession } from '@/features/auth/useSession';

type Command = { label: string; hint?: string; run: () => void };

export function CommandPalette({ open, onClose }: { open: boolean; onClose: () => void }) {
  const nav = useNavigate();
  const qc = useQueryClient();
  const toast = useToast();
  const [q, setQ] = useState('');
  const [active, setActive] = useState(0);
  const inputRef = useRef<HTMLInputElement>(null);
  const listRef = useRef<HTMLDivElement>(null);
  const { isAdmin } = useSession();

  const commands = useMemo<Command[]>(
    () => [
      /* Filtered by role from the same source as the sidebar. Three lists that
         each decide what exists is how a"hidden" destination stays reachable
         from one of them. */
      ...navFor(isAdmin)
        .flatMap((g) => g.items)
        .map((n) => ({
          label: `Go to ${n.label}`,
          hint: 'page',
          run: () => nav(n.to),
        })),
      {
        label: 'Refresh every panel',
        hint: 'data',
        run: () => {
          void qc.invalidateQueries();
          toast('Refetching');
        },
      },
      {
        label: 'Toggle theme',
        hint: 'view',
        run: () => document.querySelector<HTMLButtonElement>('[data-theme-toggle]')?.click(),
      },
    ],
    [nav, qc, toast, isAdmin],
  );

  const shown = useMemo(() => {
    const needle = q.trim().toLowerCase();
    if (!needle) return commands;
    return commands.filter((c) => c.label.toLowerCase().includes(needle));
  }, [commands, q]);

  useEffect(() => {
    if (open) {
      setQ('');
      setActive(0);
      // Focus after the transition starts so the caret does not jump.
      requestAnimationFrame(() => inputRef.current?.focus());
    }
  }, [open]);

  useEffect(() => {
    listRef.current?.querySelector('[data-active="true"]')?.scrollIntoView({ block: 'nearest' });
  }, [active]);

  if (!open) return null;

  const run = (i: number) => {
    const cmd = shown[i];
    if (!cmd) return;
    onClose();
    cmd.run();
  };

  return (
    <div
      role="dialog"
      aria-modal="true"
      aria-label="Command palette"
      className="fixed left-1/2 top-[14vh] z-110 w-[min(560px,calc(100vw-32px))] -translate-x-1/2 overflow-hidden rounded-[14px] border border-line-strong bg-surface shadow-float"
    >
      <div className="flex items-center gap-2.5 border-b border-line px-4 py-3">
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" className="size-4 shrink-0 text-ink-3">
          <circle cx="11" cy="11" r="7" />
          <path d="m20 20-3.5-3.5" />
        </svg>
        <input
          ref={inputRef}
          value={q}
          onChange={(e) => {
            setQ(e.target.value);
            setActive(0);
          }}
          onKeyDown={(e) => {
            if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
              e.preventDefault();
              if (!shown.length) return;
              setActive((a) => (a + (e.key === 'ArrowDown' ? 1 : shown.length - 1)) % shown.length);
            } else if (e.key === 'Enter') {
              e.preventDefault();
              run(active);
            }
          }}
          placeholder="Jump to a page or run an action…"
          autoComplete="off"
          spellCheck={false}
          className="flex-1 bg-transparent text-base text-ink outline-none placeholder:text-ink-3"
        />
        <kbd className="rounded-[5px] border border-line bg-raised px-1.5 py-0.5 text-label font-medium text-ink-3">
          ESC
        </kbd>
      </div>

      <div ref={listRef} role="listbox" aria-label="Results" className="max-h-[min(52vh,400px)] overflow-y-auto p-1.5">
        {shown.length === 0 ? (
          <p className="px-4 py-7 text-center text-body text-ink-3">Nothing matches that.</p>
        ) : (
          shown.map((c, i) => (
            <button
              key={c.label}
              role="option"
              aria-selected={i === active}
              data-active={i === active}
              onMouseEnter={() => setActive(i)}
              onClick={() => run(i)}
              className="flex w-full cursor-pointer items-center gap-2.5 rounded-md px-2.5 py-2 text-left text-body text-ink-2 transition-colors data-[active=true]:bg-brand-wash data-[active=true]:text-ink"
            >
              <span className="flex-1">{c.label}</span>
              {c.hint && (
                <kbd className="rounded-[5px] border border-line bg-raised px-1.5 py-0.5 text-micro font-medium text-ink-3">
                  {c.hint}
                </kbd>
              )}
            </button>
          ))
        )}
      </div>
    </div>
  );
}
