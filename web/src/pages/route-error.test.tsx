import { afterEach, expect, it, vi } from 'vitest';
import { cleanup, render, screen, waitFor } from '@testing-library/react';
import { createMemoryRouter, RouterProvider } from 'react-router-dom';
import { StrictMode } from 'react';
import { browserReporter } from '@/lib/browser-sentry';
import { RouteError } from './route-error';

// Route the real reload guard to a spy: jsdom cannot navigate.
const reload = vi.hoisted(() => vi.fn());
vi.mock('@/lib/route-modules', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/lib/route-modules')>();
  return { ...actual, reloadForStaleChunk: () => actual.reloadForStaleChunk(window.sessionStorage, Date.now(), reload) };
});

afterEach(() => { cleanup(); vi.restoreAllMocks(); });
it('captures a router loader failure and preserves the existing NotFound fallback', async () => {
  const error = new Error('private tool result'); const capture = vi.spyOn(browserReporter, 'capture').mockImplementation(() => {});
  const router = createMemoryRouter([{ path: '/', loader: () => { throw error; }, errorElement: <RouteError />, element: <p>Overview</p> }]);
  render(<StrictMode><RouterProvider router={router} /></StrictMode>);
  await screen.findByText('Page not found'); expect(screen.getByText('Back to overview')).toBeTruthy();
  await waitFor(() => expect(capture).toHaveBeenCalledWith(error, 'router'));
});

it('reloads once instead of reporting when a code-split page chunk is gone after a deploy', async () => {
  const capture = vi.spyOn(browserReporter, 'capture').mockImplementation(() => {});
  reload.mockClear();
  window.sessionStorage.clear();
  const stale = new TypeError('Failed to fetch dynamically imported module: https://mcp.laisky.com/assets/page-old.js');
  const router = createMemoryRouter([{ path: '/', lazy: async () => { throw stale; }, errorElement: <RouteError /> }]);
  render(<StrictMode><RouterProvider router={router} /></StrictMode>);
  await screen.findByText('Loading the latest version of this page…');
  expect(window.sessionStorage.getItem('route-chunk-reload-at')).not.toBeNull();
  expect(reload).toHaveBeenCalledTimes(1);
  expect(capture).not.toHaveBeenCalled();
});

it('falls back to the not-found page and reports when the reload guard already fired', async () => {
  const capture = vi.spyOn(browserReporter, 'capture').mockImplementation(() => {});
  reload.mockClear();
  window.sessionStorage.setItem('route-chunk-reload-at', String(Date.now()));
  const stale = new TypeError('Failed to fetch dynamically imported module: https://mcp.laisky.com/assets/page-old.js');
  const router = createMemoryRouter([{ path: '/', lazy: async () => { throw stale; }, errorElement: <RouteError /> }]);
  render(<StrictMode><RouterProvider router={router} /></StrictMode>);
  await screen.findByText('Page not found');
  await waitFor(() => expect(capture).toHaveBeenCalledWith(stale, 'router'));
  expect(reload).not.toHaveBeenCalled();
  window.sessionStorage.clear();
});
