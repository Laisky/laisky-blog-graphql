import { getPrivateErrorMessage, setUnexpectedRequestReporter } from './private-error';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { configurePublicApiBasePath } from './api-base';
import { fetchGraphQL, GraphQLRequestError } from './graphql';

afterEach(() => { setUnexpectedRequestReporter(); vi.unstubAllGlobals(); configurePublicApiBasePath(undefined); });
describe('GraphQL transport contract', () => {
  it('uses the public prefix, normalized auth and abort signal', async () => {
    configurePublicApiBasePath('/gateway');
    const fetch = vi.fn().mockResolvedValue(new Response(JSON.stringify({data:{Hello:'ok'}}), {status:200}));
    vi.stubGlobal('fetch', fetch);
    const controller = new AbortController();
    expect(await fetchGraphQL(' bearer Bearer test-key ', '{Hello}', {}, controller.signal)).toEqual({Hello:'ok'});
    expect(fetch).toHaveBeenCalledWith('/gateway/query', expect.objectContaining({cache:'no-store', signal:controller.signal,
      headers:expect.objectContaining({Authorization:'Bearer test-key'})}));
  });
  it('preserves GraphQL error codes and never retries', async () => {
    const fetch = vi.fn().mockResolvedValue(new Response(JSON.stringify({errors:[{message:'conflict',extensions:{code:'VERSION_CONFLICT'}}]}), {status:200}));
    vi.stubGlobal('fetch', fetch);
    const failure = await fetchGraphQL('key', 'mutation { anything }').catch((error: unknown) => error);
    expect(failure).toBeInstanceOf(GraphQLRequestError);
    expect((failure as GraphQLRequestError).message).toBe('conflict');
    expect(getPrivateErrorMessage(failure)).toBe('GraphQL request failed');
    expect((failure as GraphQLRequestError).errors[0].extensions?.code).toBe('VERSION_CONFLICT');
    expect(fetch).toHaveBeenCalledTimes(1);
  });
});

describe('GraphQL actionable and expected reply observation', () => {
  it.each([400, 401, 403, 404, 409, 412, 422, 429])('leaves expected HTTP %i failures unreported', async (status) => {
    const observer = vi.fn(); setUnexpectedRequestReporter(observer);
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response('synthetic private failure', { status })));
    await expect(fetchGraphQL('key', '{query}')).rejects.toBeInstanceOf(GraphQLRequestError);
    expect(observer).not.toHaveBeenCalled();
  });
  it.each(['not JSON', '{"errors":{}}', '{"data":null}'])('observes malformed success reply %s exactly once', async (body) => {
    const observer = vi.fn(); setUnexpectedRequestReporter(observer);
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(body)));
    const error = await fetchGraphQL('key', '{query}').catch((failure: unknown) => failure);
    expect(error).toBeInstanceOf(GraphQLRequestError);
    expect(observer).toHaveBeenCalledExactlyOnceWith(error, { operation: 'graphql', status: 200 });
  });
});
