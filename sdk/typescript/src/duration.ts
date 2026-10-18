// parseDuration converts a number of seconds, or a string such as "30s",
// "5m", "1h", or "500ms", into whole seconds. The server only accepts seconds.
export function parseDuration(value: number | string): number {
  if (typeof value === "number") {
    if (!Number.isFinite(value) || value < 0) {
      throw new Error(`invalid duration ${value}`)
    }
    return Math.floor(value)
  }
  const match = /^(\d+(?:\.\d+)?)(ms|s|m|h)?$/.exec(value.trim())
  if (!match) {
    throw new Error(`invalid duration ${JSON.stringify(value)}`)
  }
  const amount = Number(match[1])
  const unit = match[2] ?? "s"
  let seconds = amount
  if (unit === "ms") seconds = amount / 1000
  if (unit === "m") seconds = amount * 60
  if (unit === "h") seconds = amount * 3600
  if (seconds > 0 && seconds < 1) return 1
  return Math.floor(seconds)
}
