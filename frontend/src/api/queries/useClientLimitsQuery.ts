import { useQuery } from '@tanstack/react-query';

import { HttpUtil } from '@/utils';
import { keys } from '@/api/queryKeys';

export interface ISPEntry {
  id: string;
  nameEn: string;
  nameFa: string;
  kind: 'mobile' | 'fixed' | 'wireless' | 'satellite';
  asns?: number[];
  prefixes: number;
  note?: string;
}

export interface SpeedLevelOption {
  level: number;
  mbps: number;
}

export interface ShapingCapability {
  available: boolean;
  reason?: string;
  interface?: string;
}

export interface ClientLimitsCapabilities {
  isps: ISPEntry[];
  speedLadder: SpeedLevelOption[];
  shaping: ShapingCapability;
  multiplier: { min: number; max: number };
  ispLock?: { groups: number; clients: number; skipped?: string[]; lastError?: string };
}

// Fallback used until the request resolves (or when it fails): an empty catalog
// with shaping reported as unavailable, so the editor never claims a limit is
// enforced when it does not know.
const EMPTY: ClientLimitsCapabilities = {
  isps: [],
  speedLadder: [
    { level: 0, mbps: 0 },
    { level: 1, mbps: 100 },
    { level: 2, mbps: 50 },
    { level: 3, mbps: 25 },
    { level: 4, mbps: 10 },
    { level: 5, mbps: 5 },
    { level: 6, mbps: 2 },
    { level: 7, mbps: 1 },
    { level: 8, mbps: 0.5 },
  ],
  shaping: { available: false },
  multiplier: { min: 0.1, max: 100 },
};

async function fetchClientLimits(): Promise<ClientLimitsCapabilities> {
  const msg = await HttpUtil.get<ClientLimitsCapabilities>('/panel/api/server/ispCatalog', undefined, { silent: true });
  if (!msg?.success || !msg.obj) throw new Error(msg?.msg || 'Failed to fetch client limit capabilities');
  return { ...EMPTY, ...msg.obj };
}

export function useClientLimitsQuery(): ClientLimitsCapabilities {
  const query = useQuery({
    queryKey: keys.server.clientLimits(),
    queryFn: fetchClientLimits,
    staleTime: 300_000,
  });
  return query.data ?? EMPTY;
}
