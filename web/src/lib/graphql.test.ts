import { afterEach, describe, expect, it, vi } from 'vitest';
import { configurePublicApiBasePath } from './api-base';
import { fetchGraphQL, GraphQLRequestError } from './graphql';

afterEach(() => { vi.unstubAllGlobals(); configurePublicApiBasePath(undefined); });
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
    expect((failure as GraphQLRequestError).errors[0].extensions?.code).toBe('VERSION_CONFLICT');
    expect(fetch).toHaveBeenCalledTimes(1);
  });
});
