/**
 * formatDate converts a date string to a localized date-time string.
 * Returns "N/A" if the value is null/undefined or an invalid date.
 */
export function formatDate(value?: string | null): string {
  if (!value) return 'N/A';
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? value : date.toLocaleString();
}
