import { useEffect, useRef, useState } from 'react';
import { NavLink, Outlet, useLocation } from 'react-router-dom';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { adminApi } from '@/lib/api/admin';
import { authApi } from '@/lib/api/auth';
import { qk } from '@/lib/queryKeys';
import { cn } from '@/components/ui/cn';
import { IconButton } from '@/components/ui/Button';
import { ThemeToggle } from './ThemeToggle';
import { CommandPalette } from './CommandPalette';
import { NAV_GROUPS } from './nav';
import { useSession } from '@/features/auth/useSession';
import { LoginPage } from '@/features/auth/LoginPage';

/**
 * The app frame.
 *
 * A persistent sidebar on desktop and a drawer below it, rather than a top-only
 * nav: there are nine destinations in two groups, and a horizontal bar either
 * truncates them or turns into a menu nobody opens.
 *
 * The session gate lives here so no page ever renders against a 401. A page that
 * mounts, fires four queries and then shows four error cards is a worse
 * experience than one login screen.
 */
export function AppShell() {
  const [paletteOpen, setPaletteOpen] = useState(false);
  const [drawerOpen, setDrawerOpen] = useState(false);
  const [menu, setMenu] = useState<'who' | null>(null);
  const menuRef = useRef<HTMLDivElement>(null);
  const { pathname } = useLocation();
  const { session, loading, signedIn } = useSession();

  const flags = useQuery({
    queryKey: qk.flags('open'),
    queryFn: () => adminApi.flags('open'),
    retry: false,
    staleTime: 30_000,
    enabled: signedIn,
  });
  const openFlags = flags.data?.flags.length ?? 0;

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 'k') {
        e.preventDefault();
        setPaletteOpen((o) => !o);
      }
      if (e.key === 'Escape') {
        setPaletteOpen(false);
        setDrawerOpen(false);
        setMenu(null);
      }
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, []);

  /* Route change closes every transient surface. Leaving a drawer open across a
     navigation covers the page the user just asked for. */
  useEffect(() => {
    setDrawerOpen(false);
    setMenu(null);
    setPaletteOpen(false);
  }, [pathname]);

  useEffect(() => {
    if (!menu) return;
    const onDown = (e: MouseEvent) => {
      if (menuRef.current && !menuRef.current.contains(e.target as Node)) setMenu(null);
    };
    document.addEventListener('mousedown', onDown);
    return () => document.removeEventListener('mousedown', onDown);
  }, [menu]);

  if (loading) {
    return (
      <div className="grid min-h-dvh place-items-center">
        <p className="text-[13px] text-ink-3">Checking your session…</p>
      </div>
    );
  }
  if (!signedIn) return <LoginPage />;

  return (
    <div className="min-h-dvh lg:grid lg:grid-cols-[236px_1fr]">
      {/* ------------------------------------------------------------ sidebar */}
      <aside className="sticky top-0 hidden h-dvh flex-col border-r border-line bg-surface/60 lg:flex">
        <Brand />
        <nav className="flex-1 overflow-y-auto px-3 py-2 no-bar" aria-label="Main">
          {NAV_GROUPS.map((group) => (
            <div key={group.heading} className="mb-5">
              <p className="mb-1.5 px-2.5 text-[10.5px] font-bold uppercase tracking-[0.09em] text-ink-3">
                {group.heading}
              </p>
              <ul className="flex flex-col gap-0.5">
                {group.items.map((item) => (
                  <li key={item.to}>
                    <SideLink
                      to={item.to}
                      end={item.end}
                      icon={item.icon}
                      label={item.label}
                      badge={item.to === '/flags' && openFlags > 0 ? openFlags : undefined}
                    />
                  </li>
                ))}
              </ul>
            </div>
          ))}
        </nav>
        <SidebarFooter />
      </aside>

      <div className="flex min-w-0 flex-col">
        {/* ------------------------------------------------------------ header */}
        <header className="sticky top-0 z-50 border-b border-glass-line bg-glass glass">
          <div className="mx-auto flex h-[54px] max-w-[1180px] items-center gap-2 px-4 sm:px-6">
            <IconButton
              label="Open navigation"
              className="lg:hidden"
              onClick={() => setDrawerOpen(true)}
            >
              <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2"
                strokeLinecap="round" aria-hidden className="size-[18px]">
                <path d="M4 7h16M4 12h16M4 17h16" />
              </svg>
            </IconButton>

            <Breadcrumbs pathname={pathname} />

            <div className="ml-auto flex items-center gap-1.5">
              <button
                onClick={() => setPaletteOpen(true)}
                className={cn(
                  'group hidden h-[34px] cursor-pointer items-center gap-2 rounded-[10px] border',
                  'border-line bg-surface/70 pl-2.5 pr-2 text-[12.5px] text-ink-3',
                  'transition-all duration-[var(--dur-base)] ease-[var(--ease-out-quart)]',
                  'hover:border-line-strong hover:text-ink-2 sm:flex',
                )}
                aria-label="Search"
              >
                <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2"
                  strokeLinecap="round" aria-hidden className="size-3.5">
                  <circle cx="11" cy="11" r="7" />
                  <path d="m20 20-3.5-3.5" />
                </svg>
                Search
                <kbd className="ml-4 rounded border border-line bg-raised px-1.5 py-0.5 font-mono text-[10.5px]">
                  ⌘K
                </kbd>
              </button>

              <ThemeToggle />

              <div className="relative" ref={menuRef}>
                <button
                  onClick={() => setMenu(menu === 'who' ? null : 'who')}
                  aria-expanded={menu === 'who'}
                  aria-haspopup="menu"
                  aria-label="Account"
                  className={cn(
                    'grid size-[34px] cursor-pointer place-items-center rounded-full',
                    'border border-brand-edge bg-brand-wash text-[11.5px] font-bold text-brand-ink',
                    'transition-transform duration-[var(--dur-base)] ease-[var(--ease-spring)]',
                    'hover:scale-105',
                  )}
                >
                  {initials(session?.user_id)}
                </button>

                {menu === 'who' && (
                  <div
                    role="menu"
                    className="absolute right-0 top-[calc(100%+8px)] w-[248px] rounded-[14px] border border-line bg-surface p-1.5 shadow-[var(--shadow-float)]"
                  >
                    <div className="px-2.5 py-2">
                      <p className="text-[11px] font-semibold uppercase tracking-[0.06em] text-ink-3">
                        Signed in
                      </p>
                      <p className="mt-0.5 truncate font-mono text-[11.5px] text-ink-2">
                        {session?.user_id}
                      </p>
                    </div>
                    <div className="my-1 h-px bg-line" />
                    <MenuLink to="/profile" label="Profile" />
                    <MenuLink to="/settings" label="Notifications" />
                    <div className="my-1 h-px bg-line" />
                    <SignOut />
                  </div>
                )}
              </div>
            </div>
          </div>
        </header>

        <main className="mx-auto w-full max-w-[1180px] flex-1 px-4 py-6 sm:px-6">
          <Outlet />
        </main>

        <footer className="mx-auto w-full max-w-[1180px] px-4 pb-8 sm:px-6">
          <p className="border-t border-line pt-4 text-[11.5px] leading-relaxed text-ink-3">
            Bands and factor contributions, never a bare percentage. Nothing on this page is
            rendered that cannot be derived from something we observed.
          </p>
        </footer>
      </div>

      {/* ------------------------------------------------------------- drawer */}
      {drawerOpen && (
        <div className="fixed inset-0 z-70 lg:hidden">
          <button
            aria-label="Close navigation"
            onClick={() => setDrawerOpen(false)}
            className="absolute inset-0 cursor-default bg-black/45 backdrop-blur-[2px]"
          />
          <div
            className="absolute inset-y-0 left-0 flex w-[260px] flex-col border-r border-line bg-surface"
            style={{ animation: 'rise var(--dur-base) var(--ease-out-quart) both' }}
          >
            <Brand />
            <nav className="flex-1 overflow-y-auto px-3 py-2" aria-label="Main">
              {NAV_GROUPS.map((group) => (
                <div key={group.heading} className="mb-5">
                  <p className="mb-1.5 px-2.5 text-[10.5px] font-bold uppercase tracking-[0.09em] text-ink-3">
                    {group.heading}
                  </p>
                  <ul className="flex flex-col gap-0.5">
                    {group.items.map((item) => (
                      <li key={item.to}>
                        <SideLink
                          to={item.to}
                          end={item.end}
                          icon={item.icon}
                          label={item.label}
                          badge={item.to === '/flags' && openFlags > 0 ? openFlags : undefined}
                        />
                      </li>
                    ))}
                  </ul>
                </div>
              ))}
            </nav>
            <SidebarFooter />
          </div>
        </div>
      )}

      <CommandPalette open={paletteOpen} onClose={() => setPaletteOpen(false)} />
    </div>
  );
}

function Brand() {
  return (
    <div className="flex h-[54px] items-center gap-2.5 border-b border-line px-4">
      <span
        aria-hidden
        className="grid size-[26px] place-items-center rounded-[8px] border border-brand-edge bg-brand-wash"
      >
        <svg viewBox="0 0 24 24" fill="none" className="size-[15px] text-brand">
          <path d="M4 17.5 9 11l4 4 6.5-9" stroke="currentColor" strokeWidth="2.4"
            strokeLinecap="round" strokeLinejoin="round" />
          <circle cx="19.5" cy="6" r="2" fill="currentColor" />
        </svg>
      </span>
      <span className="text-[14px] font-bold tracking-[-0.02em]">DevSignal</span>
    </div>
  );
}

function SideLink({
  to,
  end,
  icon,
  label,
  badge,
}: {
  to: string;
  end?: boolean;
  icon: React.ReactNode;
  label: string;
  badge?: number;
}) {
  return (
    <NavLink
      to={to}
      end={end}
      className={({ isActive }) =>
        cn(
          'group relative flex items-center gap-2.5 rounded-[9px] px-2.5 py-[7px]',
          'text-[13px] font-medium transition-all duration-[var(--dur-base)]',
          'ease-[var(--ease-out-quart)]',
          isActive
            ? 'bg-brand-wash text-brand-ink'
            : 'text-ink-2 hover:bg-raised hover:text-ink',
        )
      }
    >
      {({ isActive }) => (
        <>
          {/* An active rail, not just a background: it survives a low-contrast
              theme and reads at a glance in a nine-item list. */}
          <span
            aria-hidden
            className={cn(
              'absolute -left-3 top-1/2 h-4 w-[3px] -translate-y-1/2 rounded-r-full',
              'transition-all duration-[var(--dur-base)] ease-[var(--ease-out-quart)]',
              isActive ? 'bg-brand opacity-100' : 'opacity-0',
            )}
          />
          {icon}
          <span className="flex-1 truncate">{label}</span>
          {badge !== undefined && (
            <span className="num rounded-full bg-warn-wash px-1.5 py-0.5 text-[10.5px] font-bold text-warn">
              {badge}
            </span>
          )}
        </>
      )}
    </NavLink>
  );
}

function SidebarFooter() {
  return (
    <div className="border-t border-line px-4 py-3">
      <p className="text-[11px] leading-relaxed text-ink-3">
        Corpus kept <span className="font-semibold text-ink-2">true</span>, not just large.
      </p>
    </div>
  );
}

function MenuLink({ to, label }: { to: string; label: string }) {
  return (
    <NavLink
      to={to}
      role="menuitem"
      className="flex rounded-md px-2.5 py-1.5 text-[12.5px] text-ink-2 transition-colors hover:bg-raised hover:text-ink"
    >
      {label}
    </NavLink>
  );
}

function SignOut() {
  const qc = useQueryClient();
  return (
    <button
      role="menuitem"
      onClick={async () => {
        await authApi.logout();
        // Reset rather than invalidate: invalidating refetches, and refetching
        // with no token means a burst of 401s behind the login screen.
        await qc.resetQueries();
      }}
      className="flex w-full cursor-pointer rounded-md px-2.5 py-1.5 text-left text-[12.5px] text-ink-2 transition-colors hover:bg-bad-wash hover:text-bad"
    >
      Sign out
    </button>
  );
}

const LABELS: Record<string, string> = {
  '': 'Overview',
  feed: 'Today',
  saved: 'Saved',
  browse: 'Corpus',
  profile: 'Profile',
  settings: 'Notifications',
  sources: 'Sources',
  flags: 'Flags',
};

function Breadcrumbs({ pathname }: { pathname: string }) {
  const parts = pathname.split('/').filter(Boolean);
  const head = LABELS[parts[0] ?? ''] ?? parts[0] ?? 'Overview';
  const isDetail = parts.length > 1;

  return (
    <nav aria-label="Breadcrumb" className="flex min-w-0 items-center gap-1.5 text-[13px]">
      <span className={isDetail ? 'text-ink-3' : 'font-semibold'}>{head}</span>
      {isDetail && (
        <>
          <span aria-hidden className="text-ink-3">
            /
          </span>
          <span className="truncate font-semibold">Detail</span>
        </>
      )}
    </nav>
  );
}

/** Two characters from the user id. A real avatar needs a name we do not have. */
function initials(userID?: string): string {
  if (!userID) return '··';
  return userID.slice(0, 2).toUpperCase();
}
