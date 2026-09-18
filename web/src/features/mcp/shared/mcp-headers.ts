/** HeaderBinding records a validated, statically reachable x-mcp-header property. */
export type HeaderBinding = { path: string[]; name: string; type: 'string' | 'integer' | 'boolean' };

/** encodeMcpHeader mirrors UTF-8 values without header injection or sentinel ambiguity. */
export function encodeMcpHeader(value: string): string {
  const bytes = new TextEncoder().encode(value);
  if (new TextDecoder().decode(bytes) !== value) throw new Error('MCP header value contains an unpaired surrogate');
  if (/^[\x09\x20-\x7e]*$/.test(value) && value.trim() === value && !(value.startsWith('=?base64?') && value.endsWith('?='))) return value;
  let binary = '';
  for (const byte of bytes) binary += String.fromCharCode(byte);
  return `=?base64?${btoa(binary)}?=`;
}

// These bounds match internal/web/tool_cors.go. Count only dynamic names for
// the 100-header limit, but include fixed unsafe names in the 8192-byte budget.
const MAX_MIRRORED_HEADERS = 100;
const MAX_PREFLIGHT_NAME_BYTES = 8192;
const MODERN_UNSAFE_REQUEST_HEADERS = [
  'authorization', 'content-type', 'mcp-method', 'mcp-name', 'mcp-protocol-version',
];

/** validateHeaderBudget rejects a tool whose possible request cannot pass CORS. */
function validateHeaderBudget(bindings: HeaderBinding[]): void {
  if (bindings.length > MAX_MIRRORED_HEADERS) throw new Error('MCP mirrored header count exceeds the server limit');
  // Names are ASCII tchar tokens. Fetch lowercases/deduplicates/sorts unsafe
  // names and joins them with commas, WITHOUT spaces. Accept's fixed value is
  // safelisted; modern calls do not carry Mcp-Session-Id.
  const names = [...MODERN_UNSAFE_REQUEST_HEADERS, ...bindings.map((binding) => binding.name.toLowerCase())];
  const bytes = [...new Set(names)].sort().join(',').length;
  if (bytes > MAX_PREFLIGHT_NAME_BYTES) throw new Error('MCP mirrored header name budget exceeds the server limit');
}

/** headerBindings validates annotations before a tool can be exposed or called. */
export function headerBindings(schema: unknown): HeaderBinding[] {
  if (!schema || typeof schema !== 'object' || Array.isArray(schema)) throw new Error('MCP tool inputSchema must be an object');
  const bindings: HeaderBinding[] = [];
  const names = new Set<string>();
  let nodes = 0;
  // These keywords contain schemas but never add a statically reachable property.
  const schemaMaps = new Set(['patternProperties', '$defs', 'definitions', 'dependentSchemas', 'dependencies']);
  const schemaChildren = new Set(['additionalProperties', 'unevaluatedProperties', 'propertyNames', 'items', 'prefixItems',
    'contains', 'additionalItems', 'unevaluatedItems', 'allOf', 'anyOf', 'oneOf', 'not', 'if', 'then', 'else']);
  function visit(raw: unknown, path: string[], reachable: boolean, depth: number): void {
    if (raw === true || raw === false) return;
    if (!raw || typeof raw !== 'object' || Array.isArray(raw)) return;
    if (++nodes > 10_000 || depth > 64) throw new Error('MCP input schema exceeds traversal limits');
    const node = raw as Record<string, unknown>;
    if (Object.hasOwn(node, 'x-mcp-header')) {
      const name = node['x-mcp-header'];
      if (!reachable || !path.length || '$ref' in node || typeof name !== 'string' || !/^[!#$%&'*+.^_`|~0-9A-Za-z-]+$/.test(name)
          || names.has(name.toLowerCase()) || typeof node.type !== 'string' || !['string', 'integer', 'boolean'].includes(node.type)) {
        throw new Error('Invalid x-mcp-header annotation; tool cannot be called');
      }
      names.add(name.toLowerCase());
      bindings.push({ path, name: `Mcp-Param-${name}`, type: node.type as HeaderBinding['type'] });
    }
    for (const [key, child] of Object.entries(node)) {
      if (key === 'properties' && child && typeof child === 'object' && !Array.isArray(child)) {
        for (const [property, value] of Object.entries(child)) visit(value, [...path, property], reachable && !('$ref' in node), depth + 1);
      } else if (schemaMaps.has(key) && child && typeof child === 'object' && !Array.isArray(child)) {
        for (const value of Object.values(child)) visit(value, path, false, depth + 1);
      } else if (schemaChildren.has(key)) {
        for (const value of Array.isArray(child) ? child : [child]) visit(value, path, false, depth + 1);
      }
    }
  }
  visit(schema, [], true, 0);
  validateHeaderBudget(bindings);
  return bindings;
}

/** mirrorMcpArguments reads own properties only and never rounds an integer header. */
export function mirrorMcpArguments(bindings: HeaderBinding[], args: Record<string, unknown>): Record<string, string> {
  const headers: Record<string, string> = {};
  for (const binding of bindings) {
    let value: unknown = args;
    for (const key of binding.path) {
      if (!value || typeof value !== 'object' || Array.isArray(value) || !Object.hasOwn(value, key)) { value = undefined; break; }
      value = (value as Record<string, unknown>)[key];
    }
    if (value === undefined || value === null) continue;
    if ((binding.type === 'integer' && (typeof value !== 'number' || !Number.isSafeInteger(value)))
        || (binding.type !== 'integer' && typeof value !== binding.type)) throw new Error('MCP mirrored argument has an invalid primitive type or range');
    headers[binding.name] = encodeMcpHeader(String(value));
  }
  return headers;
}
