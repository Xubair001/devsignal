import { test, expect, type Page } from '@playwright/test';
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

/**
 * The authenticated console, at every viewport.
 *
 * These are the pages most likely to break responsively — they carry tables,
 * multi-column grids and long identifiers — and they are exactly the pages a
 * public-page test cannot reach. The token comes from web/.env.local, the same
 * one the dev server injects, so this exercises the real API rather than a mock:
 * a responsive test against a skeleton screen proves nothing about the page with
 * data in it.
 */
/* import.meta.url, not __dirname: the package is ESM, so __dirname is undefined
   and the catch below swallowed the ReferenceError — which made every test in
   this file skip silently. A skipped suite that looks like a passing one is
   worse than a failing one. */
const here = path.dirname(fileURLToPath(import.meta.url));

function devToken(): string | null {
  const file = path.resolve(here, '../.env.local');
  if (!fs.existsSync(file)) return null;
  const env = fs.readFileSync(file, 'utf8');
  const line = env.split('\n').find((l) => l.startsWith('VITE_DEV_TOKEN='));
  const value = line ? line.slice('VITE_DEV_TOKEN='.length).trim() : '';
  return value === '' ? null : value;
}

const TOKEN = devToken();

/** Same assertion as the public suite, kept here so the failure names the page. */
async function expectNoHorizontalOverflow(page: Page, where: string) {
  const overflow = await page.evaluate(() => {
    const doc = document.documentElement;
    if (doc.scrollWidth <= doc.clientWidth + 1) return null;
    let worst: { tag: string; cls: string; right: number } | null = null;
    for (const el of Array.from(document.body.querySelectorAll<HTMLElement>('*'))) {
      const r = el.getBoundingClientRect();
      if (r.width === 0 || r.height === 0) continue;
      if (r.right > doc.clientWidth + 1 && (!worst || r.right > worst.right)) {
        worst = {
          tag: el.tagName.toLowerCase(),
          cls: (el.className || '').toString().slice(0, 140),
          right: Math.round(r.right),
        };
      }
    }
    return { scrollWidth: doc.scrollWidth, clientWidth: doc.clientWidth, worst };
  });
  expect(overflow, `${where} scrolls horizontally: ${JSON.stringify(overflow)}`).toBeNull();
}

const ROUTES = [
  '/app/feed',
  '/app/saved',
  '/app/gaps',
  '/app/browse',
  '/app/profile',
  '/app/settings',
  '/app/overview',
  '/app/sources',
  '/app/merges',
  '/app/flags',
];

test.describe('console', () => {
  test.skip(TOKEN === null, 'web/.env.local has no VITE_DEV_TOKEN');

  test.beforeEach(async ({ page }) => {
    // Seed the session before the app boots. Setting it after navigation would
    // let the first render happen unauthenticated and redirect to /login.
    await page.addInitScript(
      ([key, value]) => {
        window.localStorage.setItem(key, value);
      },
      ['ds-token', TOKEN!] as const,
    );
  });

  for (const route of ROUTES) {
    test(`${route} renders without sideways scroll`, async ({ page }) => {
      const failures: string[] = [];
      page.on('pageerror', (e) => failures.push(`pageerror: ${e.message}`));
      page.on('console', (m) => {
        if (m.type() === 'error') failures.push(`console: ${m.text()}`);
      });

      await page.goto(route);
      // Wait for content rather than a timer: a skeleton has no overflow.
      await page.waitForLoadState('networkidle');
      await expect(page.locator('main')).toBeVisible();

      await expectNoHorizontalOverflow(page, route);

      // A blank page passes an overflow check trivially, so assert it rendered.
      const text = (await page.locator('main').innerText()).trim();
      expect(text.length, `${route} rendered an empty main`).toBeGreaterThan(20);

      // React key warnings and unhandled errors are real defects, not noise.
      const real = failures.filter(
        (f) => !f.includes('404') && !f.includes('Failed to load resource'),
      );
      expect(real, `${route} logged errors: ${real.join(' | ')}`).toEqual([]);
    });
  }

  test('the sidebar collapses to a drawer below lg', async ({ page }) => {
    await page.goto('/app/feed');
    await page.waitForLoadState('networkidle');

    const width = page.viewportSize()?.width ?? 0;
    const sidebar = page.locator('aside').first();
    const burger = page.getByRole('button', { name: 'Open navigation' });

    if (width >= 1024) {
      await expect(sidebar).toBeVisible();
      await expect(burger).toBeHidden();
    } else {
      await expect(sidebar).toBeHidden();
      await expect(burger).toBeVisible();
      // And the drawer actually opens, with navigation inside it.
      await burger.click();
      await expect(page.getByRole('navigation', { name: 'Main' })).toBeVisible();
    }
  });
});

test.describe('the gap surface renders no forbidden claim', () => {
  test.skip(TOKEN === null, 'web/.env.local has no VITE_DEV_TOKEN');

  test.beforeEach(async ({ page }) => {
    await page.addInitScript(
      ([key, value]) => {
        window.localStorage.setItem(key, value);
      },
      ['ds-token', TOKEN!] as const,
    );
  });

  /**
   * The display rule as a test.
   *
   * This page answers "what should I learn", which is exactly where a
   * competitiveness estimate would feel natural and is exactly what blueprint §3
   * forbids: we have no applicant counts. A percentage on this screen would be an
   * invented probability, and the whole surface is arithmetic precisely to avoid
   * one.
   */
  test('no percentage, no readiness score, no probability language', async ({ page }) => {
    await page.goto('/app/gaps');
    await page.waitForLoadState('networkidle');

    const text = await page.locator('main').innerText();

    /* Negated sentences are dropped before matching.
       This page deliberately NAMES what it does not do — "no readiness score",
       "not an estimate of your chances" — and that copy is the point, not a
       violation. Two earlier versions of this test flagged the page's own
       disclaimers. An assertion inside a negation is a disclaimer; only what
       survives the filter is a claim. */
    const asserted = text
      .split(/(?<=[.!?])\s+|\n+/)
      .filter((sentence) => !/\b(no|not|never|without|nor|cannot)\b/i.test(sentence))
      .join(' ');

    /* Matches ASSERTIONS, not the words. An earlier version of this regex
       flagged the page's own disclaimer — "not an estimate of your chances" —
       which is exactly the copy we want. The forbidden thing is a quantified or
       asserted claim, so the patterns require a verb or a number. */
    const forbidden: [RegExp, string][] = [
      [/(increase|improve|boost|raise|lift)\w*\s+your\s+(chances?|odds)/i,
        'claims an action changes the reader\'s chances'],
      [/your\s+(chances?|odds)\s+(are|is|of|at)\b/i, 'asserts what the chances are'],
      [/\d+\s*%\s*(match|fit|chance|likely|readiness|competitive)/i,
        'attaches a percentage to a match or a chance'],
      [/\d+(\.\d+)?\s*%\s*more likely/i, 'quantifies a likelihood'],
      [/\breadiness score\b/i, 'invents a readiness score'],
      [/\bcompetitiveness (score|estimate|rating)\b/i, 'invents a competitiveness figure'],
      [/\b\d+(st|nd|rd|th) percentile\b/i, 'shows a percentile before calibration exists'],
    ];

    for (const [pattern, why] of forbidden) {
      expect(asserted, `the gap page ${why}: ${pattern}`).not.toMatch(pattern);
    }

    /* The more valuable half: the page must SAY what its numbers are. A surface
       that merely avoids the forbidden phrasing while leaving the reader to
       assume a probability has kept the letter and lost the point. */
    expect(
      text.toLowerCase(),
      'the page does not tell the reader the numbers are counts of postings',
    ).toContain('count');
    expect(
      text.toLowerCase(),
      'the page does not disclaim being an estimate of the reader\'s chances',
    ).toMatch(/not an estimate|no applicant counts/);
  });
});
