import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { RouterProvider } from 'react-router-dom';
import { ApiError } from '@/lib/api/client';
import { ToastProvider } from '@/components/ui/Toast';
import { router } from '@/routes/router';
import './index.css';

const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      staleTime: 20_000,
      // Retrying a 401 or a 404 wastes three round trips to reach the same
      // answer, and the admin surface answers 404 for a non-admin on purpose.
      retry: (attempt, error) => {
        if (error instanceof ApiError && (error.unauthorized || error.notFound)) return false;
        return attempt < 2;
      },
      refetchOnWindowFocus: false,
    },
  },
});

const root = document.getElementById('root');
if (!root) throw new Error('#root is missing from index.html');

createRoot(root).render(
  <StrictMode>
    <QueryClientProvider client={queryClient}>
      <ToastProvider>
        <RouterProvider router={router} />
      </ToastProvider>
    </QueryClientProvider>
  </StrictMode>,
);
