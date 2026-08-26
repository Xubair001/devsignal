import type { ReactNode } from 'react';

export type NavItem = { to: string; label: string; icon: ReactNode };

const stroke = {
  fill: 'none' as const,
  stroke: 'currentColor',
  strokeWidth: 2,
  strokeLinecap: 'round' as const,
  strokeLinejoin: 'round' as const,
};

export const NAV: NavItem[] = [
  {
    to: '/',
    label: 'Overview',
    icon: (
      <svg viewBox="0 0 24 24" {...stroke} className="size-4">
        <rect x="3" y="3" width="7" height="9" rx="1.5" />
        <rect x="14" y="3" width="7" height="5" rx="1.5" />
        <rect x="14" y="12" width="7" height="9" rx="1.5" />
        <rect x="3" y="16" width="7" height="5" rx="1.5" />
      </svg>
    ),
  },
  {
    to: '/sources',
    label: 'Sources',
    icon: (
      <svg viewBox="0 0 24 24" {...stroke} className="size-4">
        <path d="M12 3c4.4 0 8 1.3 8 3s-3.6 3-8 3-8-1.3-8-3 3.6-3 8-3Z" />
        <path d="M20 12c0 1.7-3.6 3-8 3s-8-1.3-8-3" />
        <path d="M4 6v12c0 1.7 3.6 3 8 3s8-1.3 8-3V6" />
      </svg>
    ),
  },
  {
    to: '/feed',
    label: 'Feed',
    icon: (
      <svg viewBox="0 0 24 24" {...stroke} className="size-4">
        <path d="M4 6h16M4 12h16M4 18h10" />
      </svg>
    ),
  },
  {
    to: '/flags',
    label: 'Flags',
    icon: (
      <svg viewBox="0 0 24 24" {...stroke} className="size-4">
        <path d="M4 15V4h12l-1.5 3L16 10H4" />
        <path d="M4 21v-6" />
      </svg>
    ),
  },
];
