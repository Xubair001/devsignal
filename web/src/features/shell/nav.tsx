import type { ReactNode } from 'react';

export type NavItem = {
  to: string;
  label: string;
  icon: ReactNode;
  end?: boolean;
  /** Hidden from a non-admin. Usability, not security — the server gates it. */
  adminOnly?: boolean;
};
export type NavGroup = { heading: string; items: NavItem[]; adminOnly?: boolean };

const stroke = {
  fill: 'none' as const,
  stroke: 'currentColor',
  strokeWidth: 1.9,
  strokeLinecap: 'round' as const,
  strokeLinejoin: 'round' as const,
};

const I = ({ children }: { children: ReactNode }) => (
  <svg viewBox="0 0 24 24" {...stroke} aria-hidden className="size-[17px] shrink-0">
    {children}
  </svg>
);

/**
 * Navigation, grouped.
 *
 * Grouped rather than one flat list because the two halves answer different
 * questions and are used by different people: the product surfaces are what a
 * developer looks at, the operations ones are what someone keeping the corpus
 * true looks at. A single list of nine items hides that distinction and makes
 * both harder to scan.
 */
export const NAV_GROUPS: NavGroup[] = [
  {
    heading: 'For you',
    items: [
      {
        to: '/app/feed',
        label: 'Today',
        icon: (
          <I>
            <path d="M4 17.5 9 11l4 4 6.5-9" />
            <circle cx="19.5" cy="6" r="1.6" />
          </I>
        ),
      },
      {
        to: '/app/saved',
        label: 'Saved',
        icon: (
          <I>
            <path d="m19 21-7-5-7 5V5a2 2 0 0 1 2-2h10a2 2 0 0 1 2 2Z" />
          </I>
        ),
      },
      {
        to: '/app/gaps',
        label: 'What you are missing',
        icon: (
          <I>
            <path d="M4 19V9M10 19V5M16 19v-7M20 19H3" />
          </I>
        ),
      },
      {
        to: '/app/browse',
        label: 'Corpus',
        icon: (
          <I>
            <circle cx="11" cy="11" r="7" />
            <path d="m20 20-3.5-3.5" />
          </I>
        ),
      },
      {
        to: '/app/profile',
        label: 'Profile',
        icon: (
          <I>
            <circle cx="12" cy="8" r="3.4" />
            <path d="M5 20a7 7 0 0 1 14 0" />
          </I>
        ),
      },
      {
        to: '/app/settings',
        label: 'Notifications',
        icon: (
          <I>
            <path d="M18 8a6 6 0 1 0-12 0c0 7-2 8-2 8h16s-2-1-2-8" />
            <path d="M10.3 20a2 2 0 0 0 3.4 0" />
          </I>
        ),
      },
    ],
  },
  {
    heading: 'Operations',
    adminOnly: true,
    items: [
      {
        to: '/app/overview',
        label: 'Overview',
        icon: (
          <I>
            <rect x="3" y="3" width="7.5" height="7.5" rx="1.6" />
            <rect x="13.5" y="3" width="7.5" height="7.5" rx="1.6" />
            <rect x="3" y="13.5" width="7.5" height="7.5" rx="1.6" />
            <rect x="13.5" y="13.5" width="7.5" height="7.5" rx="1.6" />
          </I>
        ),
      },
      {
        to: '/app/sources',
        label: 'Sources',
        icon: (
          <I>
            <ellipse cx="12" cy="6" rx="7.5" ry="3" />
            <path d="M4.5 6v6c0 1.7 3.4 3 7.5 3s7.5-1.3 7.5-3V6" />
            <path d="M4.5 12v6c0 1.7 3.4 3 7.5 3s7.5-1.3 7.5-3v-6" />
          </I>
        ),
      },
      {
        to: '/app/merges',
        label: 'Merge review',
        icon: (
          <I>
            <path d="M7 4v6a4 4 0 0 0 4 4h6" />
            <path d="M14 11l3 3-3 3" />
            <circle cx="7" cy="4" r="1.6" />
          </I>
        ),
      },
      {
        to: '/app/flags',
        label: 'Flags',
        icon: (
          <I>
            <path d="M5 21V4h9l-1 3 1 3H5" />
          </I>
        ),
      },
    ],
  },
];

/** Flat list, for the command palette and the mobile drawer. */
export const NAV: NavItem[] = NAV_GROUPS.flatMap((g) =>
  g.items.map((i) => ({ ...i, adminOnly: i.adminOnly ?? g.adminOnly })),
);

/**
 * The groups this role may see.
 *
 * Filtering here rather than at each render site means the sidebar, the drawer
 * and the command palette cannot disagree about what exists — which is exactly
 * how a"hidden" link ends up reachable from one of the three.
 */
export function navFor(isAdmin: boolean): NavGroup[] {
  return NAV_GROUPS.filter((g) => isAdmin || !g.adminOnly).map((g) => ({
    ...g,
    items: g.items.filter((i) => isAdmin || !(i.adminOnly ?? g.adminOnly)),
  }));
}
