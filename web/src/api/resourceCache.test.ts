import { beforeEach, describe, expect, it, vi } from 'vitest'
import {
  clearResourceCache,
  invalidateResource,
  invalidateResourcePrefix,
  loadResource,
  readResource,
  resourceFlightTimeoutMs,
} from './resourceCache'

describe('resourceCache', () => {
  beforeEach(clearResourceCache)

  it('returns fresh values without reloading and retains stale values', async () => {
    let now = 100
    const loader = vi.fn(async () => 'value')
    await loadResource('one', 10, loader, false, () => now)
    expect(readResource<string>('one', now)).toEqual({ data: 'value', fresh: true })
    await loadResource('one', 10, loader, false, () => now)
    expect(loader).toHaveBeenCalledTimes(1)
    now = 111
    expect(readResource<string>('one', now)).toEqual({ data: 'value', fresh: false })
  })

  it('deduplicates in-flight loads', async () => {
    let resolve!: (value: string) => void
    const loader = vi.fn(() => new Promise<string>((done) => { resolve = done }))
    const first = loadResource('one', 10, loader)
    const second = loadResource('one', 10, loader)
    expect(first).toBe(second)
    expect(loader).toHaveBeenCalledTimes(1)
    resolve('done')
    await expect(first).resolves.toBe('done')
  })

  it('supports exact, prefix and full invalidation', async () => {
    await loadResource('alias:a', 10, async () => 'a')
    await loadResource('alias:b', 10, async () => 'b')
    await loadResource('account', 10, async () => 'account')
    invalidateResource('alias:a')
    expect(readResource('alias:a')).toBeUndefined()
    invalidateResourcePrefix('alias:')
    expect(readResource('alias:b')).toBeUndefined()
    expect(readResource('account')).toBeDefined()
    clearResourceCache()
    expect(readResource('account')).toBeUndefined()
  })

  it('does not let an invalidated request overwrite its replacement', async () => {
    let resolveOld!: (value: string) => void
    const old = loadResource('one', 100, () => new Promise<string>((done) => { resolveOld = done }))
    invalidateResource('one')
    await loadResource('one', 100, async () => 'new')
    resolveOld('old')
    await old
    expect(readResource<string>('one')?.data).toBe('new')
  })

  it('shares an in-flight request even when a refresh is requested', async () => {
    let resolve!: (value: string) => void
    const loader = vi.fn(() => new Promise<string>((done) => { resolve = done }))
    const first = loadResource('one', 100, loader)
    const refresh = loadResource('one', 100, loader, true)
    expect(refresh).toBe(first)
    expect(loader).toHaveBeenCalledTimes(1)
    resolve('done')
    await refresh
  })

  it('lets a forced refresh replace a timed-out flight without stale writeback', async () => {
    let now = 1_000
    let resolveOld!: (value: string) => void
    let resolveNew!: (value: string) => void
    const oldLoader = vi.fn(() => new Promise<string>((done) => { resolveOld = done }))
    const newLoader = vi.fn(() => new Promise<string>((done) => { resolveNew = done }))

    const oldFlight = loadResource('one', 100, oldLoader, false, () => now)
    now += resourceFlightTimeoutMs - 1
    const joinedFlight = loadResource('one', 100, newLoader, true, () => now)
    expect(joinedFlight).toBe(oldFlight)
    expect(newLoader).not.toHaveBeenCalled()

    now += 2
    const replacement = loadResource('one', 100, newLoader, true, () => now)
    expect(replacement).not.toBe(oldFlight)
    expect(newLoader).toHaveBeenCalledTimes(1)

    resolveNew('new')
    await expect(replacement).resolves.toBe('new')
    resolveOld('old')
    await expect(oldFlight).resolves.toBe('old')
    expect(readResource<string>('one', now)?.data).toBe('new')
  })

  it('lets a normal load replace a timed-out flight while still joining before timeout', async () => {
    let now = 2_000
    let resolveOld!: (value: string) => void
    let resolveNew!: (value: string) => void
    const oldLoader = vi.fn(() => new Promise<string>((done) => { resolveOld = done }))
    const newLoader = vi.fn(() => new Promise<string>((done) => { resolveNew = done }))

    const oldFlight = loadResource('one', 100, oldLoader, false, () => now)
    now += resourceFlightTimeoutMs - 1
    const joinedFlight = loadResource('one', 100, newLoader, false, () => now)
    expect(joinedFlight).toBe(oldFlight)
    expect(newLoader).not.toHaveBeenCalled()

    now += 2
    const replacement = loadResource('one', 100, newLoader, false, () => now)
    expect(replacement).not.toBe(oldFlight)
    expect(newLoader).toHaveBeenCalledTimes(1)

    resolveNew('new')
    await expect(replacement).resolves.toBe('new')
    resolveOld('old')
    await expect(oldFlight).resolves.toBe('old')
    expect(readResource<string>('one', now)?.data).toBe('new')
  })
})
