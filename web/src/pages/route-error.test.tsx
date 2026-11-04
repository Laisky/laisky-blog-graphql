import { afterEach, expect, it, vi } from 'vitest';
import { cleanup, render, screen, waitFor } from '@testing-library/react';
import { createMemoryRouter, RouterProvider } from 'react-router-dom';
import { StrictMode } from 'react';
import { browserReporter } from '@/lib/browser-sentry';
import { RouteError } from './route-error';

afterEach(() => { cleanup(); vi.restoreAllMocks(); });
it('captures a router loader failure and preserves the existing NotFound fallback', async () => {
  const error = new Error('private tool result'); const capture = vi.spyOn(browserReporter, 'capture').mockImplementation(() => {});
  const router = createMemoryRouter([{ path: '/', loader: () => { throw error; }, errorElement: <RouteError />, element: <p>Overview</p> }]);
  render(<StrictMode><RouterProvider router={router} /></StrictMode>);
  await screen.findByText('Page not found'); expect(screen.getByText('Back to overview')).toBeTruthy();
  await waitFor(() => expect(capture).toHaveBeenCalledWith(error, 'router'));
});
