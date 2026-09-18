import { render, screen, within } from '@testing-library/react';
import { describe, expect, it } from 'vitest';
import { MemoryRouter } from 'react-router-dom';

import { ToolsConfigProvider } from '@/lib/tools-config-context';

import { HomePage } from './home';

/** configuredPrices mirrors library/billing/oneapi.SharedToolPrices() exactly.
 * The homepage has no tariff of its own, so the test supplies the same runtime
 * metadata the server publishes instead of restating a second price table. */
const configuredPrices = {
  web_search: { currency: 'USD', unit: 'call', amount: '0.005' },
  web_fetch: { currency: 'USD', unit: 'call', amount: '0.0001' },
  extract_key_info: { currency: 'USD', unit: 'call', amount: '0.002' },
  find_tool: { currency: 'USD', unit: 'call', amount: '0.002' },
};

/** cardFor scopes a price assertion to one operation's card, so an unrelated
 * "Free" control elsewhere on the page cannot satisfy it. */
function cardFor(title: string) {
  const card = screen.getByText(title).closest('a');
  if (!card) throw new Error(`card for ${title} is not linked to its interface`);
  return within(card);
}

describe('HomePage', () => {
  it('shows price badges for paid and free tool cards', () => {
    render(
      <MemoryRouter>
        <ToolsConfigProvider
          config={{
            web_search: true,
            web_fetch: true,
            ask_user: true,
            get_user_request: true,
            extract_key_info: true,
            file_io: true,
            memory: true,
          }}
          prices={configuredPrices}
        >
          <HomePage />
        </ToolsConfigProvider>
      </MemoryRouter>
    );

    expect(cardFor('web_search').getByText('$0.005/call')).toBeDefined();
    expect(cardFor('web_fetch').getByText('$0.0001/call')).toBeDefined();
    expect(cardFor('extract_key_info').getByText('$0.002/call')).toBeDefined();
    // Charged operations must never also render the Free badge.
    expect(cardFor('extract_key_info').queryByText('Free')).toBeNull();
    // Inspector, ask_user, get_user_request, file_io, memory and Call Logs.
    expect(screen.getAllByText('Free')).toHaveLength(6);
  });

  it('never renders a charged operation as free when runtime pricing is absent', () => {
    render(
      <MemoryRouter>
        <ToolsConfigProvider
          config={{
            web_search: true,
            web_fetch: true,
            ask_user: true,
            get_user_request: true,
            extract_key_info: true,
            file_io: true,
            memory: true,
          }}
        >
          <HomePage />
        </ToolsConfigProvider>
      </MemoryRouter>
    );

    expect(cardFor('web_search').getByText('Price unavailable')).toBeDefined();
    expect(cardFor('web_fetch').getByText('Price unavailable')).toBeDefined();
    expect(cardFor('extract_key_info').getByText('Price unavailable')).toBeDefined();
    expect(screen.getAllByText('Free')).toHaveLength(6);
  });
});
