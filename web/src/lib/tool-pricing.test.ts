import assert from 'node:assert/strict';
import { describe, it } from 'vitest';
import { toolPriceLabel } from './tool-pricing';

/** metadata creates explicit USD/call data rather than a guessed UI default. */
const metadata = (amount: unknown) => ({ extract_key_info: { currency: 'USD', unit: 'call', amount } });

describe('Configured tool price display', () => {
  it('keeps exact decimal values including a single quota unit', () => {
    for (const amount of ['0.002', '0.000002', '0.005', '18446744073709.551614']) {
      assert.equal(toolPriceLabel(metadata(amount), 'extract_key_info'), `$${amount}/call`);
    }
  });
  it('shows free only when the server explicitly supplies a zero tariff', () => {
    for (const amount of ['0', '0.0', '0.000000']) assert.equal(toolPriceLabel(metadata(amount), 'extract_key_info'), 'Free');
  });
  it('never treats missing, malformed, inherited or unsupported metadata as free', () => {
    for (const prices of [null, undefined, {}, [], '0', Object.create(metadata('0')),
      ...[null, true, 0, 0.002, '-1', 'NaN', 'Infinity', '1e-3', '00.002', '0.0000001', '<script>'].map(metadata),
      { extract_key_info: { currency: 'CAD', unit: 'call', amount: '0' } },
      { extract_key_info: { currency: 'USD', unit: 'token', amount: '0' } }]) {
      assert.equal(toolPriceLabel(prices, 'extract_key_info'), 'Price unavailable');
    }
  });
});
