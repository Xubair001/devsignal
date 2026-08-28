import { useQuery } from '@tanstack/react-query';
import { Link } from 'react-router-dom';
import { feedApi } from '@/lib/api/feed';
import { qk } from '@/lib/queryKeys';
import type { SkillGap } from '@/lib/api/types';
import { Card } from '@/components/ui/Card';
import { Pill } from '@/components/ui/Pill';
import { Button } from '@/components/ui/Button';
import { ErrorState, SkeletonCards } from '@/components/ui/States';
import { PageHeader } from '@/components/ui/PageHeader';
import { cn } from '@/components/ui/cn';

/**
 * Question four of the product's four: what should I learn.
 *
 * The single most important thing on this screen is what it does NOT show. There
 * is no "learning this raises your chances by N%", no readiness score, no
 * ranking of the user against anyone. We have no applicant counts, so any such
 * figure would be invented — and one invented number discredits the honest ones
 * beside it (blueprint §3).
 *
 * What it shows instead is arithmetic: of the roles you can actually take, this
 * many list this skill as required. The bar lengths are relative to the largest
 * count on the page, which is a reading aid for the same number, not a second
 * claim.
 */
export function GapsPage() {
  const gaps = useQuery({ queryKey: qk.gaps(), queryFn: () => feedApi.gaps() });

  if (gaps.isPending) return <SkeletonCards count={2} height="h-[220px]" />;
  if (gaps.isError) return <ErrorState error={gaps.error} onRetry={() => void gaps.refetch()} />;

  const d = gaps.data;
  const coverage = d.eligible > 0 ? d.with_skills / d.eligible : 0;
  const busiest = Math.max(1, ...d.gaps.map((g) => g.required_by + g.preferred_by));

  return (
    <div className="flex flex-col gap-4 sm:gap-5">
      <PageHeader
        title="What you are missing"
        subtitle="Across the roles you are eligible for, which skills they ask for that you have not listed. Counts of postings — not an estimate of your chances."
        aside={
          d.state === 'ready' ? (
            <Pill tone="neutral">
              <span className="num">{d.with_skills}</span> of {d.eligible} roles read
            </Pill>
          ) : undefined
        }
      />

      {d.state === 'stale' && (
        <Card className="flex flex-col items-start gap-3">
          <Pill tone="no_data">Nothing to analyse yet</Pill>
          <p className="max-w-[68ch] text-body leading-relaxed text-ink-2">
            The eligibility gate has not run since your profile last changed, so there is no
            eligible set to analyse. Results computed against an older profile describe a gate you
            no longer have, and using them would answer for who you were.
          </p>
          <Button as="a" variant="primary" href="/app/feed">
            Open your feed to refresh it
          </Button>
        </Card>
      )}

      {d.state === 'insufficient_extraction' && (
        <Card className="flex flex-col items-start gap-3">
          <Pill tone="no_data">Not enough information</Pill>
          <p className="max-w-[68ch] text-body leading-relaxed text-ink-2">
            Only <b className="font-semibold">{Math.round(coverage * 100)}%</b> of the{' '}
            {d.eligible} roles you are eligible for have had their requirements read. Below 60%
            this list would say more about which postings we failed to parse than about what the
            market wants — it would name whichever skills happened to be in the fraction we could
            read.
          </p>
          <p className="max-w-[68ch] text-meta leading-relaxed text-ink-3">
            This is the same bar the fit model uses before it will call something a Strong fit.
            Showing you a confident list built on a fifth of the corpus is the failure mode it
            exists to prevent.
          </p>
        </Card>
      )}

      {d.state === 'ready' && (
        <>
          {d.strengths.length > 0 && (
            <Card>
              <h2 className="text-lead font-semibold">What you already have</h2>
              <p className="mt-1 text-meta leading-relaxed text-ink-3">
                The same arithmetic run the other way. A gap list on its own reads as a deficit
                report; this is what makes it a position.
              </p>
              <ul className="mt-3.5 flex flex-wrap gap-1.5">
                {d.strengths.map((s) => (
                  <li key={s.name}>
                    <span className="inline-flex items-center gap-1.5 rounded-full border border-good/25 bg-good-wash px-2.5 py-1 text-meta font-medium text-good">
                      {s.name}
                      <span className="num text-micro opacity-80">
                        {s.required_by} role{s.required_by === 1 ? '' : 's'}
                      </span>
                    </span>
                  </li>
                ))}
              </ul>
            </Card>
          )}

          <Card>
            <h2 className="text-lead font-semibold">What they ask for that you have not listed</h2>
            <p className="mt-1 max-w-[70ch] text-meta leading-relaxed text-ink-3">
              Ordered by how many of your eligible roles list each as required. A skill half the
              market wants is useless advice if every posting needing it fails your location or
              work-authorization gate — so only roles you could actually take are counted.
            </p>

            {d.gaps.length === 0 ? (
              <p className="mt-4 text-body text-ink-2">
                No gaps: every skill your eligible roles require is one you have listed.
              </p>
            ) : (
              <ul className="mt-4 flex flex-col gap-2.5">
                {d.gaps.map((g) => (
                  <GapRow key={g.slug} gap={g} busiest={busiest} />
                ))}
              </ul>
            )}
          </Card>

          <Card className="bg-raised/40">
            <h2 className="text-body font-semibold">How to read this</h2>
            <ul className="mt-2 flex flex-col gap-1.5 text-meta leading-relaxed text-ink-2">
              <li>
                Every number is a <b className="font-semibold">count of postings</b>. There is no
                percentage here and no readiness score, because we have no applicant counts and any
                such figure would be invented.
              </li>
              <li>
                A gap is <b className="font-semibold">not a penalty</b>. The required-skills factor
                already scores what you match; counting it again here would be double-counting.
              </li>
              <li>
                <b className="font-semibold">Required</b> and <b className="font-semibold">preferred</b>{' '}
                are kept separate because the postings said so. Collapsing them would invent a
                weighting nobody stated.
              </li>
              {d.excluded_unknown_phrases > 0 && (
                <li>
                  {d.excluded_unknown_phrases} further phrases were left out for not being in our
                  skill vocabulary. A large number there means the vocabulary is behind — not that
                  you have few gaps.
                </li>
              )}
              <li>
                {d.eligible - d.with_skills > 0 ? (
                  <>
                    {d.eligible - d.with_skills} of your eligible roles have no extracted
                    requirements and contribute nothing above.
                  </>
                ) : (
                  <>Every eligible role had its requirements read, so nothing is missing from the
                    counts.</>
                )}
              </li>
            </ul>
            <Link
              to="/app/profile"
              className="mt-3.5 inline-flex text-meta font-medium text-brand underline decoration-brand/35 underline-offset-2 hover:decoration-brand"
            >
              Add a skill you already have to your profile
            </Link>
          </Card>
        </>
      )}
    </div>
  );
}

function GapRow({ gap, busiest }: { gap: SkillGap; busiest: number }) {
  const total = gap.required_by + gap.preferred_by;
  const reqShare = (gap.required_by / busiest) * 100;
  const prefShare = (gap.preferred_by / busiest) * 100;

  return (
    <li className="flex flex-wrap items-center gap-x-3 gap-y-1.5">
      <span className="w-[150px] shrink-0 truncate text-body font-medium">{gap.name}</span>

      {/* A reading aid for the counts beside it, scaled to the largest on the
          page — not a second claim. The numbers are the content and are always
          present in text, so the bar carries no information of its own. */}
      <span
        aria-hidden
        className="flex h-2 min-w-[80px] flex-1 overflow-hidden rounded-full bg-raised"
      >
        <span
          className={cn('h-full bg-brand', reqShare > 0 && 'min-w-[3px]')}
          style={{ width: `${reqShare}%` }}
        />
        <span
          className={cn('h-full bg-brand/30', prefShare > 0 && 'min-w-[3px]')}
          style={{ width: `${prefShare}%` }}
        />
      </span>

      <span className="num shrink-0 text-meta text-ink-2">
        <b className="font-semibold">{gap.required_by}</b> required
        {gap.preferred_by > 0 && (
          <span className="text-ink-3"> · {gap.preferred_by} preferred</span>
        )}
      </span>
      <span className="sr-only">
        {gap.name}: required by {gap.required_by} of your eligible roles, preferred by{' '}
        {gap.preferred_by}, {total} in total.
      </span>
    </li>
  );
}
