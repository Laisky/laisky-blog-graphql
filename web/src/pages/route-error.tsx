import { useEffect, useRef, useState } from 'react';
import { useRouteError } from 'react-router-dom';

import { browserReporter } from '@/lib/browser-sentry';
import { isStaleChunkError, reloadForStaleChunk } from '@/lib/route-modules';
import { NotFoundPage } from '@/pages/not-found';

interface RouteErrorProps {
  /** standalone marks an error boundary rendered outside the console layout; see NotFoundPage. */
  standalone?: boolean;
}

/**
 * RouteError reports router-caught failures while retaining the existing fallback UI.
 * When a code-split page fails to download (typically an open tab after a deploy
 * replaced the hashed assets) it reloads once so the browser picks up the new build.
 */
export function RouteError({ standalone = false }: RouteErrorProps) {
  const error = useRouteError();
  const reported = useRef<{ error: unknown } | null>(null);
  const [reloading, setReloading] = useState(false);
  useEffect(() => {
    if (reported.current && Object.is(reported.current.error, error)) return;
    reported.current = { error };
    if (isStaleChunkError(error) && reloadForStaleChunk()) {
      setReloading(true);
      return;
    }
    browserReporter.capture(error, 'router');
  }, [error]);

  if (reloading) {
    const status = (
      <p role="status" className="mx-auto max-w-lg pt-20 text-center text-muted-foreground">
        Loading the latest version of this page…
      </p>
    );
    return standalone ? <main className="px-4">{status}</main> : status;
  }
  return <NotFoundPage standalone={standalone} />;
}
