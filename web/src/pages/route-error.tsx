import { useEffect } from 'react';
import { useRouteError } from 'react-router-dom';

import { browserReporter } from '@/lib/browser-sentry';
import { NotFoundPage } from '@/pages/not-found';

/** RouteError reports router-caught failures while retaining the existing fallback UI. */
export function RouteError() {
  const error = useRouteError();
  useEffect(() => { browserReporter.capture(error, 'router'); }, [error]);
  return <NotFoundPage />;
}
