import { useEffect, useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { profileApi } from '@/lib/api/profile';
import { qk } from '@/lib/queryKeys';
import type { Profile, ProfileInput } from '@/lib/api/types';
import { Card } from '@/components/ui/Card';
import { Pill } from '@/components/ui/Pill';
import { Button } from '@/components/ui/Button';
import { Field, Input, Select, Toggle } from '@/components/ui/Field';
import { ErrorState, SkeletonCards } from '@/components/ui/States';
import { useToast } from '@/components/ui/Toast';
import { SkillEditor } from './SkillEditor';
import { ResumePanel } from './ResumePanel';
import { DangerZone } from './DangerZone';
import { PageHeader } from '@/components/ui/PageHeader';

const SENIORITY = ['intern', 'junior', 'mid', 'senior', 'staff', 'principal'];
const FAMILIES = [
  'backend', 'frontend', 'fullstack', 'mobile', 'data', 'ml', 'platform',
  'security', 'qa', 'design', 'product', 'sales', 'support', 'engineering',
];
const WORK_MODES = ['remote', 'hybrid', 'onsite'];

/** Empty is "no constraint", which is how retrieval reads it too. */
const NO_CONSTRAINT = '';

export function ProfilePage() {
  const qc = useQueryClient();
  const toast = useToast();

  const profile = useQuery({ queryKey: qk.profile(), queryFn: () => profileApi.get() });
  const [form, setForm] = useState<ProfileInput | null>(null);
  const [skills, setSkills] = useState<{ name: string }[]>([]);
  const [unresolved, setUnresolved] = useState<string[]>([]);

  // Seeded from the server once, then owned by the form. Re-seeding on every
  // refetch would discard whatever the user was typing.
  useEffect(() => {
    if (profile.data && form === null) {
      setForm(toInput(profile.data));
      setSkills(profile.data.skills.map((s) => ({ name: s.name })));
    }
  }, [profile.data, form]);

  const save = useMutation({
    mutationFn: (body: ProfileInput) => profileApi.save(body),
    onSuccess: (p) => {
      setUnresolved(p.unresolved_skills ?? []);
      // A profile edit bumps profile_version, which invalidates every cached fit
      // score. The feed and the saved list both have to be dropped or the user
      // sees scores computed against the profile they just changed.
      void qc.invalidateQueries({ queryKey: qk.profile() });
      void qc.invalidateQueries({ queryKey: ['feed'] });
      void qc.invalidateQueries({ queryKey: qk.saved() });
      const n = p.unresolved_skills?.length ?? 0;
      toast(
        n > 0
          ? `Saved. ${n} skill${n === 1 ? '' : 's'} could not be recognised.`
          : 'Profile saved. Fit scores will be recomputed.',
        n > 0 ? 'bad' : 'ok',
      );
    },
    onError: () => toast('Could not save the profile', 'bad'),
  });

  if (profile.isPending) return <SkeletonCards count={2} height="h-[280px]" />;
  if (profile.isError)
    return <ErrorState error={profile.error} onRetry={() => void profile.refetch()} />;
  if (!form) return null;

  const set = <K extends keyof ProfileInput>(k: K, v: ProfileInput[K]) =>
    setForm({ ...form, [k]: v });

  return (
    <div className="flex flex-col gap-5">
      <PageHeader
        title="Profile"
        subtitle="What matching reads. Every field here is an input to the eligibility gate or the fit score."
        aside={
          <Pill tone="neutral">
            <span className="num">v{profile.data.profile_version}</span>
          </Pill>
        }
      />

      <form
        onSubmit={(e) => {
          e.preventDefault();
          save.mutate({ ...form, skills: skills.map((s) => ({ ...s, proficiency: null, years: null })) });
        }}
        className="flex flex-col gap-5"
      >
        <Card className="flex flex-col gap-5">
          <h2 className="text-[15px] font-semibold">You</h2>

          <Field label="Headline" htmlFor="headline"
            hint="Free text. It is embedded into your profile vector, so it affects the semantic factor.">
            <Input
              id="headline"
              value={form.headline ?? ''}
              onChange={(e) => set('headline', e.target.value || null)}
              placeholder="Senior backend engineer, Go and PostgreSQL"
            />
          </Field>

          <div className="grid gap-4 sm:grid-cols-3">
            <Field label="Seniority" htmlFor="seniority">
              <Select
                id="seniority"
                value={form.seniority ?? NO_CONSTRAINT}
                onChange={(e) => set('seniority', e.target.value || null)}
              >
                <option value={NO_CONSTRAINT}>Not stated</option>
                {SENIORITY.map((s) => (
                  <option key={s} value={s}>{s}</option>
                ))}
              </Select>
            </Field>

            <Field label="Years of experience" htmlFor="years">
              <Input
                id="years"
                type="number"
                min={0}
                max={70}
                value={form.years_experience ?? ''}
                onChange={(e) =>
                  set('years_experience', e.target.value === '' ? null : Number(e.target.value))
                }
              />
            </Field>

            <Field label="Work mode" htmlFor="workmode">
              <Select
                id="workmode"
                value={form.work_mode_preference ?? NO_CONSTRAINT}
                onChange={(e) => set('work_mode_preference', e.target.value || null)}
              >
                <option value={NO_CONSTRAINT}>No preference</option>
                {WORK_MODES.map((m) => (
                  <option key={m} value={m}>{m}</option>
                ))}
              </Select>
            </Field>
          </div>

          <Toggle
            checked={form.is_management}
            onChange={(v) => set('is_management', v)}
            label="I am looking for people-leadership roles"
            hint="The seniority factor is track-aware: an IC staff role and an engineering manager role are not interchangeable, and this is what tells them apart."
          />
        </Card>

        <Card className="flex flex-col gap-5">
          <div>
            <h2 className="text-[15px] font-semibold">What you are looking for</h2>
            <p className="mt-1 text-[12.5px] leading-relaxed text-ink-3">
              These feed the eligibility gate, which is a boolean stage — a role that fails one is
              excluded and explained, never scored down. Leaving a field empty means no constraint.
            </p>
          </div>

          <Field
            label="Role families"
            hint="Retrieval's keyword channel and the domain factor both read this."
          >
            <ChipPicker
              options={FAMILIES}
              selected={form.target_role_families}
              onChange={(v) => set('target_role_families', v)}
            />
          </Field>

          <div className="grid gap-4 sm:grid-cols-2">
            <Field
              label="Countries"
              htmlFor="countries"
              hint="ISO-2 codes, comma separated. A remote role whose hiring region includes one of these also passes."
            >
              <Input
                id="countries"
                value={form.target_countries.join(', ')}
                onChange={(e) => set('target_countries', splitList(e.target.value))}
                placeholder="GB, US, DE"
              />
            </Field>

            <Field
              label="Languages"
              htmlFor="languages"
              hint="A posting stating a language you do not list is excluded by the gate."
            >
              <Input
                id="languages"
                value={form.languages.join(', ')}
                onChange={(e) => set('languages', splitList(e.target.value))}
                placeholder="en, de"
              />
            </Field>
          </div>

          <div className="grid gap-4 sm:grid-cols-3">
            <Field
              label="Minimum salary"
              htmlFor="salary"
              hint="Major units. Stored as minor units; only compared against a disclosed range in a comparable currency."
            >
              <Input
                id="salary"
                type="number"
                min={0}
                value={form.min_salary_minor == null ? '' : form.min_salary_minor / 100}
                onChange={(e) =>
                  set(
                    'min_salary_minor',
                    e.target.value === '' ? null : Math.round(Number(e.target.value) * 100),
                  )
                }
                placeholder="90000"
              />
            </Field>
            <Field label="Currency" htmlFor="currency">
              <Select
                id="currency"
                value={form.salary_currency ?? 'USD'}
                onChange={(e) => set('salary_currency', e.target.value)}
              >
                {['USD', 'EUR', 'GBP', 'CAD', 'AUD', 'INR'].map((c) => (
                  <option key={c} value={c}>{c}</option>
                ))}
              </Select>
            </Field>
            <Field label="Period" htmlFor="period">
              <Select
                id="period"
                value={form.salary_period ?? 'year'}
                onChange={(e) => set('salary_period', e.target.value)}
              >
                {['year', 'month', 'day', 'hour'].map((p) => (
                  <option key={p} value={p}>{p}</option>
                ))}
              </Select>
            </Field>
          </div>
        </Card>

        <Card className="flex flex-col gap-4">
          <div>
            <h2 className="text-[15px] font-semibold">Skills</h2>
            <p className="mt-1 text-[12.5px] leading-relaxed text-ink-3">
              Matched against a posting&apos;s extracted skills through a shared ontology, so
              &ldquo;Go&rdquo; here and &ldquo;Golang&rdquo; on a posting reach the same skill.
              Together the required- and preferred-skill factors are 45 of the model&apos;s
              100 points.
            </p>
          </div>
          <SkillEditor skills={skills} unresolved={unresolved} onChange={setSkills} />
        </Card>

        <div className="sticky bottom-4 z-30 flex items-center justify-end gap-3 rounded-[14px] border border-line bg-glass px-4 py-3 glass">
          <p className="mr-auto text-[12px] text-ink-3">
            Saving bumps your profile version and recomputes every fit score.
          </p>
          <Button
            type="submit"
            variant="primary"
            disabled={save.isPending}
            className="h-9"
          >
            {save.isPending ? 'Saving…' : 'Save profile'}
          </Button>
        </div>
      </form>

      <ResumePanel />
      <DangerZone />
    </div>
  );
}

function ChipPicker({
  options,
  selected,
  onChange,
}: {
  options: string[];
  selected: string[];
  onChange: (v: string[]) => void;
}) {
  return (
    <div className="flex flex-wrap gap-1.5">
      {options.map((o) => {
        const on = selected.includes(o);
        return (
          <button
            key={o}
            type="button"
            aria-pressed={on}
            onClick={() => onChange(on ? selected.filter((x) => x !== o) : [...selected, o])}
            className={
              'cursor-pointer rounded-full border px-2.5 py-1 text-[12px] font-medium ' +
              'transition-all duration-[var(--dur-base)] ease-[var(--ease-out-quart)] ' +
              (on
                ? 'border-brand-edge bg-brand-wash text-brand-ink'
                : 'border-line bg-raised text-ink-3 hover:border-line-strong hover:text-ink-2')
            }
          >
            {o}
          </button>
        );
      })}
    </div>
  );
}

function splitList(v: string): string[] {
  return v
    .split(',')
    .map((s) => s.trim())
    .filter(Boolean);
}

function toInput(p: Profile): ProfileInput {
  return {
    headline: p.headline,
    years_experience: p.years_experience,
    seniority: p.seniority,
    is_management: p.is_management,
    target_role_families: p.target_role_families ?? [],
    target_countries: p.target_countries ?? [],
    work_mode_preference: p.work_mode_preference,
    target_employment_types: p.target_employment_types ?? [],
    languages: p.languages ?? [],
    min_salary_minor: p.min_salary?.min_minor ?? null,
    salary_currency: p.min_salary?.currency ?? null,
    salary_period: p.min_salary?.period ?? null,
    work_authorization: p.work_authorization,
  };
}
