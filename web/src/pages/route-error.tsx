import { useEffect, useRef } from 'react';
import { useRouteError } from 'react-router-dom';

import { browserReporter } from '@/lib/browser-sentry';
import { NotFoundPage } from '@/pages/not-found';

/** RouteError reports router-caught failures while retaining the existing fallback UI. */
export function RouteError() {
  const error = useRouteError();
  const reported = useRef<{ error: unknown } | null>(null);
  useEffect(() => {
    if (reported.current && Object.is(reported.current.error, error)) return;
    reported.current = { error };
    browserReporter.capture(error, 'router');
  }, [error]);
  return <NotFoundPage />;
}
