import { Link } from 'react-router-dom';
import { ThemeToggle } from '@/features/shell/ThemeToggle';
import { Mark } from '@/components/ui/Mark';
import { cn } from '@/components/ui/cn';
import { Container } from '@/components/ui/Container';

/**
 * The public page.
 *
 * Every claim here is one the product can actually keep, which is the whole
 * pitch and also a constraint on the copy: there is no"10,000 jobs" number,
 * no"we find you the perfect role", no testimonial. Blueprint §3 binds the
 * marketing surface as much as the app — inventing a figure here to make the
 * page feel substantial would discredit the honest ones inside it.
 *
 * The specimen card is real markup with real values, not a screenshot, so it
 * cannot drift from what the feed actually renders.
 */
export function LandingPage() {
  return (
    <div className="min-h-dvh">
      <Header />
      <Hero />
      <Questions />
      <Honesty />
      <Closing />
      <Footer />
    </div>
  );
}

function Header() {
  return (
    <header className="sticky top-0 z-50 border-b border-glass-line bg-glass glass">
      <Container as="nav" className="flex h-[62px] items-center gap-3 sm:h-[66px]">
        {/* min-h keeps the tap target at the floor even though the mark is
            smaller than it. A 28px link is a miss-tap on a phone. */}
        <Link to="/" className="flex min-h-[36px] items-center gap-2.5">
          <Mark size={28} />
          <span className="text-lead font-bold tracking-[-0.02em]">DevSignal</span>
        </Link>
        <nav className="ml-auto flex items-center gap-1.5 sm:gap-2.5">
          <ThemeToggle />
          <Link
            to="/login"
            className="rounded-[9px] px-3 py-2 text-body font-medium text-ink-2 transition-colors hover:text-ink"
          >
            Sign in
          </Link>
          <Link
            to="/register"
            className={cn(
              'rounded-[9px] border border-transparent bg-brand px-3.5 py-2 text-body',
              'font-semibold text-white transition-all duration-[var(--dur-base)]',
              'ease-[var(--ease-out-quart)] hover:-translate-y-px hover:brightness-110',
            )}
          >
            Get started
          </Link>
        </nav>
      </Container>
    </header>
  );
}

function Hero() {
  return (
    <Container as="section" className="pb-16 pt-12 sm:pb-24 sm:pt-20">
      <div className="grid items-center gap-12 lg:grid-cols-[1.05fr_1fr] lg:gap-14">
        <div className="rise">
          <p className="mb-5 inline-flex items-center gap-2 rounded-full border border-line bg-surface px-3 py-1.5 text-label font-semibold uppercase tracking-[0.07em] text-ink-3">
            <span aria-hidden className="size-1.5 rounded-full bg-good" />
            Verified liveness, not a bigger index
          </p>

          <h1 className="text-hero font-extrabold leading-[1.08] tracking-[-0.03em] sm:text-hero">
            An estimated one in four job listings is a ghost.
            <span className="mt-2 block text-brand">We check, then tell you why.</span>
          </h1>

          <p className="mt-6 max-w-[54ch] text-lead leading-[1.65] text-ink-2">
            DevSignal is an explainable recommender over a corpus we keep true. Every role it
            shows you carries the arithmetic behind its rating and the moment we last confirmed
            the role was open — because a recommendation you cannot check is a guess with better
            typography.
          </p>

          <div className="mt-8 flex flex-wrap items-center gap-3">
            <Link
              to="/register"
              className={cn(
                'inline-flex h-11 items-center rounded-[11px] bg-brand px-5 text-base',
                'font-semibold text-white shadow-[0_6px_18px_-6px_var(--color-brand-edge)]',
                'transition-all duration-[var(--dur-base)] ease-[var(--ease-out-quart)]',
                'hover:-translate-y-0.5 hover:brightness-110',
              )}
            >
              Create an account
            </Link>
            <Link
              to="/login"
              className="inline-flex h-11 items-center rounded-[11px] border border-line bg-surface px-5 text-base font-medium text-ink-2 transition-all duration-[var(--dur-base)] hover:border-line-strong hover:text-ink"
            >
              Sign in
            </Link>
          </div>

          <p className="mt-5 text-meta leading-relaxed text-ink-3">
            No credit card, because there is nothing to charge for yet. This is early software and
            the corpus is small — see what it does and does not know below.
          </p>
        </div>

        <SpecimenCard />
      </div>
    </Container>
  );
}

/**
 * A real feed card, in real markup.
 *
 * Values are illustrative and labelled as such below the fold, but the SHAPE is
 * exactly what the app renders: a band with the points behind it, an unscored
 * factor named rather than hidden, and liveness with its own timestamp.
 */
function SpecimenCard() {
  return (
    <div
      className="rise rounded-[18px] border border-line bg-surface p-1.5 shadow-[var(--shadow-float)]"
      style={{ animationDelay: '90ms' }}
    >
      <div className="rounded-[13px] border border-line bg-ground p-4">
        <div className="flex items-start justify-between gap-3">
          <div className="min-w-0">
            <p className="text-base font-semibold leading-snug">
              Senior Backend Engineer, Platform
            </p>
            <p className="mt-0.5 text-meta text-ink-3">
              <span className="font-medium text-ink-2">GitLab</span> · Remote (US, CA)
            </p>
          </div>
          <span className="shrink-0 rounded-full border border-line bg-surface px-2 py-1 text-micro font-bold uppercase tracking-wider text-ink-3">
            Stretch
          </span>
        </div>

        <p className="mt-2.5 flex items-center gap-1.5 text-label font-medium">
          <span
            aria-hidden
            className="size-1.5 rounded-full bg-good shadow-[0_0_0_3px_var(--color-good-wash)]"
          />
          <span className="text-good">Verified open</span>
          <span className="font-normal text-ink-3">· checked 2 hours ago</span>
        </p>

        <p className="mt-2 text-meta text-ink-3">Salary not disclosed</p>

        <div className="mt-3.5 border-t border-line pt-3">
          <p className="text-label font-semibold uppercase tracking-[0.06em] text-ink-3">
            39 of a possible 90 points
          </p>
          <ul className="mt-2 flex flex-col gap-1.5">
            <Ledger points="+15" of="15" label="seniority" full />
            <Ledger points="+12" of="35" label="required skills — 1 of 3" />
            <Ledger points="+10" of="10" label="domain" full />
            <Ledger points="+3" of="20" label="overall role similarity" />
            <Ledger unscored label="compensation — employer did not disclose pay" />
          </ul>
        </div>
      </div>

      <p className="px-3 py-2.5 text-label leading-relaxed text-ink-3">
        Illustrative values, real layout. There is no percentage anywhere, because we have not
        calibrated one — a bare number would imply a probability we cannot support.
      </p>
    </div>
  );
}

function Ledger({
  points,
  of,
  label,
  full,
  unscored,
}: {
  points?: string;
  of?: string;
  label: string;
  full?: boolean;
  unscored?: boolean;
}) {
  return (
    <li className="flex items-baseline gap-2 text-meta">
      <span
        className={cn(
          'num w-[72px] shrink-0 font-mono text-label',
          unscored ? 'italic text-ink-3' : full ? 'font-semibold text-good' : 'text-ink-2',
        )}
      >
        {unscored ? 'not scored' : `${points} of ${of}`}
      </span>
      <span className={unscored ? 'italic text-ink-3' : 'text-ink-2'}>{label}</span>
    </li>
  );
}

const QUESTIONS = [
  {
    n: '01',
    q: 'What should I apply to?',
    a: 'A short daily list, ranked. Seven roles, not seven hundred — the number is the product promise, and it is what Precision@7 is measured against.',
    state: 'built' as const,
  },
  {
    n: '02',
    q: 'Why this one?',
    a: 'The per-factor arithmetic, shown. Fit is a weighted sum over bounded factors, so the breakdown IS the model rather than a story told about it afterwards.',
    state: 'built' as const,
  },
  {
    n: '03',
    q: 'What am I missing?',
    a: 'Every role the eligibility gate excluded, with the specific reason."Why am I not seeing X" has an answer you can read.',
    state: 'built' as const,
  },
  {
    n: '04',
    q: 'What should I learn to be more competitive?',
    a: 'Demand by skill, region and work mode, and how it moves. This needs longitudinal data that cannot be backfilled, so collection has started and the surface has not.',
    state: 'collecting' as const,
  },
];

function Questions() {
  return (
    <section className="border-y border-line bg-ground-2/40">
      <Container className="py-16 sm:py-20">
        <h2 className="max-w-[30ch] text-hero font-bold leading-tight tracking-[-0.025em] sm:text-hero">
          Four questions, in order.
        </h2>
        <p className="mt-3 max-w-[62ch] text-base leading-relaxed text-ink-2">
          The fourth is the one that matters most and the only one nobody can build quickly — it
          needs months of observed demand. The numbering is a dependency order, not a menu.
        </p>

        <div className="mt-10 grid gap-4 sm:grid-cols-2">
          {QUESTIONS.map((item) => (
            <article
              key={item.n}
              className={cn(
                'group rounded-[14px] border bg-surface p-5',
                'transition-all duration-[var(--dur-base)] ease-[var(--ease-out-quart)]',
                'hover:-translate-y-0.5 hover:shadow-[var(--shadow-raise)]',
                item.state === 'collecting' ? 'border-warn/25' : 'border-line',
              )}
            >
              <div className="flex items-center justify-between gap-3">
                <span className="num font-mono text-label font-semibold text-ink-3">
                  {item.n}
                </span>
                <span
                  className={cn(
                    'rounded-full px-2 py-0.5 text-micro font-bold uppercase tracking-wider',
                    item.state === 'built'
                      ? 'bg-good-wash text-good'
                      : 'bg-warn-wash text-warn',
                  )}
                >
                  {item.state === 'built' ? 'Built' : 'Collecting data'}
                </span>
              </div>
              <h3 className="mt-3 text-lead font-semibold leading-snug tracking-[-0.015em]">
                {item.q}
              </h3>
              <p className="mt-2 text-body leading-relaxed text-ink-2">{item.a}</p>
            </article>
          ))}
        </div>
      </Container>
    </section>
  );
}

const RULES = [
  {
    t: 'A missing field beats an invented one',
    d: 'When an employer does not disclose pay, it says"Salary not disclosed". It never shows an estimate dressed as the employer\'s number.',
  },
  {
    t: 'No score depends on the clock',
    d: 'A role\'s rating is a pure function of your profile and the posting. Freshness orders the list; it never inflates the match.',
  },
  {
    t: 'Unreadable is not zero',
    d: 'If we could not extract a posting\'s requirements, the factor is excluded from the total rather than scored as a miss — and the band says"Not enough information".',
  },
  {
    t: 'Closure needs evidence',
    d: 'A role is marked closed only after a successful poll in which it was absent. A failed fetch or a source outage never removes anything.',
  },
];

function Honesty() {
  return (
    <Container as="section" className="py-16 sm:py-20">
      <div className="grid gap-10 lg:grid-cols-[0.85fr_1fr] lg:gap-14">
        <div>
          <h2 className="text-hero font-bold leading-tight tracking-[-0.025em] sm:text-hero">
            Trust is lost by one invented field.
          </h2>
          <p className="mt-3 max-w-[46ch] text-base leading-relaxed text-ink-2">
            Not by a missing one. That single sentence decides most of the design, and these are
            the rules it produced. They are enforced in the code, with tests, not stated as
            values.
          </p>
        </div>

        <ul className="flex flex-col gap-3">
          {RULES.map((r) => (
            <li key={r.t} className="flex gap-3.5 rounded-[13px] border border-line bg-surface p-4">
              <svg
                viewBox="0 0 24 24"
                fill="none"
                stroke="currentColor"
                strokeWidth="2.3"
                strokeLinecap="round"
                strokeLinejoin="round"
                aria-hidden
                className="mt-0.5 size-4 shrink-0 text-good"
              >
                <path d="m4 12.5 5 5L20 6.5" />
              </svg>
              <div>
                <h3 className="text-body font-semibold">{r.t}</h3>
                <p className="mt-1 text-meta leading-relaxed text-ink-2">{r.d}</p>
              </div>
            </li>
          ))}
        </ul>
      </div>
    </Container>
  );
}

function Closing() {
  return (
    <section className="border-t border-line bg-ground-2/40">
      <Container className="py-16 text-center sm:py-20">
        <h2 className="mx-auto max-w-[26ch] text-hero font-bold leading-tight tracking-[-0.025em] sm:text-hero">
          See what it knows, and what it does not.
        </h2>
        <p className="mx-auto mt-3 max-w-[58ch] text-base leading-relaxed text-ink-2">
          The console shows its own gaps: which sources are healthy, which objectives cannot be
          measured yet, and why a role was excluded from your feed.
        </p>
        <Link
          to="/register"
          className={cn(
            'mt-8 inline-flex h-11 items-center rounded-[11px] bg-brand px-6 text-base',
            'font-semibold text-white shadow-[0_6px_18px_-6px_var(--color-brand-edge)]',
            'transition-all duration-[var(--dur-base)] ease-[var(--ease-out-quart)]',
            'hover:-translate-y-0.5 hover:brightness-110',
          )}
        >
          Create an account
        </Link>
      </Container>
    </section>
  );
}

function Footer() {
  return (
    <footer className="border-t border-line">
      <Container className="flex flex-col gap-3 py-8 sm:flex-row sm:items-center">
        <div className="flex items-center gap-2.5">
          <Mark size={22} />
          <span className="text-body font-semibold">DevSignal</span>
        </div>
        <p className="text-meta leading-relaxed text-ink-3 sm:ml-auto sm:text-right">
          Early software over a small corpus. We would rather show you a short honest list than a
          long confident one.
        </p>
      </Container>
    </footer>
  );
}
