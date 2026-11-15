import { afterEach, beforeEach, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { createMemoryRouter, RouterProvider } from 'react-router-dom';
import { StrictMode } from 'react';
import { browserReporter } from '@/lib/browser-sentry';
import { RouteError } from './route-error';

// Route the real reload guard to a spy: jsdom cannot navigate.
const reload = vi.hoisted(() => vi.fn());
vi.mock('@/lib/route-modules', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/lib/route-modules')>();
  return {
    ...actual,
    reloadPage: reload,
    reloadForStaleChunk: (options: Parameters<typeof actual.reloadForStaleChunk>[0] = {}) => actual.reloadForStaleChunk({ ...options, reload }),
  };
});

const staleChunk = () => new TypeError('Failed to fetch dynamically imported module: https://mcp.laisky.com/assets/page-old.js');

/** renderFailingLazyRoute mounts a router whose only page fails to download its chunk. */
function renderFailingLazyRoute(error: unknown) {
  const router = createMemoryRouter([{ path: '/', lazy: async () => { throw error; }, errorElement: <RouteError /> }]);
  render(<StrictMode><RouterProvider router={router} /></StrictMode>);
}

beforeEach(() => { reload.mockClear(); window.sessionStorage.clear(); });
afterEach(() => { cleanup(); vi.restoreAllMocks(); window.sessionStorage.clear(); });

it('captures a router loader failure and preserves the existing NotFound fallback', async () => {
  const error = new Error('private tool result'); const capture = vi.spyOn(browserReporter, 'capture').mockImplementation(() => {});
  const router = createMemoryRouter([{ path: '/', loader: () => { throw error; }, errorElement: <RouteError />, element: <p>Overview</p> }]);
  render(<StrictMode><RouterProvider router={router} /></StrictMode>);
  await screen.findByText('Page not found'); expect(screen.getByText('Back to overview')).toBeTruthy();
  await waitFor(() => expect(capture).toHaveBeenCalledWith(error, 'router'));
});

it('reloads once instead of reporting when a code-split page chunk is gone after a deploy', async () => {
  const capture = vi.spyOn(browserReporter, 'capture').mockImplementation(() => {});
  renderFailingLazyRoute(staleChunk());
  await screen.findByText('Loading the latest version of this page…');
  expect(window.sessionStorage.getItem('route-chunk-reload-at')).not.toBeNull();
  expect(reload).toHaveBeenCalledTimes(1);
  expect(capture).not.toHaveBeenCalled();
});

it('offers an explicit reload and reports when the reload guard already fired', async () => {
  const capture = vi.spyOn(browserReporter, 'capture').mockImplementation(() => {});
  window.sessionStorage.setItem('route-chunk-reload-at', String(Date.now()));
  const stale = staleChunk();
  renderFailingLazyRoute(stale);
  await screen.findByRole('heading', { name: 'This page could not be loaded' });
  expect(screen.queryByText('Page not found')).toBeNull();
  await waitFor(() => expect(capture).toHaveBeenCalledWith(stale, 'router'));
  expect(reload).not.toHaveBeenCalled();

  fireEvent.click(screen.getByRole('button', { name: 'Reload page' }));
  expect(reload).toHaveBeenCalledTimes(1);
});

it('renders the recovery page instead of crashing when session storage cannot be accessed', async () => {
  const capture = vi.spyOn(browserReporter, 'capture').mockImplementation(() => {});
  vi.spyOn(window, 'sessionStorage', 'get').mockImplementation(() => {
    throw new DOMException('The operation is insecure.', 'SecurityError');
  });
  const stale = staleChunk();
  renderFailingLazyRoute(stale);
  await screen.findByRole('heading', { name: 'This page could not be loaded' });
  expect(reload).not.toHaveBeenCalled();
  await waitFor(() => expect(capture).toHaveBeenCalledWith(stale, 'router'));
});

it('does not reload or report while offline, and explains how to recover', async () => {
  const capture = vi.spyOn(browserReporter, 'capture').mockImplementation(() => {});
  vi.spyOn(navigator, 'onLine', 'get').mockReturnValue(false);
  renderFailingLazyRoute(staleChunk());
  await screen.findByRole('heading', { name: 'This page could not be loaded' });
  expect(screen.getByText(/appear to be offline/i)).toBeTruthy();
  expect(reload).not.toHaveBeenCalled();
  expect(capture).not.toHaveBeenCalled();
  expect(window.sessionStorage.getItem('route-chunk-reload-at')).toBeNull();
});
