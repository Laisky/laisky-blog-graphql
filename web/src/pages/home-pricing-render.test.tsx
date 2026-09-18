import { cleanup, render, screen, within } from '@testing-library/react';
import { afterEach, describe, expect, it } from 'vitest';
import { MemoryRouter } from 'react-router-dom';
import { ToolsConfigProvider } from '@/lib/tools-config-context';
import { defaultToolsConfig } from '@/lib/runtime-config';
import { HomePage } from './home';

/** home uses the real context and page; no pricing hook or card implementation is mocked. */
function home(amount?: string) {
  const prices = amount === undefined ? undefined : { extract_key_info: { currency: 'USD', unit: 'call', amount } };
  return <MemoryRouter><ToolsConfigProvider config={defaultToolsConfig} prices={prices}><HomePage /></ToolsConfigProvider></MemoryRouter>;
}

/** extractionCard scopes assertions to the extraction operation instead of unrelated free UI controls. */
function extractionCard() {
  const card = screen.getByText('extract_key_info').closest('a');
  if (!card) throw new Error('Extraction card is not linked to its configured interface');
  return within(card);
}

afterEach(cleanup);

describe('Homepage price context integration', () => {
  it('renders configured prices and updates them through the real provider', () => {
    const view = render(home('0.002'));
    expect(extractionCard().getByText('$0.002/call')).toBeTruthy();
    expect(extractionCard().queryByText('Free')).toBeNull();
    view.rerender(home('0.003'));
    expect(extractionCard().getByText('$0.003/call')).toBeTruthy();
    expect(extractionCard().queryByText('$0.002/call')).toBeNull();
  });
  it('does not turn missing runtime metadata into a zero price', () => {
    render(home());
    expect(extractionCard().getByText('Price unavailable')).toBeTruthy();
    expect(extractionCard().queryByText('Free')).toBeNull();
  });
});
