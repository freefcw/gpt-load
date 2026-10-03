const maxImportBytes = 32 * 1024 * 1024
const maxImportCredentials = 5000

export interface APIKeyFileImport {
  credentials: string
  count: number
}

export class APIKeyFileImportError extends Error {
  constructor(readonly code: 'empty' | 'too_large' | 'too_many' | 'read_failed') {
    super(code)
  }
}

// 服务端只在整段正文是一个 JSON 数组时才展开。多文件要先拆成逐行条目再拼接。
function credentialLine(value: unknown): string {
  if (typeof value !== 'string') return JSON.stringify(value)
  const trimmed = value.trim()
  if (!trimmed) return ''
  if (!/[\r\n]/.test(value)) return value
  if (trimmed.startsWith('{')) {
    try {
      const parsed: unknown = JSON.parse(trimmed)
      if (parsed && typeof parsed === 'object' && !Array.isArray(parsed))
        return JSON.stringify(parsed)
    } catch {
      // 含换行的非法对象不能再按行拆，压成单行密钥。
    }
  }
  return JSON.stringify({ api_key: value })
}

export async function readAPIKeyCredentialFiles(files: readonly File[]): Promise<APIKeyFileImport> {
  if (files.reduce((size, file) => size + file.size, 0) > maxImportBytes)
    throw new APIKeyFileImportError('too_large')

  const contents: string[] = []
  for (const file of files) {
    let content: string
    try {
      content = new TextDecoder('utf-8', { fatal: true }).decode(await file.arrayBuffer()).trim()
    } catch {
      throw new APIKeyFileImportError('read_failed')
    }
    if (!content) continue
    let parsed: unknown
    try {
      parsed = JSON.parse(content)
    } catch {
      // 非完整 JSON 沿用多行输入规则，由服务端逐行校验。
    }
    if (Array.isArray(parsed)) {
      for (const item of parsed) contents.push(credentialLine(item))
      continue
    }
    if (parsed && typeof parsed === 'object') {
      // 每份格式化 JSON 压成一行，合并后仍沿用现有文本导入合同。
      content = JSON.stringify(parsed)
    }
    contents.push(content)
  }

  const credentials = contents.join('\n')
  const count = credentials.split('\n').filter((line) => line.trim()).length
  if (!count) throw new APIKeyFileImportError('empty')
  if (count > maxImportCredentials) throw new APIKeyFileImportError('too_many')
  if (new TextEncoder().encode(JSON.stringify({ credentials })).length > maxImportBytes)
    throw new APIKeyFileImportError('too_large')
  return { credentials, count }
}
