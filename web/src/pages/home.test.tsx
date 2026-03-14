import { render, screen } from '@testing-library/react';
import { describe, expect, it } from 'vitest';
import { MemoryRouter } from 'react-router-dom';

import { ToolsConfigProvider } from '@/lib/tools-config-context';

import { HomePage } from './home';

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
        >
          <HomePage />
        </ToolsConfigProvider>
      </MemoryRouter>
    );

    expect(screen.getByText('$0.005/call')).toBeDefined();
    expect(screen.getByText('$0.0001/call')).toBeDefined();
    expect(screen.getAllByText('Free')).toHaveLength(7);
  });
});
