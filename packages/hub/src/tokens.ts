// Machine tokens. A token is `<machine>.<secret>`; the hub stores only SHA-256(secret), in bucket
// coop_tokens under key <machine>. One machine has at most one valid token.
import { createHash, randomBytes, timingSafeEqual } from 'node:crypto'
import { decode, encode, isToken, TokenRecord } from '@coop/core'
import type { KV } from '@nats-io/kv'

const sha256 = (s: string) => createHash('sha256').update(s).digest('hex')

/** Create a new token for `machine`. It replaces the old token of that machine, if any. */
export async function issueToken(tokens: KV, machine: string): Promise<string> {
  if (!isToken(machine)) throw new Error(`invalid machine name: ${machine}`)
  const secret = randomBytes(32).toString('base64url')
  const r: TokenRecord = { sha256: sha256(secret), created_at: new Date().toISOString() }
  await tokens.put(machine, encode(TokenRecord, r))
  return `${machine}.${secret}`
}

export async function revokeToken(tokens: KV, machine: string): Promise<boolean> {
  const r = await readRecord(tokens, machine)
  if (r === undefined || r.revoked_at !== undefined) return false
  await tokens.put(machine, encode(TokenRecord, { ...r, revoked_at: new Date().toISOString() }))
  return true
}

export async function listTokens(
  tokens: KV,
): Promise<{ machine: string; created_at: string; revoked_at?: string }[]> {
  const out = []
  for await (const machine of await tokens.keys()) {
    const r = await readRecord(tokens, machine)
    if (r === undefined) continue
    const row = { machine, created_at: r.created_at }
    out.push(r.revoked_at === undefined ? row : { ...row, revoked_at: r.revoked_at })
  }
  return out.sort((a, b) => a.machine.localeCompare(b.machine))
}

/** The machine that a bearer token belongs to, or undefined if the token is not valid. */
export async function verifyToken(tokens: KV, token: string): Promise<string | undefined> {
  const dot = token.indexOf('.')
  if (dot < 1) return undefined
  const machine = token.slice(0, dot)
  const secret = token.slice(dot + 1)
  if (!isToken(machine) || secret.length === 0) return undefined
  const r = await readRecord(tokens, machine)
  if (r === undefined || r.revoked_at !== undefined) return undefined
  const a = Buffer.from(sha256(secret), 'hex')
  const b = Buffer.from(r.sha256, 'hex')
  return a.length === b.length && timingSafeEqual(a, b) ? machine : undefined
}

async function readRecord(tokens: KV, machine: string): Promise<TokenRecord | undefined> {
  const e = await tokens.get(machine)
  return e?.operation === 'PUT' ? decode(TokenRecord, e.value) : undefined
}
