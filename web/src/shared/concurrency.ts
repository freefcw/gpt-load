import { InvalidResponseError } from './http/errors'

export interface ConcurrencyView {
  current: number
  limit: number
}

export function readConcurrency(value: unknown): ConcurrencyView {
  if (!value || typeof value !== 'object') throw new InvalidResponseError()
  const row = value as Record<string, unknown>
  for (const key of ['current', 'limit']) {
    const candidate = row[key]
    if (typeof candidate !== 'number' || !Number.isSafeInteger(candidate) || candidate < 0) {
      throw new InvalidResponseError()
    }
  }
  return { current: row.current as number, limit: row.limit as number }
}
