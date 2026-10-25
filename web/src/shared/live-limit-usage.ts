export type LiveLimitUsageTone = 'unlimited' | 'success' | 'warning' | 'danger'

export interface LiveLimitUsage {
  unlimited: boolean
  remainingPercent: number
  tone: LiveLimitUsageTone
}

export function describeLiveLimitUsage(used: number, limit: number): LiveLimitUsage {
  if (limit <= 0) {
    return { unlimited: true, remainingPercent: 100, tone: 'unlimited' }
  }

  const remainingPercent = Math.max(0, Math.min(100, ((limit - used) / limit) * 100))
  let tone: Exclude<LiveLimitUsageTone, 'unlimited'> = 'success'
  if (used >= limit || remainingPercent < 30) {
    tone = 'danger'
  } else if (remainingPercent < 70) {
    tone = 'warning'
  }

  return { unlimited: false, remainingPercent, tone }
}
