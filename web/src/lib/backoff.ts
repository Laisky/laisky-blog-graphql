/** BackoffOptions configures backoffDelay and createFailureBackoff. */
export interface BackoffOptions {
  /** baseMs is the delay after the first failure. */
  baseMs: number;
  /** maxMs caps the delay however many failures follow. */
  maxMs: number;
  /**
   * jitter spreads each delay uniformly over [delay/2, delay] ("equal jitter"), so clients
   * that started failing together, as every open tab does in an outage, do not all retry
   * at the same moment when the server comes back.
   */
  jitter?: boolean;
  /** random returns a number in [0, 1); it defaults to Math.random and exists for tests. */
  random?: () => number;
}

/**
 * backoffDelay returns the wait before the next attempt after `failures` consecutive
 * failures: baseMs for the first, doubling for each further one, capped at maxMs.
 */
export function backoffDelay(failures: number, { baseMs, maxMs, jitter = false, random = Math.random }: BackoffOptions): number {
  const delay = Math.min(baseMs * 2 ** Math.max(0, failures - 1), maxMs);
  if (!jitter) {
    return delay;
  }
  return Math.round(delay / 2 + (delay / 2) * random());
}

/** FailureBackoff counts consecutive failures of one loop and turns them into delays. */
export interface FailureBackoff {
  /** fail records one more consecutive failure and returns the wait before the next attempt. */
  fail(): number;
  /** reset forgets the failures after an attempt that succeeded. */
  reset(): void;
}

/** createFailureBackoff returns a counter whose fail() delays grow per backoffDelay until reset(). */
export function createFailureBackoff(options: BackoffOptions): FailureBackoff {
  let failures = 0;
  return {
    fail() {
      failures += 1;
      return backoffDelay(failures, options);
    },
    reset() {
      failures = 0;
    },
  };
}
