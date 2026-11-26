// 与后端 parseHeaderRules 和经典编辑器一致：set、default、remove 共用一个不区分大小写的名称空间。
const headerNamePattern = /^[!#$%&'*+.^_`|~\w-]+$/u

export function validGroupHeaderName(name: string): boolean {
  return headerNamePattern.test(name.trim())
}

export function duplicateGroupHeaderNames(
  headers: readonly { name: string }[],
  removeHeaders: string,
): boolean {
  const names = [
    ...headers.map((header) => header.name.trim()),
    ...removeHeaders
      .split(/\r?\n/u)
      .map((line) => line.trim())
      .filter(Boolean),
  ].filter((name) => headerNamePattern.test(name))
  const folded = names.map((name) => name.toLowerCase())
  return new Set(folded).size !== folded.length
}
