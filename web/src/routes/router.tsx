import { createBrowserRouter } from 'react-router-dom';
import { AppShell } from '@/features/shell/AppShell';
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

export const router = createBrowserRouter([
  {
    path: '/',
    element: <AppShell />,
    children: [
      { index: true, element: <OverviewPage /> },
      { path: 'feed', element: <FeedPage /> },
      { path: 'saved', element: <SavedPage /> },
      { path: 'browse', element: <BrowsePage /> },
      { path: 'browse/:id', element: <OpportunityPage /> },
      { path: 'profile', element: <ProfilePage /> },
      { path: 'settings', element: <SettingsPage /> },
      { path: 'sources', element: <SourcesPage /> },
      { path: 'flags', element: <FlagsPage /> },
      { path: 'merges', element: <MergeQueuePage /> },
      { path: '*', element: <NotFound /> },
    ],
  },
]);
