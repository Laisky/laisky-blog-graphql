import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { ExtractKeyInfoPage } from './page';
import { fetchGraphQL } from '@/lib/graphql';

const auth = vi.hoisted(() => ({ apiKey:'test-key', isToolConsoleLocked:false }));
const config = vi.hoisted(() => ({ extract_key_info:true }));
vi.mock('@/lib/api-key-context', () => ({useApiKey:()=>auth}));
vi.mock('@/lib/tools-config-context', () => ({useToolsConfig:()=>config}));
vi.mock('@/lib/graphql', () => ({fetchGraphQL:vi.fn()}));
const request = vi.mocked(fetchGraphQL);
beforeEach(() => { auth.apiKey='test-key';auth.isToolConsoleLocked=false;config.extract_key_info=true; });
afterEach(() => { cleanup(); vi.resetAllMocks(); });
function fill() {
 fireEvent.change(screen.getByLabelText('Query'),{target:{value:'where?'}});
 fireEvent.change(screen.getByLabelText('Materials'),{target:{value:'the answer is here'}});
}
describe('extraction console', () => {
 it('calls the real GraphQL contract and leaves the default top K to the server', async () => {
  request.mockResolvedValue({ExtractKeyInfo:{query:'where?',created_at:'',contexts:['here']}});
  render(<ExtractKeyInfoPage/>);fill();fireEvent.click(screen.getByRole('button',{name:'Extract'}));
  await screen.findByText('here');
  expect(request).toHaveBeenCalledTimes(1);
  expect(request.mock.calls[0][2]).toEqual({query:'where?',materials:'the answer is here'});
  expect(request.mock.calls[0][1]).toContain('ExtractKeyInfo(query: $query, materials: $materials, top_k: $topK)');
 });
 it('does not execute a disabled capability', () => {
  config.extract_key_info=false;render(<ExtractKeyInfoPage/>);fill();
  fireEvent.submit(screen.getByRole('button',{name:'Extract'}).closest('form')!);
  expect(request).not.toHaveBeenCalled();
 });
 it('aborts and ignores responses after changing credentials', async () => {
  let resolve!:(value:unknown)=>void;
  request.mockImplementation(()=>new Promise((done)=>{resolve=done}));
  const view=render(<ExtractKeyInfoPage/>);fill();fireEvent.click(screen.getByRole('button',{name:'Extract'}));
  await waitFor(()=>expect(request).toHaveBeenCalledTimes(1));
  const signal=request.mock.calls[0][3]!;
  auth.apiKey='other-key';view.rerender(<ExtractKeyInfoPage/>);
  expect(signal.aborted).toBe(true);
  await act(async()=>resolve({ExtractKeyInfo:{query:'old',created_at:'',contexts:['OLD TENANT']}}));
  expect(screen.queryByText('OLD TENANT')).toBeNull();
 });
});
