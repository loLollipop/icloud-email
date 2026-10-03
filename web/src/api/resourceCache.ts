export interface ResourceSnapshot<T> {
  data: T
  fresh: boolean
}

interface CacheEntry<T> {
  data?: T
  expiresAt: number
  promise?: Promise<T>
  startedAt?: number
}

const entries = new Map<string, CacheEntry<unknown>>()
export const resourceFlightTimeoutMs = 30_000

/** Read the last successful value. Expired values remain available as stale data. */
export function readResource<T>(key: string, now = Date.now()): ResourceSnapshot<T> | undefined {
  const entry = entries.get(key) as CacheEntry<T> | undefined
  if (!entry || entry.data === undefined) return undefined
  return { data: entry.data, fresh: entry.expiresAt > now }
}

/**
 * Load a resource, sharing a recent in-flight request for the same key. Any
 * caller supersedes a timed-out request without allowing stale writeback.
 */
export function loadResource<T>(
  key: string,
  ttlMs: number,
  loader: () => Promise<T>,
  force = false,
  now = Date.now,
): Promise<T> {
  const current = entries.get(key) as CacheEntry<T> | undefined
  const currentTime = now()
  // Keep short-lived navigation requests deduplicated. Any caller may replace
  // a stuck flight after the timeout; entry identity prevents the old promise
  // from overwriting the replacement when it eventually settles.
  if (
    current?.promise
    && (current.startedAt === undefined || currentTime - current.startedAt < resourceFlightTimeoutMs)
  ) return current.promise
  if (!force && !current?.promise) {
    if (current?.data !== undefined && current.expiresAt > currentTime) {
      return Promise.resolve(current.data)
    }
  }

  const next: CacheEntry<T> = {
    data: current?.data,
    expiresAt: current?.expiresAt ?? 0,
    startedAt: currentTime,
  }
  const promise = loader().then(
    (data) => {
      if (entries.get(key) === next) {
        next.data = data
        next.expiresAt = now() + ttlMs
        next.promise = undefined
        next.startedAt = undefined
      }
      return data
    },
    (error: unknown) => {
      if (entries.get(key) === next) {
        next.promise = undefined
        next.startedAt = undefined
      }
      throw error
    },
  )
  next.promise = promise
  entries.set(key, next as CacheEntry<unknown>)
  return promise
}

export function invalidateResource(key: string): void {
  entries.delete(key)
}

export function invalidateResourcePrefix(prefix: string): void {
  for (const key of entries.keys()) {
    if (key.startsWith(prefix)) entries.delete(key)
  }
}

export function clearResourceCache(): void {
  entries.clear()
}

export const resourceKeys = {
  accounts: 'accounts',
  aliases: (accountID: string) => `aliases:${accountID}`,
  inbox: (query: string) => `inbox:${query}`,
  inboxAccountPrefix: (accountID: string) => `inbox:account_id=${encodeURIComponent(accountID)}&`,
}

export const resourceTTLs = {
  accounts: 5 * 60_000,
  aliases: 30_000,
  inbox: 8_000,
}
