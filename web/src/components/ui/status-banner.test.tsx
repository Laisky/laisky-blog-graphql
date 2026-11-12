import '@testing-library/jest-dom/vitest';
import { render, screen } from '@testing-library/react';
import { describe, expect, it } from 'vitest';

import { StatusBanner } from './status-banner';

describe('StatusBanner live region', () => {
  it('announces error messages assertively as an alert', () => {
    render(<StatusBanner status={{ tone: 'error', message: 'Login failed.' }} />);

    const banner = screen.getByRole('alert');
    expect(banner).toHaveTextContent('Login failed.');
    expect(screen.queryByRole('status')).not.toBeInTheDocument();
  });

  it.each(['info', 'success'] as const)('announces %s messages politely as a status', (tone) => {
    render(<StatusBanner status={{ tone, message: 'Saved.' }} />);

    const banner = screen.getByRole('status');
    expect(banner).toHaveTextContent('Saved.');
    expect(screen.queryByRole('alert')).not.toBeInTheDocument();
  });

  it('keeps the tone styling and subtext on the announced element', () => {
    render(<StatusBanner status={{ tone: 'success', message: 'Connected.' }} subtext="sk-…1234" className="extra" />);

    const banner = screen.getByRole('status');
    expect(banner).toHaveClass('rounded-lg', 'border', 'bg-emerald-500/10', 'extra');
    expect(banner).toHaveTextContent('sk-…1234');
  });
});
