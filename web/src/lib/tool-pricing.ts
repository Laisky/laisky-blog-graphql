/** ToolPrice is a configured charge; it is not a receipt or an authorization decision. */
export type ToolPrice = { currency: 'USD'; unit: 'call'; amount: string };

/** toolPriceLabel validates untrusted runtime metadata and never assumes missing data is free. */
export function toolPriceLabel(prices: unknown, name: string): string {
  if (!prices || typeof prices !== 'object' || Array.isArray(prices) || !Object.hasOwn(prices, name)) return 'Price unavailable';
  const value: unknown = (prices as Record<string, unknown>)[name];
  if (!value || typeof value !== 'object' || Array.isArray(value)) return 'Price unavailable';
  const price = value as Partial<ToolPrice>;
  if (price.currency !== 'USD' || price.unit !== 'call' || typeof price.amount !== 'string'
      || !/^(0|[1-9][0-9]{0,15})(\.[0-9]{1,6})?$/.test(price.amount)) return 'Price unavailable';
  return /^0(?:\.0+)?$/.test(price.amount) ? 'Free' : `$${price.amount}/call`;
}
