/**
 * PollReason identifies what started a poll run.
 * - 'initial': the first load requested by start().
 * - 'timer': a background tick scheduled after a previous run.
 * - 'visible': the catch-up refresh after the document became visible again.
 * - 'manual': an explicit refresh(), usually right after a user action.
 */
export type PollReason = 'initial' | 'timer' | 'visible' | 'manual';

/** PollRunContext is handed to every poll run. */
export interface PollRunContext {
  /** signal aborts when a newer run supersedes this one or the poller is disposed. */
  signal: AbortSignal;
  /** reason reports what started this run. */
  reason: PollReason;
  /**
   * priority is 'low' for timer-driven background ticks so they do not compete
   * with requests a person is waiting on; it is undefined (browser default) otherwise.
   */
  priority?: RequestPriority;
}

/**
 * PollRun performs one poll. It resolves to the delay in milliseconds before the
 * next background tick, or null to stop polling. A run must handle its own
 * errors; a rejected run stops polling.
 */
export type PollRun = (context: PollRunContext) => Promise<number | null>;

/** VisibilityPoller controls a chained-timeout poll loop that pauses while the page is hidden. */
export interface VisibilityPoller {
  /** start runs the first poll immediately at default priority. */
  start(): void;
  /** schedule replaces any pending tick with a background tick after delay milliseconds. */
  schedule(delay: number): void;
  /** refresh aborts any in-flight run and polls immediately at default priority. */
  refresh(): void;
  /** dispose cancels the pending tick, aborts in-flight work and removes the visibility listener. */
  dispose(): void;
}

/**
 * createVisibilityPoller builds a poll loop that never overlaps runs, schedules
 * each tick only after the previous run settles, skips ticks while the document
 * is hidden, and refreshes once when the document becomes visible again.
 * Background ticks are marked with fetch priority 'low'.
 */
export function createVisibilityPoller(run: PollRun, doc: Document = document): VisibilityPoller {
  let disposed = false;
  let timer: ReturnType<typeof setTimeout> | null = null;
  let inFlight: AbortController | null = null;
  // pausedWhileHidden records that a tick was due (or pending) while the page was hidden,
  // so the loop resumes on visibility instead of staying stopped.
  let pausedWhileHidden = false;

  const isHidden = () => doc.visibilityState === 'hidden';

  function clearTimer() {
    if (timer !== null) {
      clearTimeout(timer);
      timer = null;
    }
  }

  function execute(reason: PollReason) {
    if (disposed) return;
    clearTimer();
    pausedWhileHidden = false;
    inFlight?.abort();
    const controller = new AbortController();
    inFlight = controller;

    const context: PollRunContext = { signal: controller.signal, reason };
    if (reason === 'timer') {
      context.priority = 'low';
    }

    void run(context)
      .catch(() => null)
      .then((next) => {
        if (inFlight === controller) {
          inFlight = null;
        }
        if (disposed || controller.signal.aborted || next === null) return;
        schedule(next);
      });
  }

  function schedule(delay: number) {
    if (disposed) return;
    clearTimer();
    if (isHidden()) {
      pausedWhileHidden = true;
      return;
    }
    timer = setTimeout(() => {
      timer = null;
      if (isHidden()) {
        pausedWhileHidden = true;
        return;
      }
      execute('timer');
    }, delay);
  }

  function handleVisibilityChange() {
    if (disposed) return;
    if (isHidden()) {
      if (timer !== null) {
        clearTimer();
        pausedWhileHidden = true;
      }
      return;
    }
    // An in-flight run re-arms the loop itself when it settles.
    if (!pausedWhileHidden || inFlight) return;
    execute('visible');
  }

  doc.addEventListener('visibilitychange', handleVisibilityChange);

  return {
    start: () => execute('initial'),
    schedule,
    refresh: () => execute('manual'),
    dispose: () => {
      if (disposed) return;
      disposed = true;
      clearTimer();
      inFlight?.abort();
      inFlight = null;
      doc.removeEventListener('visibilitychange', handleVisibilityChange);
    },
  };
}
