import { createBrowserRouter, Navigate } from 'react-router-dom';
import { AppShell } from '@/features/shell/AppShell';
import { RequireAuth, RequireAdmin } from '@/features/auth/RequireAuth';
import { AuthPage } from '@/features/auth/AuthPage';
import { LandingPage } from '@/features/landing/LandingPage';
import { OverviewPage } from '@/features/overview/OverviewPage';
import { SourcesPage } from '@/features/sources/SourcesPage';
import { FeedPage } from '@/features/feed/FeedPage';
import { FlagsPage } from '@/features/flags/FlagsPage';
import { ProfilePage } from '@/features/profile/ProfilePage';
import { BrowsePage } from '@/features/browse/BrowsePage';
import { OpportunityPage } from '@/features/browse/OpportunityPage';
import { SavedPage } from '@/features/saved/SavedPage';
import { SettingsPage } from '@/features/settings/SettingsPage';
import { MergeQueuePage } from '@/features/admin/MergeQueuePage';
import { NotFound } from '@/components/ui/States';

/**
 * Public at `/`, the console under `/app`.
 *
 * Separated rather than switching on the session at `/`, because a deep link has
 * to mean one thing: `/app/browse/:id` is always the console and `/` is always
 * the public page, whoever is looking. The alternative — root renders the
 * landing page when signed out and the overview when signed in — makes every
 * shared URL ambiguous.
 *
 * The admin routes sit behind their own element rather than being filtered out
 * of the list, so a bookmarked URL gets a real answer instead of falling through
 * to the catch-all.
 */
export const router = createBrowserRouter([
  { path: '/', element: <LandingPage /> },
  { path: '/login', element: <AuthPage mode="login" /> },
  { path: '/register', element: <AuthPage mode="register" /> },

  {
    element: <RequireAuth />,
    children: [
      {
        path: '/app',
        element: <AppShell />,
        children: [
          /* The product home is the feed, for every role. The overview is an
             operations screen — it reads the SLO report, which is admin-gated —
             so making it the landing screen would greet a plain user with a
             surface they cannot load. */
          { index: true, element: <Navigate to="/app/feed" replace /> },
          { path: 'feed', element: <FeedPage /> },
          { path: 'saved', element: <SavedPage /> },
          { path: 'browse', element: <BrowsePage /> },
          { path: 'browse/:id', element: <OpportunityPage /> },
          { path: 'profile', element: <ProfilePage /> },
          { path: 'settings', element: <SettingsPage /> },

          {
            element: <RequireAdmin />,
            children: [
              { path: 'overview', element: <OverviewPage /> },
              { path: 'sources', element: <SourcesPage /> },
              { path: 'merges', element: <MergeQueuePage /> },
              { path: 'flags', element: <FlagsPage /> },
            ],
          },

          { path: '*', element: <NotFound /> },
        ],
      },
    ],
  },

  { path: '*', element: <NotFound /> },
]);
