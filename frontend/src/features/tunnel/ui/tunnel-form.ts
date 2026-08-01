export function tunnelLimitsAreValid(port: string, rpm: string, contextMiB: string): boolean {
  if (!port.trim() || !rpm.trim() || !contextMiB.trim()) return false
  const parsedPort = Number(port)
  const parsedRPM = Number(rpm)
  const parsedContext = Number(contextMiB)
  return Number.isInteger(parsedPort) && parsedPort >= 1 && parsedPort <= 65_535 &&
    Number.isInteger(parsedRPM) && parsedRPM >= 0 && parsedRPM <= 1_000_000 &&
    Number.isFinite(parsedContext) && parsedContext >= 0 && parsedContext <= 2_048
}
