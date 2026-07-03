// formatSsoJwtSchema converts a claims schema object into readable JSON for the modal.
// It accepts an optional schema object and returns a pretty-printed JSON string.
export function formatSsoJwtSchema(schema: Record<string, unknown> | undefined): string {
  if (!schema) {
    return '{}';
  }

  return JSON.stringify(schema, null, 2);
}
