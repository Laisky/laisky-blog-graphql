/**
 * formatDate converts a date string to a localized date-time string.
 * Returns "N/A" if the value is null/undefined or an invalid date.
 */
export function formatDate(value?: string | null): string {
  if (!value) return 'N/A';
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? value : date.toLocaleString();
}

/**
 * identityMessage constructs a human-readable identity string from user info.
 */
export function identityMessage(userId?: string, keyHint?: string): string {
  const user = userId || 'unknown user';
  const suffix = keyHint ? `token •••${keyHint}` : 'token hidden';
  return `Linked identity: ${user} (${suffix})`;
}
