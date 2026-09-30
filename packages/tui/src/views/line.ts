// A styled line is a list of segments. Views build lines; the Ink app only paints them.

export interface Seg {
  readonly text: string
  readonly color?: string | undefined
  readonly dim?: boolean | undefined
  readonly bold?: boolean | undefined
  readonly inverse?: boolean | undefined
}
export type Line = readonly Seg[]

/** A view's output: lines, and for each line the message id it shows (for selection). */
export interface Rendered {
  readonly lines: readonly Line[]
  readonly ids: readonly (string | undefined)[]
}

export const seg = (text: string, style: Omit<Seg, 'text'> = {}): Seg => ({ text, ...style })

export function plain(l: Line): string {
  return l.map((s) => s.text).join('')
}

/** Length in code points. Views use only single-width characters for their own drawing. */
export function len(s: string): number {
  return [...s].length
}

/** Cut to `width` code points (with an ellipsis when cut), then pad to `width`. */
export function fit(s: string, width: number): string {
  if (width <= 0) return ''
  const cps = [...oneLine(s)]
  const cut = cps.length > width ? `${cps.slice(0, width - 1).join('')}…` : cps.join('')
  return cut + ' '.repeat(width - len(cut))
}

/** Cut a whole line to `width` code points; pad it with spaces. */
export function fitLine(l: Line, width: number): Line {
  const out: Seg[] = []
  let left = width
  for (const s of l) {
    if (left <= 0) break
    const cps = [...oneLine(s.text)]
    if (cps.length <= left) {
      out.push({ ...s, text: cps.join('') })
      left -= cps.length
    } else {
      out.push({ ...s, text: `${cps.slice(0, Math.max(0, left - 1)).join('')}…` })
      left = 0
    }
  }
  if (left > 0) out.push(seg(' '.repeat(left)))
  return out
}

/** Newlines and tabs would break the layout. */
export function oneLine(s: string): string {
  return s.replace(/\r?\n/g, ' ⏎ ').replace(/\t/g, ' ')
}

/** HH:MM:SS of an ISO time (UTC, so that output does not depend on the host). */
export function clock(iso: string): string {
  return iso.slice(11, 19)
}

/** A short age: 42s, 3m, 2h, 5d. */
export function age(sinceIso: string, now: number): string {
  const s = Math.max(0, Math.round((now - Date.parse(sinceIso)) / 1000))
  if (s < 60) return `${s}s`
  if (s < 3600) return `${Math.floor(s / 60)}m`
  if (s < 86_400) return `${Math.floor(s / 3600)}h`
  return `${Math.floor(s / 86_400)}d`
}

/** Wrap text to lines of at most `width` code points. */
export function wrap(text: string, width: number): string[] {
  const out: string[] = []
  for (const para of text.split(/\r?\n/)) {
    let cur = ''
    for (const word of para.split(' ')) {
      const cand = cur === '' ? word : `${cur} ${word}`
      if (len(cand) <= width) {
        cur = cand
        continue
      }
      if (cur !== '') out.push(cur)
      let w = [...word]
      while (w.length > width) {
        out.push(w.slice(0, width).join(''))
        w = w.slice(width)
      }
      cur = w.join('')
    }
    out.push(cur)
  }
  return out
}

export const STATE_COLOR: Record<string, string> = {
  working: 'green',
  blocked: 'red',
  done: 'blue',
  idle: 'gray',
  left: 'gray',
  unknown: 'gray',
}

/** A one-line placeholder, fitted to the width like every other line. */
export function note(text: string, width: number): Rendered {
  return { lines: [fitLine([seg(text, { dim: true })], width)], ids: [undefined] }
}
