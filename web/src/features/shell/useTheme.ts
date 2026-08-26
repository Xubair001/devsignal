import { useCallback, useEffect, useState } from 'react';

export type Theme = 'light' | 'dark';

function read(): Theme {
  const stamp = document.documentElement.dataset.theme;
  if (stamp === 'dark' || stamp === 'light') return stamp;
  // No stamp means "follow the OS", which is the default state most viewers see.
  return window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light';
}

export function useTheme() {
  const [theme, setTheme] = useState<Theme>(read);

  // Track the OS while the user has made no explicit choice.
  useEffect(() => {
    const mq = window.matchMedia('(prefers-color-scheme: dark)');
    const onChange = () => {
      if (!document.documentElement.dataset.theme) setTheme(mq.matches ? 'dark' : 'light');
    };
    mq.addEventListener('change', onChange);
    return () => mq.removeEventListener('change', onChange);
  }, []);

  const toggle = useCallback(() => {
    const next: Theme = read() === 'dark' ? 'light' : 'dark';
    document.documentElement.dataset.theme = next;
    setTheme(next);
    try {
      localStorage.setItem('ds-theme', next);
    } catch {
      // Private mode. The choice holds for this session and is not remembered.
    }
  }, []);

  return { theme, toggle };
}
