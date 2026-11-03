/**
 * UUM bridge login callback (second-development feature, decision 020).
 *
 * The external Java portal authenticates the user (via POST
 * /api/v1/identity/bridge with its platform key) and redirects the browser
 * to this frontend with a hash fragment:
 *
 *   https://<frontend>/#bridge_result=<base64url(JSON)>
 *
 * where JSON is `{token: string, refresh_token?: string}` — the bridge
 * login response. The hash (not a query param) keeps the token out of
 * server access logs; the App-level consumer clears it from the address
 * bar and history as soon as it is consumed.
 *
 * Security rules: payloads are never logged, never sent to analytics, and
 * a malformed payload only yields a generic error message.
 */

export interface BridgeLoginPayload {
  token: string
  refresh_token?: string
}

export class BridgeCallbackError extends Error {}

/** base64url → JSON. Throws BridgeCallbackError on malformed input. */
export function decodeBridgeResult(encoded: string): BridgeLoginPayload {
  try {
    const normalized = encoded.replace(/-/g, '+').replace(/_/g, '/')
    const padded = normalized + '='.repeat((4 - (normalized.length % 4)) % 4)
    const binary = window.atob(padded)
    const bytes = Uint8Array.from(binary, (char) => char.charCodeAt(0))
    const parsed = JSON.parse(new TextDecoder().decode(bytes))
    if (!parsed || typeof parsed !== 'object' || typeof parsed.token !== 'string' || !parsed.token) {
      throw new BridgeCallbackError('malformed payload')
    }
    return {
      token: parsed.token,
      refresh_token: typeof parsed.refresh_token === 'string' ? parsed.refresh_token : undefined,
    }
  } catch (error) {
    if (error instanceof BridgeCallbackError) throw error
    throw new BridgeCallbackError('malformed payload')
  }
}

/** The encoded bridge_result value from the current URL hash, or null. */
export function readBridgeResultFromHash(): string | null {
  if (typeof window === 'undefined') return null
  const hash = window.location.hash.startsWith('#') ? window.location.hash.slice(1) : ''
  if (!hash) return null
  const encoded = new URLSearchParams(hash).get('bridge_result')
  return encoded && encoded.length > 0 ? encoded : null
}

/** Whether the current hash carries a bridge error flag. */
export function hasBridgeErrorInHash(): boolean {
  if (typeof window === 'undefined') return false
  const hash = window.location.hash || ''
  return hash.includes('bridge_error=')
}

/** Strip the callback hash from the URL bar and history. */
export function clearBridgeCallbackHash(path = '/'): void {
  window.history.replaceState({}, document.title, path)
}
