import type { Log } from '../schema.ts'
import { countLabels, nextLabels, reasonLabels, stageLabels, statusLabels } from './flow-catalog.ts'
export function readableLog(log: Partial<Log>): string {
  if (!log.stage || !log.status)
    return [
      log.module,
      log.code,
      log.systemCode,
      log.httpStatus ? `HTTP ${log.httpStatus}` : undefined,
    ]
      .filter(Boolean)
      .join(' · ')
  const result = [
    `${statusLabels[log.status] ?? log.status}：${stageLabels[log.stage] ?? log.stage}`,
  ]
  if (log.role)
    result.push(
      `用途=${({ planner: '判斷參與', thinker: '生成回覆', learning: '背景學習', vision: '圖片辨識', embedding: '記憶向量', models: '探索模型' } as Record<string, string>)[log.role] ?? log.role}`,
    )
  if (log.apiModel || log.modelId) result.push(`模型=${log.apiModel ?? log.modelId}`)
  if (log.toolName) result.push(`工具=${log.toolName}`)
  if (log.reason)
    result.push(
      `原因：${reasonLabels[log.reason] ?? (/^provider_http_[1-5]\d\d$/.test(log.reason) ? `模型提供商回傳 HTTP ${log.reason.slice(-3)}` : `未分類狀態（${log.reason}）`)}`,
    )
  for (const [key, value] of Object.entries(log.counts ?? {}))
    result.push(
      `${log.mode === 'simulation' && key === 'sentSegments' ? '模擬完成分段數' : (countLabels[key] ?? key)}=${value}`,
    )
  if (log.outputKind)
    result.push(
      `輸出類型=${({ text: '文字', tool_calls: '工具呼叫', text_and_tools: '文字及工具呼叫', empty: '空結果', embeddings: '向量', model_list: '模型列表' } as Record<string, string>)[log.outputKind] ?? log.outputKind}`,
    )
  if (log.durationMs !== undefined) result.push(`耗時=${log.durationMs}ms`)
  if (log.systemCode) result.push(`系統=${log.systemCode}`)
  if (log.httpStatus) result.push(`HTTP=${log.httpStatus}`)
  if (log.nextStep) result.push(`下一步：${nextLabels[log.nextStep] ?? log.nextStep}`)
  return result.join('；')
}
export function stdoutLog(log: Log): string {
  const correlation = [
    log.traceId ? `追蹤=${log.traceId}` : undefined,
    log.turnId ? `回合=${log.turnId}` : undefined,
    log.batchId ? `學習批次=${log.batchId}` : undefined,
  ]
    .filter(Boolean)
    .join(' ')
  return `${log.at} ${log.level.toUpperCase()} ${correlation ? `[${correlation}] ` : ''}${log.message ?? readableLog(log)} | ${JSON.stringify(log)}\n`
}
