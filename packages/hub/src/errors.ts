import type { ErrorCode } from '@coop/core'

const STATUS: Record<ErrorCode, 401 | 403 | 404 | 409 | 413 | 422 | 429 | 503> = {
  unauthorized: 401,
  forbidden: 403,
  not_found: 404,
  conflict: 409,
  too_large: 413,
  ambiguous: 409,
  invalid: 422,
  rate_limited: 429,
  unavailable: 503,
}

/** An error that the API returns to the client as `{ error, message }`. */
export class HubError extends Error {
  constructor(
    readonly code: ErrorCode,
    message: string,
  ) {
    super(message)
  }

  get status() {
    return STATUS[this.code]
  }
}
