import { describe, expect, it } from 'vitest';
import { requireHistoryID } from './version-state';

describe('exact history row identifiers', () => {
  it.each(['1', '7', '9007199254740992', '9007199254740993', '18446744073709551615'])(
    'preserves %s as a string', (id) => expect(requireHistoryID(id)).toBe(id),
  );
  it('keeps adjacent large history IDs distinct in URLs', () => {
    const first = requireHistoryID('9007199254740992');
    const second = requireHistoryID('9007199254740993');
    expect(`/versions/${first}/restore`).not.toBe(`/versions/${second}/restore`);
    // This is the old numeric representation's collision, not the new protocol.
    expect(Number(first)).toBe(Number(second));
  });
  it.each([1, 9007199254740992, null, undefined, '', '0', '01', '-1', '1e3', '../7', '18446744073709551616'])(
    'rejects lossy or malformed identifiers: %s', (id) => expect(() => requireHistoryID(id)).toThrow(),
  );
});
