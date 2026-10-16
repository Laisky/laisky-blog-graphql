import { afterEach, describe, expect, it, vi } from 'vitest';
import { getPrivateErrorRedactions, getPrivateErrorMessage, markPrivateError, parsePrivateJson, readPrivateJsonResponse, reportUnexpectedRequestError, requestWithUnexpectedError, setUnexpectedRequestReporter } from './private-error';

describe('private response error annotations', () => {
  it('preserves identity, UI message, full stack and enumerable fields', () => {
    const error = Object.assign(new Error('synthetic prompt token and private response'), { status: 403, code: 'FORBIDDEN' });
    const stack = error.stack;
    const fields = Object.keys(error);
    expect(markPrivateError(error, 'MCP request failed')).toBe(error);
    expect(error.message).toBe('synthetic prompt token and private response');
    expect(error.stack).toBe(stack);
    expect(Object.keys(error)).toEqual(fields);
    expect(error.status).toBe(403);
    expect(getPrivateErrorMessage(error)).toBe('MCP request failed');
    expect(JSON.stringify(error)).not.toContain('MCP request failed');
  });
  it('leaves ordinary programming errors unmarked', () => {
    expect(getPrivateErrorMessage(new Error('ordinary failure'))).toBeUndefined();
    expect(getPrivateErrorMessage(null)).toBeUndefined();
    expect(getPrivateErrorMessage('private text')).toBeUndefined();
  });
  it('finds a private cause without changing wrappers or the cause', () => {
    const cause = markPrivateError(new Error('private body'), 'GraphQL request failed');
    const wrapper = new Error('operation failed: private body', { cause });
    const stack = wrapper.stack;
    expect(getPrivateErrorMessage(wrapper)).toBe('GraphQL request failed');
    expect(wrapper.message).toContain('private body');
    expect(wrapper.stack).toBe(stack);
    expect(wrapper.cause).toBe(cause);
  });
  it('bounds traversal, detects cycles and never evaluates cause getters', () => {
    const getter = vi.fn(() => { throw new Error('private accessor'); });
    const accessor = Object.defineProperty(new Error('wrapper'), 'cause', { get: getter });
    expect(getPrivateErrorMessage(accessor)).toBeUndefined();
    expect(getter).not.toHaveBeenCalled();
    const cycle = new Error('cycle');
    Object.defineProperty(cycle, 'cause', { value: cycle });
    expect(getPrivateErrorMessage(cycle)).toBeUndefined();
    let chain = markPrivateError(new Error('private'), 'MCP request failed');
    for (let depth = 0; depth < 8; depth += 1) chain = new Error('wrapper', { cause: chain });
    expect(getPrivateErrorMessage(chain)).toBeUndefined();
    const hostile = new Proxy({}, { getOwnPropertyDescriptor: () => { throw new Error('private'); } });
    expect(getPrivateErrorMessage(hostile)).toBeUndefined();
  });
  it('retains native JSON SyntaxError messages and stacks with a safe summary', () => {
    let failure: unknown;
    try { parsePrivateJson('synthetic private prompt is not JSON', 'Invalid MCP JSON response'); }
    catch (error) { failure = error; }
    expect(failure).toBeInstanceOf(SyntaxError);
    expect((failure as Error).stack).toContain('parsePrivateJson');
    expect(getPrivateErrorMessage(failure)).toBe('Invalid MCP JSON response');
    expect(parsePrivateJson('{"value":1}', 'Invalid MCP JSON response')).toEqual({ value: 1 });
  });
  it('preserves response parsing errors by identity and leaves body-read failures unmarked', async () => {
    const syntax = new SyntaxError('synthetic private body');
    const response = { json: vi.fn().mockRejectedValue(syntax) } as unknown as Response;
    await expect(readPrivateJsonResponse(response, 'Invalid FileIO JSON response')).rejects.toBe(syntax);
    expect(getPrivateErrorMessage(syntax)).toBe('Invalid FileIO JSON response');
    const bodyFailure = new TypeError('Body already read');
    response.json = vi.fn().mockRejectedValue(bodyFailure);
    await expect(readPrivateJsonResponse(response, 'Invalid FileIO JSON response')).rejects.toBe(bodyFailure);
    expect(getPrivateErrorMessage(bodyFailure)).toBeUndefined();
  });
});

afterEach(() => { setUnexpectedRequestReporter(); vi.restoreAllMocks(); });

describe('actionable request failure observer', () => {
  it('reports malformed private JSON once with its safe marker already applied', async () => {
    const observer = vi.fn((error: unknown) => expect(getPrivateErrorMessage(error)).toBe('Invalid FileIO JSON response'));
    setUnexpectedRequestReporter(observer);
    const syntax = new SyntaxError('synthetic private file');
    const response = { json: async () => { throw syntax; } } as unknown as Response;
    await expect(readPrivateJsonResponse(response, 'Invalid FileIO JSON response')).rejects.toBe(syntax);
    reportUnexpectedRequestError(syntax, { operation: 'file-io' });
    expect(observer).toHaveBeenCalledExactlyOnceWith(syntax, { operation: 'file-io' });
  });
  it('preserves network failure identity and does not add request URLs or bodies', async () => {
    const observer = vi.fn(); setUnexpectedRequestReporter(observer);
    const failure = new TypeError('Failed to fetch');
    const fetcher = vi.fn().mockRejectedValue(failure);
    await expect(requestWithUnexpectedError('/private?token=synthetic', { body: 'private request' }, 'mcp', fetcher)).rejects.toBe(failure);
    reportUnexpectedRequestError(failure, { operation: 'mcp' });
    expect(observer).toHaveBeenCalledExactlyOnceWith(failure, { operation: 'mcp' });
  });
  it.each([400, 401, 403, 404, 409, 412, 422, 429])('does not report an expected HTTP %i reply', async (status) => {
    const observer = vi.fn(); setUnexpectedRequestReporter(observer);
    const response = new Response('synthetic remote error', { status });
    expect(await requestWithUnexpectedError('/api', {}, 'ask-user', vi.fn().mockResolvedValue(response))).toBe(response);
    expect(observer).not.toHaveBeenCalled();
  });
  it('does not report cancellation, including custom caller abort reasons', async () => {
    const observer = vi.fn(); setUnexpectedRequestReporter(observer);
    const aborted = new DOMException('Cancelled', 'AbortError');
    await expect(requestWithUnexpectedError('/api', {}, 'mcp', vi.fn().mockRejectedValue(aborted))).rejects.toBe(aborted);
    const custom = new Error('synthetic caller reason');
    const controller = new AbortController(); controller.abort(custom);
    await expect(requestWithUnexpectedError('/api', { signal: controller.signal }, 'mcp', vi.fn().mockRejectedValue(custom))).rejects.toBe(custom);
    expect(observer).not.toHaveBeenCalled();
  });
  it('bounds metadata and contains reporter failures with only a static diagnostic', () => {
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => undefined);
    const observer = vi.fn(() => { throw new Error('synthetic token must not be logged'); });
    setUnexpectedRequestReporter(observer);
    const error = new Error('original UI failure');
    expect(() => reportUnexpectedRequestError(error, { operation: 'graphql', status: NaN, code: Infinity })).not.toThrow();
    expect(observer).toHaveBeenCalledExactlyOnceWith(error, { operation: 'graphql' });
    expect(warn).toHaveBeenCalledExactlyOnceWith('Unexpected request error reporting failed.');
    expect(error.message).toBe('original UI failure');
  });
  it('keeps failures eligible until a ready reporter is registered', () => {
    const error = new Error('failure');
    reportUnexpectedRequestError(error, { operation: 'graphql' });
    const observer = vi.fn(); setUnexpectedRequestReporter(observer);
    reportUnexpectedRequestError(error, { operation: 'graphql', status: 200, code: -32603 });
    expect(observer).toHaveBeenCalledExactlyOnceWith(error, { operation: 'graphql', status: 200, code: -32603 });
  });
});

describe('specific private exception redactions', () => {
  it('preserves an ordinary outer message while identifying its private inner cause', () => {
    const inner = markPrivateError(new Error('synthetic private response body'), 'GraphQL request failed');
    const outer = new Error('GraphQL display failed', { cause: inner });
    expect(getPrivateErrorRedactions(outer)).toEqual([
      { message: 'synthetic private response body', replacement: 'GraphQL request failed' },
    ]);
    expect(outer.message).toBe('GraphQL display failed');
    expect(inner.message).toBe('synthetic private response body');
    expect(getPrivateErrorMessage(outer)).toBe('GraphQL request failed');
  });
  it('provides an exact match for copied linked-exception messages and leaves unrelated messages alone', () => {
    const inner = markPrivateError(new Error('synthetic private MCP result'), 'MCP request failed');
    const outer = new Error('Tool renderer failed', { cause: inner });
    const redactions = getPrivateErrorRedactions(outer);
    const linkedCopies = ['synthetic private MCP result', 'Tool renderer failed', 'synthetic private MCP result'];
    expect(linkedCopies.map((message) => redactions.find((pair) => pair.message === message)?.replacement ?? message))
      .toEqual(['MCP request failed', 'Tool renderer failed', 'MCP request failed']);
  });
  it('includes each marked message and bounds cyclic or deeply nested causes', () => {
    const inner = markPrivateError(new Error('private inner'), 'MCP request failed');
    const outer = markPrivateError(new Error('private outer', { cause: inner }), 'FileIO request failed');
    Object.defineProperty(inner, 'cause', { value: outer });
    expect(getPrivateErrorRedactions(outer)).toEqual([
      { message: 'private outer', replacement: 'FileIO request failed' },
      { message: 'private inner', replacement: 'MCP request failed' },
    ]);
    let deep: Error = inner;
    for (let depth = 0; depth < 8; depth += 1) deep = new Error('ordinary wrapper', { cause: deep });
    expect(getPrivateErrorRedactions(deep)).toEqual([]);
  });
  it('does not invoke message or cause getters and contains hostile descriptors', () => {
    const messageGetter = vi.fn(() => 'synthetic private getter');
    const causeGetter = vi.fn(() => { throw new Error('private cause getter'); });
    const privateAccessor = markPrivateError(new Error('original'), 'MCP request failed');
    Object.defineProperty(privateAccessor, 'message', { get: messageGetter });
    Object.defineProperty(privateAccessor, 'cause', { get: causeGetter });
    expect(getPrivateErrorRedactions(privateAccessor)).toEqual([]);
    expect(messageGetter).not.toHaveBeenCalled();
    expect(causeGetter).not.toHaveBeenCalled();
    const hostile = new Proxy({}, { getOwnPropertyDescriptor: () => { throw new Error('private trap'); } });
    expect(getPrivateErrorRedactions(hostile)).toEqual([]);
  });
  it('reads only current own message data and does not cache raw messages', () => {
    const error = markPrivateError(new Error('first private body'), 'MCP request failed');
    expect(getPrivateErrorRedactions(error)).toEqual([{ message: 'first private body', replacement: 'MCP request failed' }]);
    error.message = 'updated private body';
    expect(getPrivateErrorRedactions(error)).toEqual([{ message: 'updated private body', replacement: 'MCP request failed' }]);
    expect(getPrivateErrorRedactions(new Error('ordinary failure'))).toEqual([]);
    expect(getPrivateErrorRedactions(null)).toEqual([]);
  });
});
