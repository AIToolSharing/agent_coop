// Tokens. A token is `<name>.<secret>`; the hub stores only SHA-256(secret), in bucket coop_tokens
// under key <name>, with the token's role. A name has at most one valid token. A machine token lets
// agents on that machine act; an operator token opens the admin API (the TUI).
import { createHash, randomBytes, timingSafeEqual } from 'node:crypto'
import { decode, encode, isToken, TokenRecord, type TokenRole } from '@coop/core'
import type { KV } from '@nats-io/kv'

const sha256 = (s: string) => createHash('sha256').update(s).digest('hex')

/** Who a token belongs to. */
export interface TokenOwner {
  readonly name: string
  readonly role: TokenRole
}

/** Create a new token for `name`. It replaces the old token of that name, if any. */
export async function issueToken(
  tokens: KV,
  name: string,
  role: TokenRole = 'machine',
): Promise<string> {
  if (!isToken(name)) throw new Error(`invalid token name: ${name}`)
  const secret = randomBytes(32).toString('base64url')
  const r: TokenRecord = { sha256: sha256(secret), role, created_at: new Date().toISOString() }
  await tokens.put(name, encode(TokenRecord, r))
  return `${name}.${secret}`
}

export async function revokeToken(tokens: KV, name: string): Promise<boolean> {
  const r = await readRecord(tokens, name)
  if (r === undefined || r.revoked_at !== undefined) return false
  await tokens.put(name, encode(TokenRecord, { ...r, revoked_at: new Date().toISOString() }))
  return true
}

export interface TokenRow {
  readonly name: string
  readonly role: TokenRole
  readonly created_at: string
  readonly revoked_at?: string
}

export async function listTokens(tokens: KV): Promise<TokenRow[]> {
  const out: TokenRow[] = []
  for await (const name of await tokens.keys()) {
    const r = await readRecord(tokens, name)
    if (r === undefined) continue
    const row: TokenRow = { name, role: r.role, created_at: r.created_at }
    out.push(r.revoked_at === undefined ? row : { ...row, revoked_at: r.revoked_at })
  }
  return out.sort((a, b) => a.name.localeCompare(b.name))
}

/** The owner of a bearer token, or undefined if the token is not valid. */
export async function verifyToken(tokens: KV, token: string): Promise<TokenOwner | undefined> {
  const dot = token.indexOf('.')
  if (dot < 1) return undefined
  const name = token.slice(0, dot)
  const secret = token.slice(dot + 1)
  if (!isToken(name) || secret.length === 0) return undefined
  const r = await readRecord(tokens, name)
  if (r === undefined || r.revoked_at !== undefined) return undefined
  const a = Buffer.from(sha256(secret), 'hex')
  const b = Buffer.from(r.sha256, 'hex')
  return a.length === b.length && timingSafeEqual(a, b) ? { name, role: r.role } : undefined
}

async function readRecord(tokens: KV, name: string): Promise<TokenRecord | undefined> {
  const e = await tokens.get(name)
  return e?.operation === 'PUT' ? decode(TokenRecord, e.value) : undefined
}
