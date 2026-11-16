import { describe, expect, it } from 'vitest';

import { backoffDelay, createFailureBackoff } from './backoff';

describe('backoffDelay', () => {
  it('doubles from the base delay and stays capped', () => {
    const options = { baseMs: 2000, maxMs: 30_000 };
    expect([0, 1, 2, 3, 4, 5, 40].map((failures) => backoffDelay(failures, options))).toEqual([2000, 2000, 4000, 8000, 16000, 30000, 30000]);
  });

  it('spreads a jittered delay over the upper half of the step', () => {
    const at = (random: number) => backoffDelay(3, { baseMs: 1000, maxMs: 60_000, jitter: true, random: () => random });
    expect(at(0)).toBe(2000);
    expect(at(0.5)).toBe(3000);
    expect(at(0.999999)).toBe(4000);
  });
});

describe('createFailureBackoff', () => {
  it('grows with consecutive failures and starts over after a success', () => {
    const backoff = createFailureBackoff({ baseMs: 8000, maxMs: 60_000 });
    expect([backoff.fail(), backoff.fail(), backoff.fail(), backoff.fail(), backoff.fail()]).toEqual([8000, 16000, 32000, 60000, 60000]);
    backoff.reset();
    expect(backoff.fail()).toBe(8000);
  });
});
