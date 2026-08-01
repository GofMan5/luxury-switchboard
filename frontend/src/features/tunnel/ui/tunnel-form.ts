const publisherProfilePattern = /^v1\.2\d{4}\.[0-9a-f]{48}$/u

export function publisherProfileIsValid(value: string): boolean {
  return !value.trim() || publisherProfilePattern.test(value.trim())
}

export function tunnelLimitsAreValid(port: string, rpm: string, contextMiB: string, publisherProfile = ''): boolean {
  if (!port.trim() || !rpm.trim() || !contextMiB.trim()) return false
  const parsedPort = Number(port)
  const parsedRPM = Number(rpm)
  const parsedContext = Number(contextMiB)
  return Number.isInteger(parsedPort) && parsedPort >= 1 && parsedPort <= 65_535 &&
    Number.isInteger(parsedRPM) && parsedRPM >= 0 && parsedRPM <= 1_000_000 &&
    Number.isFinite(parsedContext) && parsedContext >= 0 && parsedContext <= 2_048 &&
    publisherProfileIsValid(publisherProfile)
}
