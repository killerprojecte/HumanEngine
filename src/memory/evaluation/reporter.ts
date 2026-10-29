import type { EvaluationReport } from './runner.ts'

/** JSON keeps model text as data; renderers must not interpret it as HTML or instructions. */
export function serializeMemoryEvaluation(report: EvaluationReport): string {
  return `${JSON.stringify(report, null, 2)}\n`
}
