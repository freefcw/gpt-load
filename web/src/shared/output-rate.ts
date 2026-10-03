export type OutputTiming = {
  protocol: string
  status_code: number
  duration_ms: number
  output_tokens: string
  usage_state: string
}

// Average output speed follows the server's duration_ms contract: it starts
// when a concrete credential is admitted and includes retry gaps, credential
// rotation and reasoning time. It is intentionally not a client round-trip or
// token-generation-only metric.
export function outputTokensPerSecond(row: OutputTiming): number | null {
  const tokens = Number(row.output_tokens)
  if (
    row.status_code < 200 ||
    row.status_code >= 300 ||
    !['openai-completions', 'openai-responses', 'anthropic', 'gemini'].includes(row.protocol) ||
    (row.usage_state !== 'complete' && row.usage_state !== 'partial') ||
    !Number.isSafeInteger(tokens) ||
    tokens <= 0
  ) {
    return null
  }
  return Number.isSafeInteger(row.duration_ms) && row.duration_ms > 0
    ? tokens / (row.duration_ms / 1000)
    : null
}
