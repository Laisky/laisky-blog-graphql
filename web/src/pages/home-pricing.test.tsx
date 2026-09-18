import assert from 'node:assert/strict';
import { beforeEach, describe, it, vi } from 'vitest';
import { HomePage } from './home';

const state = vi.hoisted(() => ({
  prices: undefined as unknown,
  tools: { web_search: true, web_fetch: true, ask_user: true, get_user_request: true,
    extract_key_info: true, file_io: true, memory: false },
}));
vi.mock('@/lib/tools-config-context', () => ({
  useToolsConfig: () => state.tools,
  useToolPrices: () => state.prices,
}));

/** cardPrices inspects the actual homepage's card props, independently of CSS or focus behavior. */
function cardPrices(node: unknown, result = new Map<string, string>()): Map<string, string> {
  if (Array.isArray(node)) { node.forEach((child) => cardPrices(child, result)); return result; }
  if (!node || typeof node !== 'object') return result;
  const props = (node as { props?: Record<string, unknown> }).props;
  if (!props) return result;
  if (typeof props.title === 'string' && typeof props.priceLabel === 'string') result.set(props.title, props.priceLabel);
  cardPrices(props.children, result);
  return result;
}

/** tariff creates runtime metadata; the UI must not substitute an embedded tariff. */
function tariff(amount: string) { return { currency: 'USD', unit: 'call', amount }; }

beforeEach(() => {
  state.prices = { web_search: tariff('0.005'), web_fetch: tariff('0.0001'), extract_key_info: tariff('0.002') };
  state.tools.extract_key_info = true;
});

describe('Homepage configured pricing behavior', () => {
  it('does not label the charged extraction operation Free', () => {
    assert.equal(cardPrices(HomePage()).get('extract_key_info'), '$0.002/call');
  });
  it('tracks changes in server metadata for every metered homepage card', () => {
    state.prices = { web_search: tariff('0.006'), web_fetch: tariff('0.000002'), extract_key_info: tariff('0.003') };
    const prices = cardPrices(HomePage());
    assert.equal(prices.get('web_search'), '$0.006/call');
    assert.equal(prices.get('web_fetch'), '$0.000002/call');
    assert.equal(prices.get('extract_key_info'), '$0.003/call');
  });
  it('shows unknown pricing after unavailable or malformed runtime metadata', () => {
    for (const value of [undefined, null, {}, { extract_key_info: { currency: 'USD', unit: 'call', amount: 0 } }]) {
      state.prices = value;
      assert.equal(cardPrices(HomePage()).get('extract_key_info'), 'Price unavailable');
    }
  });
  it('keeps pricing separate from interface availability', () => {
    state.tools.extract_key_info = false;
    assert.equal(cardPrices(HomePage()).get('extract_key_info'), '$0.002/call');
  });
  it('retains the unmetered Inspector UI control', () => {
    assert.equal(cardPrices(HomePage()).get('Inspector'), 'Free');
  });
});
