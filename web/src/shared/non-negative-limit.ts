// Parse the decimal form used by group and credential limit editors. Empty
// input is the zero sentinel; callers provide the domain-specific meaning of
// zero in their labels and API comments.
export function parseNonNegativeSafeInteger(raw: string): number {
  const trimmed = raw.trim()
  if (trimmed === '') return 0
  const value = Number(trimmed)
  return Number.isSafeInteger(value) && value >= 0 ? value : Number.NaN
}
