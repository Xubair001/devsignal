import { createContext, useCallback, useContext, useRef, useState } from 'react';
import type { ReactNode } from 'react';

type Toast = { id: number; text: string; tone: 'ok' | 'bad' };
type Show = (text: string, tone?: 'ok' | 'bad') => void;

const ToastCtx = createContext<Show | null>(null);

/**
 * Returns the show function itself rather than an object wrapping it. Every call
 * site wants `toast('Saved')`, and `toast.show('Saved')` is a layer that earns
 * nothing.
 */
export function useToast(): Show {
  const show = useContext(ToastCtx);
  if (!show) throw new Error('useToast must be used inside <ToastProvider>');
  return show;
}

export function ToastProvider({ children }: { children: ReactNode }) {
  const [toasts, setToasts] = useState<Toast[]>([]);
  const seq = useRef(0);

  const show = useCallback((text: string, tone: 'ok' | 'bad' = 'ok') => {
    const id = ++seq.current;
    setToasts((t) => [...t, { id, text, tone }]);
    setTimeout(() => setToasts((t) => t.filter((x) => x.id !== id)), 2600);
  }, []);

  return (
    <ToastCtx.Provider value={show}>
      {children}
      <div
        role="status"
        aria-live="polite"
        className="pointer-events-none fixed bottom-5 left-1/2 z-120 flex -translate-x-1/2 flex-col items-center gap-2"
      >
        {toasts.map((t) => (
          <div
            key={t.id}
            className={
              'flex animate-[toast_.22s_ease-out] items-center gap-2 rounded-full px-4 py-2.5 text-[13px] font-medium shadow-float ' +
              (t.tone === 'bad' ? 'bg-bad text-white' : 'bg-ink text-ground')
            }
          >
            <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.4" strokeLinecap="round" strokeLinejoin="round" className="size-[15px]">
              {t.tone === 'bad' ? <path d="M18 6 6 18M6 6l12 12" /> : <path d="m4 12 5 5L20 6" />}
            </svg>
            {t.text}
          </div>
        ))}
      </div>
      <style>{`@keyframes toast{from{opacity:0;transform:translateY(12px)}}`}</style>
    </ToastCtx.Provider>
  );
}
