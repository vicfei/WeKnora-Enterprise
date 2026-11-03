package web_search

import (
	"context"
	"strings"

	"github.com/Tencent/WeKnora/internal/types"
)

// maxActorIDLen bounds the outbound actor identifier. It mirrors the auth
// middleware's external-user-id cap so anything that passed inbound
// validation always fits the outbound header.
const maxActorIDLen = 128

// DMZ audit header name, per the gateway contract (2026-10-27).
const DMZExternalUserIDHeader = "X-External-User-ID"

// DefaultDMZActorID is the fixed fallback when no end-user identity resolves
// (service/background calls), per the DMZ gateway contract: "weknora".
const DefaultDMZActorID = "weknora"

// RequestActorFromContext resolves the END-USER identity that initiated this
// search, for downstream internet-access audit (v3.1.3, DMZ contract 2026-10-27):
//
//   - API-key channel with an external user principal (direct header or
//     signed token): the external user id — for the Java portal this is the
//     UM 号 carried as the token subject. The principal ID is
//     "<tenantID>:<externalUserID>", so the id after the first colon is the
//     verbatim external id.
//   - Native web session: the logged-in user's username (their account).
//   - Service identities (tenant/platform API key without external user,
//     IM, embed): "weknora" (the DMZ contract's fixed fallback).
//
// The DMZ gateway requires the value LOWERCASED (English letters); the result
// is attached to the outbound provider request as the X-External-User-ID header.
func RequestActorFromContext(ctx context.Context) string {
	actor := ""
	if principal, ok := types.PrincipalFromContext(ctx); ok {
		if principal.Type == types.PrincipalAPIExternalUser {
			if idx := strings.Index(principal.ID, ":"); idx >= 0 && idx+1 < len(principal.ID) {
				actor = principal.ID[idx+1:]
			}
		}
	}
	if actor == "" {
		if user, ok := ctx.Value(types.UserContextKey).(*types.User); ok && user != nil && strings.TrimSpace(user.Username) != "" {
			if principal, has := types.PrincipalFromContext(ctx); !has || principal.Type != types.PrincipalAPITenant {
				actor = user.Username
			}
		}
	}
	if actor == "" {
		return DefaultDMZActorID
	}
	return clampActorID(strings.ToLower(actor))
}

// clampActorID trims and bounds the identifier; anything beyond the cap is
// truncated rather than rejected so auditing never blocks a search.
func clampActorID(id string) string {
	id = strings.TrimSpace(id)
	if len(id) > maxActorIDLen {
		id = id[:maxActorIDLen]
	}
	return id
}
