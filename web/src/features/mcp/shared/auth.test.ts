import { describe, expect, it } from 'vitest';

import { resolveMcpEndpointByLocation } from './auth';

describe('resolveMcpEndpointByLocation', () => {
  it('uses the site prefix when pathname contains /mcp', () => {
    expect(resolveMcpEndpointByLocation('/mcp/tools/file_io', '10.0.0.2')).toBe('/mcp');
  });

  it('does not treat non-mcp path prefixes as MCP routing hints', () => {
    expect(resolveMcpEndpointByLocation('/portal/tools/file_io', 'mcp.laisky.com')).toBe('/');
  });

  it('uses root endpoint for domain-based production routing', () => {
    expect(resolveMcpEndpointByLocation('/tools/file_io', 'mcp.laisky.com')).toBe('/');
  });

  it('keeps /mcp endpoint for localhost development', () => {
    expect(resolveMcpEndpointByLocation('/tools/file_io', 'localhost')).toBe('/mcp');
  });

  it('keeps /mcp endpoint for private-network hosts', () => {
    expect(resolveMcpEndpointByLocation('/tools/file_io', '100.75.198.70')).toBe('/mcp');
  });

  it('prioritizes explicit endpoint configuration', () => {
    expect(resolveMcpEndpointByLocation('/tools/file_io', 'mcp.laisky.com', '/custom/')).toBe('/custom');
    expect(resolveMcpEndpointByLocation('/tools/file_io', 'mcp.laisky.com', 'https://api.laisky.com/mcp/')).toBe(
      'https://api.laisky.com/mcp'
    );
  });
});
