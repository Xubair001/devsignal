import { test, expect, type Page } from '@playwright/test';

/**
 * The one responsive failure that matters.
 *
 * A page whose BODY scrolls sideways is broken on every phone, and it is always
 * caused by one element — a table without an overflow wrapper, a fixed width, a
 * long unbroken string. Checking scrollWidth against clientWidth catches all of
 * them at once, and reporting the offending element makes the failure actionable
 * rather than a puzzle.
 */
async function expectNoHorizontalOverflow(page: Page, where: string) {
  const overflow = await page.evaluate(() => {
    const doc = document.documentElement;
    // 1px of slack: sub-pixel layout rounding is not a bug.
    if (doc.scrollWidth <= doc.clientWidth + 1) return null;

    // Name the widest offender so the failure says what to fix.
    let worst: { tag: string; cls: string; right: number } | null = null;
    for (const el of Array.from(document.body.querySelectorAll<HTMLElement>('*'))) {
      const r = el.getBoundingClientRect();
      if (r.width === 0 || r.height === 0) continue;
      if (r.right > doc.clientWidth + 1 && (!worst || r.right > worst.right)) {
        worst = {
          tag: el.tagName.toLowerCase(),
          cls: (el.className || '').toString().slice(0, 120),
          right: Math.round(r.right),
        };
      }
    }
    return { scrollWidth: doc.scrollWidth, clientWidth: doc.clientWidth, worst };
  });

  expect(
    overflow,
    `${where} scrolls horizontally: ${JSON.stringify(overflow)}`,
  ).toBeNull();
}

/** Public pages need no session. */
const PUBLIC = ['/', '/login', '/register'];

test.describe('public pages', () => {
  for (const path of PUBLIC) {
    test(`${path} does not scroll sideways`, async ({ page }) => {
      await page.goto(path);
      await page.waitForLoadState('networkidle');
      await expectNoHorizontalOverflow(page, path);
    });
  }

  test('the landing page fills the viewport it is given', async ({ page }, info) => {
    await page.goto('/');
    await page.waitForLoadState('networkidle');

    const width = page.viewportSize()?.width ?? 0;
    /* The CONTAINER, not a heading: a heading sits in whichever grid column it
       was placed in, so measuring it would assert about the layout inside the
       container rather than about the container's use of the viewport. */
    const box = await page.locator('[data-container]').first().boundingBox();
    expect(box).not.toBeNull();

    // The gutter is the empty space either side of the content. A container that
    // is too narrow for a wide screen reads as a column floating in a field of
    // nothing, which is exactly the complaint this asserts against.
    const gutter = box!.x;
    const share = (box!.width + gutter * 2) / width;
    info.annotations.push({
      type: 'layout',
      description: `viewport ${width}px, content ${Math.round(box!.width)}px, gutter ${Math.round(gutter)}px`,
    });

    if (width >= 1280) {
      expect(share, 'content plus gutters should use most of a wide viewport').toBeGreaterThan(0.82);
      expect(gutter, 'gutter on a wide screen should not exceed a quarter of it')
        .toBeLessThan(width * 0.25);
    } else {
      expect(gutter, 'gutter on a small screen should be a margin, not a column')
        .toBeLessThan(48);
    }
  });

  test('every interactive control is big enough to tap', async ({ page }) => {
    await page.goto('/');
    await page.waitForLoadState('networkidle');

    const small = await page.evaluate(() => {
      const out: string[] = [];
      for (const el of Array.from(
        document.querySelectorAll<HTMLElement>('a, button, [role="button"], input, select'),
      )) {
        const r = el.getBoundingClientRect();
        if (r.width === 0 || r.height === 0) continue;
        // 32px is below the 44px ideal but is the practical floor for a dense
        // console; anything under it is a genuine miss-tap risk.
        if (r.height < 32) {
          out.push(`${el.tagName.toLowerCase()} "${(el.textContent || '').trim().slice(0, 30)}" h=${Math.round(r.height)}`);
        }
      }
      return out;
    });
    expect(small, `controls under 32px tall: ${small.join('; ')}`).toEqual([]);
  });

  test('the theme toggle switches and the page stays readable', async ({ page }) => {
    await page.goto('/');
    const toggle = page.locator('[data-theme-toggle]');
    await expect(toggle).toBeVisible();

    const before = await page.evaluate(() =>
      getComputedStyle(document.body).backgroundColor,
    );
    await toggle.click();
    await page.waitForTimeout(350);
    const after = await page.evaluate(() =>
      getComputedStyle(document.body).backgroundColor,
    );
    expect(after, 'the toggle did not change the background').not.toBe(before);

    // The classic unreadable-theme bug: text and ground from different palettes.
    const contrast = await page.evaluate(() => {
      const lum = (c: string) => {
        const [r, g, b] = (c.match(/\d+/g) ?? ['0', '0', '0']).map(Number);
        const f = (v: number) => {
          const s = v / 255;
          return s <= 0.03928 ? s / 12.92 : ((s + 0.055) / 1.055) ** 2.4;
        };
        return 0.2126 * f(r!) + 0.7152 * f(g!) + 0.0722 * f(b!);
      };
      const s = getComputedStyle(document.body);
      const a = lum(s.color);
      const b = lum(s.backgroundColor);
      const [hi, lo] = a > b ? [a, b] : [b, a];
      return (hi + 0.05) / (lo + 0.05);
    });
    expect(contrast, 'body text on body background is below 4.5:1').toBeGreaterThan(4.5);
  });
});
