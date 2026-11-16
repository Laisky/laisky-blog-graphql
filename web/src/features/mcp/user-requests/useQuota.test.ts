import { describe, expect, it } from 'vitest';

import type { QuotaResponse } from './api';
import { quotaPercent } from './useQuota';

const base: QuotaResponse = { user_identity: 'u', used_bytes: 0, quota_bytes: 1000, object_count: 0, ttl_days: 7 };

describe('quotaPercent', () => {
  it('counts bytes reserved by unfinished uploads, as admission does', () => {
    expect(quotaPercent({ ...base, used_bytes: 250, reserved_bytes: 250, available_bytes: 500 })).toBe(50);
  });

  it('accepts a server that does not report reservations', () => {
    expect(quotaPercent({ ...base, used_bytes: 250 })).toBe(25);
  });

  it('caps at 100 and is 0 without a quota', () => {
    expect(quotaPercent({ ...base, used_bytes: 900, reserved_bytes: 300 })).toBe(100);
    expect(quotaPercent({ ...base, quota_bytes: 0 })).toBe(0);
    expect(quotaPercent(null)).toBe(0);
  });
});
