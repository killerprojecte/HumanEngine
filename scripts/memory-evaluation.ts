import { runMemoryEvaluation } from '../src/memory/evaluation/runner.ts'
import { serializeMemoryEvaluation } from '../src/memory/evaluation/reporter.ts'

// No provider, endpoint, credential or environment lookup is available from this CLI.
const args = process.argv.slice(2)
if (args.length > 1 || (args.length === 1 && args[0] !== '--dry-run')) {
  process.stderr.write(
    'Only --dry-run is supported; real model evaluation requires an explicitly authorized host adapter.\n',
  )
  process.exitCode = 2
} else {
  try {
    const report = await runMemoryEvaluation()
    process.stdout.write(serializeMemoryEvaluation(report))
    if (report.status !== 'completed') process.exitCode = 1
  } catch {
    process.stderr.write(
      'Synthetic evaluation failed; inspect local tests without exposing raw errors.\n',
    )
    process.exitCode = 1
  }
}
