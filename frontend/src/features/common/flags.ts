/**
 * Feature flags for the portal second-development features.
 *
 * Every flag defaults to OFF so the stock pages keep their exact behaviour.
 * A build opts in via the corresponding VITE_ env variable, e.g.
 * `VITE_ENABLE_TAG_TREE=1 npm run build`. Falsy spellings ("0", "false",
 * "off", "no") are treated as disabled so CI can explicitly override a
 * defaulted-on variable later without changing this module.
 */
const FALSY_SPELLINGS = new Set(['0', 'false', 'off', 'no'])

function enabled(value: string | undefined): boolean {
  return !!value && !FALSY_SPELLINGS.has(value.trim().toLowerCase())
}

/** Hierarchical tag tree UI (KB editor panel, pickers, chat mention browse). */
export function tagTreeEnabled(): boolean {
  return enabled(import.meta.env.VITE_ENABLE_TAG_TREE)
}

/** Portal knowledge base list via `GET /portal/knowledge-bases`. */
export function portalListEnabled(): boolean {
  return enabled(import.meta.env.VITE_ENABLE_PORTAL_LIST)
}

/** KB routing toggle in the chat KB selector (enable_kb_routing request flag). */
export function kbRoutingEnabled(): boolean {
  return enabled(import.meta.env.VITE_ENABLE_KB_ROUTING)
}

/** Weighted-ranking (rerank) toggle in the chat input toolbar (rerank_model_id request field). */
export function weightedRankingEnabled(): boolean {
  return enabled(import.meta.env.VITE_ENABLE_WEIGHTED_RANKING)
}

/** UUM bridge login entry (#bridge_result= hash callback + login button). */
export function bridgeLoginEnabled(): boolean {
  return enabled(import.meta.env.VITE_ENABLE_BRIDGE_LOGIN)
}

/** Doc-versioning UI (as-new-version upload checkbox, pending queue, history). */
export function docVersionsEnabled(): boolean {
  return enabled(import.meta.env.VITE_ENABLE_DOC_VERSIONS)
}

/**
 * External portal login URL shown on the login page. Empty means the
 * "portal unified login" button stays hidden.
 */
export function portalLoginUrl(): string {
  return (import.meta.env.VITE_PORTAL_LOGIN_URL || '').trim()
}
