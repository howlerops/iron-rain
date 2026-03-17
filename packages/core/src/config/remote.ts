/**
 * Remote config fetcher — fetches and merges team/org config from a URL.
 */

export interface RemoteConfigResult {
  config: Record<string, unknown>;
  fetchedAt: number;
  source: string;
}

const CACHE_TTL_MS = 3600_000; // 1 hour

let cachedRemote: RemoteConfigResult | null = null;

/**
 * Fetch remote config from a URL. Results are cached for 1 hour.
 */
export async function fetchRemoteConfig(
  url: string,
  force = false,
): Promise<RemoteConfigResult | null> {
  // Return cached if still fresh
  if (
    !force &&
    cachedRemote &&
    Date.now() - cachedRemote.fetchedAt < CACHE_TTL_MS
  ) {
    return cachedRemote;
  }

  try {
    const response = await fetch(url, {
      headers: { Accept: "application/json" },
      signal: AbortSignal.timeout(10000),
    });

    if (!response.ok) {
      process.stderr.write(
        `[config] Remote config fetch failed: ${response.status} ${response.statusText}\n`,
      );
      return cachedRemote;
    }

    const config = (await response.json()) as Record<string, unknown>;
    cachedRemote = { config, fetchedAt: Date.now(), source: url };
    return cachedRemote;
  } catch (err) {
    process.stderr.write(
      `[config] Remote config fetch error: ${err instanceof Error ? err.message : String(err)}\n`,
    );
    return cachedRemote;
  }
}

/**
 * Deep merge two config objects. Local values win on conflict.
 */
export function deepMergeConfig(
  remote: Record<string, unknown>,
  local: Record<string, unknown>,
): Record<string, unknown> {
  const result = { ...remote };

  for (const [key, localValue] of Object.entries(local)) {
    if (localValue === undefined) continue;

    const remoteValue = result[key];
    if (
      typeof localValue === "object" &&
      localValue !== null &&
      !Array.isArray(localValue) &&
      typeof remoteValue === "object" &&
      remoteValue !== null &&
      !Array.isArray(remoteValue)
    ) {
      result[key] = deepMergeConfig(
        remoteValue as Record<string, unknown>,
        localValue as Record<string, unknown>,
      );
    } else {
      result[key] = localValue;
    }
  }

  return result;
}

/**
 * Clear the remote config cache.
 */
export function clearRemoteConfigCache(): void {
  cachedRemote = null;
}
