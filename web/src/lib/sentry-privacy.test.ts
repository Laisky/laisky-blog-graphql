import { expect, it } from 'vitest';
import { markPrivateError } from './private-error';
import { sanitizeBreadcrumb, sanitizeErrorEvent, sanitizeText, sanitizeUrl } from './sentry-privacy';

it('filters URL userinfo/query/fragment including relative, blob and embedded URLs without losing routes', () => {
  for (const url of ['https://user:synthetic-private@mcp.laisky.com/mcp?token=synthetic-private#private', '//user:synthetic-private@mcp.laisky.com/mcp?token=synthetic-private#private', 'blob:https://user:synthetic-private@mcp.laisky.com/mcp?token=synthetic-private#private']) {
    expect(sanitizeUrl(url)).not.toContain('synthetic-private'); expect(sanitizeUrl(url)).toContain('/mcp');
  }
  expect(sanitizeText('Request https://user:synthetic-private@mcp.laisky.com/mcp?api_key=synthetic-private#private failed.')).toBe('Request https://mcp.laisky.com/mcp failed.');
  expect(sanitizeText('Failed to render Dashboard')).toBe('Failed to render Dashboard');
});

it('filters already-formatted private console objects, retaining useful diagnostic fields', () => {
  const input = ['HTTP request failed', { status: 503, rawResponse: 'synthetic-private-response', headers: { Authorization: 'synthetic-private' }, token_count: 4 }];
  const clean = sanitizeBreadcrumb({ category: 'console', message: 'HTTP request failed {rawResponse:synthetic-private-response}', data: { arguments: input, logger: 'console' } }, { input });
  expect(JSON.stringify(clean)).not.toContain('synthetic-private'); expect(clean?.message).toContain('HTTP request failed'); expect(clean?.message).toContain('503'); expect(clean?.message).toContain('token_count');
});

it('preserves an ordinary outer error and all source frames while redacting marked truncated copies', () => {
  const payload = 'synthetic-private-response-'.repeat(40); const cause = markPrivateError(new Error(payload), 'MCP request failed');
  const error = new Error('Dashboard initialization failed', { cause });
  const clean = sanitizeErrorEvent({ exception: { values: [{ value: error.message }, { value: payload.slice(0,250) + '...' }, { value: ('Error: ' + payload).slice(0,250) + '...' }] } }, { originalException: error });
  expect(clean?.exception?.values?.map((item) => item.value)).toEqual(['Dashboard initialization failed','MCP request failed','MCP request failed']);
});

it('bounds hostile metadata branching and skips accessors while retaining technical browser context', () => {
  const getter = () => { throw new Error('must not read'); };
  let branch: Record<string, unknown> = { body: 'synthetic-private' };
  for (let depth=0; depth<8; depth++) branch = Object.fromEntries(Array.from({ length:100 }, (_,index)=>[`node${index}`, branch]));
  const custom = Object.defineProperty({ branch }, 'danger', { enumerable:true, get:getter });
  const clean = sanitizeErrorEvent({ contexts: { browser: { name:'Firefox', version:'140' }, custom }, extra: { apiKeys:['synthetic-private'], userId:'synthetic-private', prompts:'synthetic-private', token_count:7 } }, {});
  expect(clean?.contexts?.browser?.name).toBe('Firefox'); expect(clean?.extra?.token_count).toBe(7); expect(JSON.stringify(clean)).not.toContain('synthetic-private'); expect(JSON.stringify(clean).length).toBeLessThan(100000);
});

it('omits private DOM inputs while retaining sanitized button location', () => {
  const input = document.createElement('input'); input.value='synthetic-private'; input.id='synthetic-private';
  expect(sanitizeBreadcrumb({ category:'ui.input', message:input.id }, { target:input })).toBeNull();
  const button = document.createElement('button'); button.id='synthetic-private'; button.className='submit';
  expect(sanitizeBreadcrumb({ category:'ui.click', message:button.id }, { target:button })?.message).toBe('button.submit');
});
