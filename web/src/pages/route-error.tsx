import { CloudOff, RefreshCw } from 'lucide-react';
import { useEffect, useRef, useState, type ReactElement } from 'react';
import { useRouteError } from 'react-router-dom';

import { Button } from '@/components/ui/button';
import { browserReporter } from '@/lib/browser-sentry';
import { isStaleChunkError, reloadForStaleChunk, reloadPage, type StaleChunkRecovery } from '@/lib/route-modules';
import { NotFoundPage } from '@/pages/not-found';

interface RouteErrorProps {
  /** standalone marks an error boundary rendered outside the console layout; see NotFoundPage. */
  standalone?: boolean;
}

/**
 * RouteError reports router-caught failures while retaining the existing fallback UI.
 * When a code-split page fails to download it reloads once, which is what an open tab
 * needs after a deploy replaced the hashed assets. When that cannot help (offline, or a
 * reload already happened moments ago) it explains the failure and offers a manual
 * reload instead of claiming the page does not exist.
 */
export function RouteError({ standalone = false }: RouteErrorProps) {
  const error = useRouteError();
  const reported = useRef<{ error: unknown } | null>(null);
  const [recovery, setRecovery] = useState<StaleChunkRecovery | null>(null);
  useEffect(() => {
    if (reported.current && Object.is(reported.current.error, error)) return;
    reported.current = { error };
    if (isStaleChunkError(error)) {
      const outcome = reloadForStaleChunk();
      setRecovery(outcome);
      // A reload in progress or an offline browser is not a defect worth reporting.
      if (outcome !== 'unavailable') return;
    }
    browserReporter.capture(error, 'router');
  }, [error]);

  const wrap = (content: ReactElement) => (standalone ? <main className="px-4">{content}</main> : content);
  const staleChunk = isStaleChunkError(error);
  // Until the effect has decided, show the likely outcome rather than flashing an error.
  if (recovery === 'reloading' || (staleChunk && recovery === null)) {
    return wrap(
      <p role="status" className="mx-auto max-w-lg pt-20 text-center text-muted-foreground">
        Loading the latest version of this page…
      </p>
    );
  }
  if (staleChunk) {
    return wrap(<PageLoadFailed offline={recovery === 'offline'} />);
  }
  return <NotFoundPage standalone={standalone} />;
}

/** PageLoadFailed tells the visitor a page could not be downloaded and offers a manual reload. */
function PageLoadFailed({ offline }: { offline: boolean }) {
  return (
    <div className="mx-auto flex max-w-lg flex-col items-center gap-6 pt-20 text-center">
      <div className="flex h-20 w-20 items-center justify-center rounded-full bg-muted">
        <CloudOff className="h-10 w-10 text-muted-foreground" aria-hidden="true" />
      </div>
      <div className="space-y-2">
        <h1 className="text-3xl font-bold tracking-tight text-foreground">This page could not be loaded</h1>
        <p className="text-muted-foreground">
          {offline
            ? 'You appear to be offline. Reconnect to the network, then reload the page.'
            : 'Part of the console could not be downloaded. Reloading usually fixes this; if it keeps happening, check your connection.'}
        </p>
      </div>
      <Button type="button" onClick={() => reloadPage()}>
        <RefreshCw className="mr-2 h-4 w-4" aria-hidden="true" />
        Reload page
      </Button>
    </div>
  );
}
