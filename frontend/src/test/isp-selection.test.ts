import { describe, expect, it } from 'vitest';

import { resolveIspSelection } from '@/lib/clients/isp-selection';

// Regression guard for the selector that made every operator unpickable: the
// stored value never contains "all", so the previous implementation read each
// pick as "the user chose All networks" and reset the selection.
describe('resolveIspSelection', () => {
  it('keeps the operator picked while "All networks" is displayed', () => {
    expect(resolveIspSelection([], ['all', 'mci'])).toEqual(['mci']);
  });

  it('supports picking many operators one after another', () => {
    let value = resolveIspSelection([], ['all', 'mci']);
    value = resolveIspSelection(value, ['mci', 'irancell']);
    value = resolveIspSelection(value, ['mci', 'irancell', 'shatel']);
    expect(value).toEqual(['mci', 'irancell', 'shatel']);
  });

  it('clears the restriction when "All networks" is picked again', () => {
    expect(resolveIspSelection(['mci', 'irancell'], ['mci', 'irancell', 'all'])).toEqual([]);
  });

  it('clearing the field means no restriction', () => {
    expect(resolveIspSelection(['mci'], [])).toEqual([]);
  });

  it('removing one of several operators keeps the rest', () => {
    expect(resolveIspSelection(['mci', 'irancell'], ['mci'])).toEqual(['mci']);
  });
});
