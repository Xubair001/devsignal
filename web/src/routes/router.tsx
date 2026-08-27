import { createBrowserRouter } from 'react-router-dom';
import { AppShell } from '@/features/shell/AppShell';
import { OverviewPage } from '@/features/overview/OverviewPage';
import { SourcesPage } from '@/features/sources/SourcesPage';
import { FeedPage } from '@/features/feed/FeedPage';
import { FlagsPage } from '@/features/flags/FlagsPage';

export const router = createBrowserRouter([
  {
    path: '/',
    element: <AppShell />,
    children: [
      { index: true, element: <OverviewPage /> },
      { path: 'sources', element: <SourcesPage /> },
      { path: 'feed', element: <FeedPage /> },
      { path: 'flags', element: <FlagsPage /> },
      {
        path: '*',
        element: (
          <p className="py-16 text-center text-[14px] text-ink-3">
            That page does not exist.
          </p>
        ),
      },
    ],
  },
]);
