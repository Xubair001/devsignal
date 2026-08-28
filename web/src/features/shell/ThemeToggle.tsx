import { IconButton } from '@/components/ui/Button';
import { useTheme } from './useTheme';

/** The two glyphs cross-fade and rotate rather than swapping instantly. */
export function ThemeToggle() {
  const { theme, toggle } = useTheme();
  const dark = theme === 'dark';

  return (
    <IconButton
      label={dark ? 'Switch to light theme' : 'Switch to dark theme'}
      onClick={toggle}
      /* The command palette's "Toggle theme" finds the button by this attribute.
         It was missing, so that command silently did nothing — caught by the
         responsive e2e test, which is the only thing that exercises it. */
      data-theme-toggle=""
      className="overflow-hidden"
    >
      <svg
        viewBox="0 0 24 24"
        fill="none"
        stroke="currentColor"
        strokeWidth="2"
        strokeLinecap="round"
        className={
          'absolute size-[17px] transition-[opacity,transform] duration-300 ease-out-quart ' +
          (dark ? 'rotate-[70deg] scale-60 opacity-0' : 'rotate-0 scale-100 opacity-100')
        }
      >
        <circle cx="12" cy="12" r="4.2" />
        <path d="M12 2.5v2M12 19.5v2M2.5 12h2M19.5 12h2M5.3 5.3l1.4 1.4M17.3 17.3l1.4 1.4M18.7 5.3l-1.4 1.4M6.7 17.3l-1.4 1.4" />
      </svg>
      <svg
        viewBox="0 0 24 24"
        fill="none"
        stroke="currentColor"
        strokeWidth="2"
        strokeLinecap="round"
        strokeLinejoin="round"
        className={
          'absolute size-[17px] transition-[opacity,transform] duration-300 ease-out-quart ' +
          (dark ? 'rotate-0 scale-100 opacity-100' : '-rotate-[70deg] scale-60 opacity-0')
        }
      >
        <path d="M21 12.8A8.5 8.5 0 1 1 11.2 3a6.6 6.6 0 0 0 9.8 9.8Z" />
      </svg>
    </IconButton>
  );
}
