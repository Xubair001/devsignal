import { useEffect, useRef, useState } from 'react';
import { NavLink, Outlet, useLocation } from 'react-router-dom';
import { useQuery } from '@tanstack/react-query';
import { adminApi } from '@/lib/api/admin';
import { qk } from '@/lib/queryKeys';
import { cn } from '@/components/ui/cn';
import { IconButton } from '@/components/ui/Button';
import { ThemeToggle } from './ThemeToggle';
import { CommandPalette } from './CommandPalette';
import { NAV } from './nav';

export function AppShell() {
  const [paletteOpen, setPaletteOpen] = useState(false);
  const [drawerOpen, setDrawerOpen] = useState(false);
  const [menu, setMenu] = useState<'bell' | 'who' | null>(null);
  const headerRef = useRef<HTMLElement>(null);
  const { pathname } = useLocation();

  /* The flag count is a real query, so the badge never claims work that is not
     there. It fails quietly: a missing count must not break the header. */
  const flags = useQuery({
    queryKey: qk.flags('open'),
    queryFn: () => adminApi.flags('open'),
    retry: false,
    staleTime: 30_000,
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

  useEffect(() => {
    const onClick = (e: MouseEvent) => {
      if (!headerRef.current?.contains(e.target as Node)) setMenu(null);
    };
    window.addEventListener('click', onClick);
    return () => window.removeEventListener('click', onClick);
  }, []);

  // Route changes close the transient chrome.
  useEffect(() => {
    setDrawerOpen(false);
    setMenu(null);
  }, [pathname]);

  const here = NAV.find((n) => n.to === pathname)?.label ?? 'Overview';
  const overlay = paletteOpen || drawerOpen;

  return (
    <div className="flex min-h-screen flex-col">
      <a
        href="#main"
        className="absolute left-3 top-[-60px] z-200 rounded-md bg-brand px-3.5 py-2 font-semibold text-white transition-[top] duration-200 focus:top-3"
      >
        Skip to content
      </a>

      <header ref={headerRef} className="glass sticky top-0 z-60 border-b border-line">
        <div className="mx-auto flex h-15 max-w-[1440px] items-center gap-4 px-5">
          <IconButton
            label="Open navigation"
            aria-expanded={drawerOpen}
            onClick={() => setDrawerOpen(true)}
            className="md:hidden"
          >
            <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" className="size-[17px]">
              <path d="M3 6h18M3 12h18M3 18h18" />
            </svg>
          </IconButton>

          <div className="flex shrink-0 items-center gap-2.5">
            <span
              aria-hidden
              className="grid size-7 place-items-center rounded-lg bg-linear-[140deg,var(--color-brand),#a855f7] shadow-[0_0_0_1px_var(--color-line),0_2px_8px_var(--color-brand-wash)]"
            >
              <svg viewBox="0 0 24 24" fill="none" stroke="#fff" strokeWidth="2.4" strokeLinecap="round" strokeLinejoin="round" className="size-[15px]">
                <path d="M3 15l4-6 4 3 4-7 6 10" />
              </svg>
            </span>
            <span className="hidden text-[14.5px] font-semibold tracking-tight sm:inline">DevSignal</span>
          </div>

          <nav aria-label="Breadcrumb" className="hidden items-center gap-1.5 text-[13px] text-ink-3 md:flex">
            <span className="opacity-45">/</span>
            <span>Operations</span>
            <span className="opacity-45">/</span>
            <span className="font-medium text-ink">{here}</span>
          </nav>

          <div className="flex-1" />

          <button
            onClick={() => setPaletteOpen(true)}
            aria-label="Search — press Command K"
            className="flex h-[34px] cursor-pointer items-center gap-2 rounded-[10px] border border-line bg-surface pl-3 pr-2 text-ink-3 transition-all duration-200 hover:border-line-strong hover:text-ink-2 hover:shadow-card md:min-w-[224px]"
          >
            <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" className="size-[15px] shrink-0">
              <circle cx="11" cy="11" r="7" />
              <path d="m20 20-3.5-3.5" />
            </svg>
            <span className="hidden flex-1 text-left text-[13px] md:inline">Search or jump to…</span>
            <kbd className="hidden rounded-[5px] border border-line bg-raised px-1.5 py-0.5 text-[11px] font-medium md:inline">
              ⌘K
            </kbd>
          </button>

          <div className="relative">
            <IconButton
              label={openFlags ? `Notifications, ${openFlags} open` : 'Notifications'}
              aria-expanded={menu === 'bell'}
              onClick={() => setMenu((m) => (m === 'bell' ? null : 'bell'))}
            >
              <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" className="size-[17px]">
                <path d="M18 8A6 6 0 0 0 6 8c0 7-3 9-3 9h18s-3-2-3-9" />
                <path d="M13.7 21a2 2 0 0 1-3.4 0" />
              </svg>
              {openFlags > 0 && (
                <span className="absolute right-1.5 top-1.5 size-[7px] rounded-full bg-bad ring-2 ring-glass" />
              )}
            </IconButton>
            <Menu open={menu === 'bell'} label="Notifications">
              <MenuHead title="Notifications" sub={openFlags ? `${openFlags} need attention` : 'Nothing waiting'} />
              {openFlags > 0 ? (
                <NavLink to="/flags" className={menuItem}>
                  <Dot tone="bad" />
                  <span>
                    <b className="font-semibold">{openFlags} reported listing{openFlags > 1 ? 's' : ''}</b>
                    <br />
                    <span className="text-[11.5px] text-ink-3">Waiting in the review queue</span>
                  </span>
                </NavLink>
              ) : (
                <p className="px-2.5 py-3 text-[12.5px] text-ink-3">The review queue is clear.</p>
              )}
            </Menu>
          </div>

          <span data-theme-toggle-wrap>
            <ThemeToggle />
          </span>

          <div className="relative">
            <button
              aria-label="Account menu"
              aria-expanded={menu === 'who'}
              onClick={() => setMenu((m) => (m === 'who' ? null : 'who'))}
              className="grid size-[30px] shrink-0 cursor-pointer place-items-center rounded-full bg-linear-[140deg,#34d399,var(--color-brand)] text-[11.5px] font-semibold text-white shadow-[0_0_0_1px_var(--color-line)] transition-all duration-200 hover:-translate-y-px hover:shadow-raise"
            >
              AZ
            </button>
            <Menu open={menu === 'who'} label="Account">
              <MenuHead title="Abdullah Zubair" sub="ops@devsignal.dev" mono />
              <button className={menuItem}>Profile and preferences</button>
              <button className={menuItem}>Workspace settings</button>
              <button className={menuItem}>Sign out</button>
            </Menu>
          </div>
        </div>
      </header>

      <nav aria-label="Sections" className="glass sticky top-15 z-50 hidden border-b border-line md:block">
        <div className="no-bar mx-auto flex max-w-[1440px] gap-0.5 overflow-x-auto px-5">
          {NAV.map((n) => (
            <NavLink
              key={n.to}
              to={n.to}
              end={n.to === '/'}
              className={({ isActive }) =>
                cn(
                  'group relative whitespace-nowrap px-3 pb-3 pt-2.5 text-[13.5px] transition-colors',
                  isActive ? 'font-semibold text-ink' : 'font-medium text-ink-3 hover:text-ink',
                )
              }
            >
              {({ isActive }) => (
                <>
                  {n.label}
                  {n.to === '/flags' && openFlags > 0 && (
                    <span
                      className={cn(
                        'ml-1.5 rounded-[5px] px-1.5 py-px text-[11px] font-semibold num',
                        isActive ? 'bg-brand-wash text-brand-ink' : 'bg-raised text-ink-3',
                      )}
                    >
                      {openFlags}
                    </span>
                  )}
                  <span
                    className={cn(
                      'absolute inset-x-2 -bottom-px h-0.5 origin-center rounded-t bg-brand transition-transform duration-250 ease-out-quart',
                      isActive ? 'scale-x-100' : 'scale-x-0',
                    )}
                  />
                </>
              )}
            </NavLink>
          ))}
        </div>
      </nav>

      <main id="main" className="mx-auto w-full max-w-[1440px] flex-1 px-5 pb-16 pt-5.5">
        <Outlet />
      </main>

      {/* overlays */}
      <div
        onClick={() => {
          setPaletteOpen(false);
          setDrawerOpen(false);
        }}
        className={cn(
          'fixed inset-0 z-100 bg-[rgb(9_13_22/0.5)] backdrop-blur-[3px] transition-opacity duration-200',
          overlay ? 'opacity-100' : 'pointer-events-none opacity-0',
        )}
      />

      <CommandPalette open={paletteOpen} onClose={() => setPaletteOpen(false)} />

      <aside
        aria-label="Navigation"
        aria-hidden={!drawerOpen}
        className={cn(
          'fixed inset-y-0 left-0 z-110 flex w-[min(280px,82vw)] flex-col gap-1 border-r border-line bg-surface p-3.5 shadow-float transition-transform duration-300 ease-out-quart',
          drawerOpen ? 'translate-x-0' : '-translate-x-full',
        )}
      >
        <div className="mb-2.5 flex items-center justify-between">
          <span className="text-[14.5px] font-semibold">DevSignal</span>
          <IconButton label="Close navigation" onClick={() => setDrawerOpen(false)}>
            <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" className="size-[17px]">
              <path d="M18 6 6 18M6 6l12 12" />
            </svg>
          </IconButton>
        </div>
        {NAV.map((n) => (
          <NavLink
            key={n.to}
            to={n.to}
            end={n.to === '/'}
            className={({ isActive }) =>
              cn(
                'flex items-center gap-2.5 rounded-md px-2.5 py-2.5 text-[14px] transition-colors',
                isActive
                  ? 'bg-brand-wash font-semibold text-brand-ink'
                  : 'font-medium text-ink-2 hover:bg-raised hover:text-ink',
              )
            }
          >
            {n.icon}
            {n.label}
            {n.to === '/flags' && openFlags > 0 && (
              <span className="ml-auto text-[11.5px] text-ink-3 num">{openFlags}</span>
            )}
          </NavLink>
        ))}
      </aside>
    </div>
  );
}

/* ---------------------------------------------------------------- bits --- */

const menuItem =
  'flex w-full cursor-pointer items-center gap-2.5 rounded-md px-2.5 py-2 text-left text-[13px] text-ink-2 no-underline transition-colors hover:bg-raised hover:text-ink';

function Menu({
  open,
  label,
  children,
}: {
  open: boolean;
  label: string;
  children: React.ReactNode;
}) {
  return (
    <div
      role="menu"
      aria-label={label}
      className={cn(
        'absolute right-0 top-[calc(100%+8px)] z-70 min-w-[240px] rounded-[14px] border border-line bg-surface p-1.5 shadow-float transition-[opacity,transform] duration-200 ease-out-quart',
        open ? 'opacity-100' : 'pointer-events-none -translate-y-1.5 scale-98 opacity-0',
      )}
    >
      {children}
    </div>
  );
}

function MenuHead({ title, sub, mono }: { title: string; sub: string; mono?: boolean }) {
  return (
    <div className="mb-1.5 border-b border-line px-2.5 pb-2.5 pt-2">
      <p className="text-[13.5px] font-semibold">{title}</p>
      <p className={cn('text-[12px] text-ink-3', mono && 'font-mono')}>{sub}</p>
    </div>
  );
}

function Dot({ tone }: { tone: 'bad' | 'warn' }) {
  return <span className={cn('mt-1.5 size-1.5 shrink-0 rounded-full', tone === 'bad' ? 'bg-bad' : 'bg-warn')} />;
}
