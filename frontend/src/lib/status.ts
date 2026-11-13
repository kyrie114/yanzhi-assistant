export type DocStatus = '待处理' | '解析中' | '向量化中' | '已完成' | '失败';

const TERMINAL_STATUSES: ReadonlySet<string> = new Set(['已完成', '失败']);

function isTerminal(status: string): boolean {
  return TERMINAL_STATUSES.has(status);
}

export function needsPolling(docs: readonly { status: string }[]): boolean {
  return docs.some((doc) => !isTerminal(doc.status));
}
