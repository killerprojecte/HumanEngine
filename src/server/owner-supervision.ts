import { writeSync } from 'node:fs'

/** Only an unexpected exit of an already-ready runtime worker reaches these handlers.
 * Exit the host so its existing external process policy can act. No RPC is replayed. */
export function createOwnerFailureHandler(
  exit: (code: number) => unknown = (code) => process.exit(code),
): () => void {
  return createFailureHandler(
    '[HumanEngine] Engine owner exited unexpectedly; in-flight outcomes may be unknown. No operations will be replayed.\n',
    exit,
  )
}

export function createVerifierFailureHandler(
  exit: (code: number) => unknown = (code) => process.exit(code),
): () => void {
  return createFailureHandler(
    '[HumanEngine] Publication verifier exited unexpectedly; in-flight outcomes may be unknown. No operations will be replayed.\n',
    exit,
  )
}

function createFailureHandler(diagnostic: string, exit: (code: number) => unknown): () => void {
  let failed = false
  return () => {
    if (failed) return
    failed = true
    try {
      writeSync(2, diagnostic)
    } catch {
      // No raw worker error or payload is needed to report this fatal boundary.
    } finally {
      // exitCode alone would leave the HTTP listener and the surviving worker running.
      // This is not a graceful-drain guarantee; clients can observe a closed socket.
      exit(1)
    }
  }
}
