package web_search

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
)

// TestDoubaoProviderForwardsUserActorHeader verifies the v3.1.3 internet-access
// audit contract: the end-user identity resolved from the request context
// (the external user id — the Java portal's UM 号 — for API-key sessions,
// the username for native web sessions) is forwarded to the DMZ gateway as
// the X-External-User-ID header; pure service identities send no header.
func TestDoubaoProviderForwardsUserActorHeader(t *testing.T) {
	cases := []struct {
		name      string
		ctx       context.Context
		wantActor string
	}{
		{
			name:      "external user principal forwards um",
			ctx:       types.WithPrincipal(context.Background(), types.Principal{Type: types.PrincipalAPIExternalUser, ID: "10006:CHENFEI772"}),
			wantActor: "chenfei772",
		},
		{
			name:      "tenant service principal falls back to weknora",
			ctx:       types.WithPrincipal(context.Background(), types.Principal{Type: types.PrincipalAPITenant, ID: "10006"}),
			wantActor: "weknora",
		},
		{
			name:      "no principal falls back to weknora",
			ctx:       context.Background(),
			wantActor: "weknora",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var gotActor string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotActor = r.Header.Get("X-External-User-ID")
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"Result":{"ResultCount":0,"WebResults":[]}}`))
			}))
			defer server.Close()

			doubao := &DoubaoProvider{client: server.Client(), baseURL: server.URL}
			if _, err := doubao.Search(tc.ctx, "query", 1, false); err != nil {
				t.Fatal(err)
			}
			if gotActor != tc.wantActor {
				t.Fatalf("X-External-User-ID = %q, want %q", gotActor, tc.wantActor)
			}
		})
	}
}

// TestRequestActorFromContextNativeUser covers the native-session fallback:
// a logged-in user's username is attributed when no external principal exists.
func TestRequestActorFromContextNativeUser(t *testing.T) {
	ctx := context.WithValue(context.Background(), types.UserContextKey, &types.User{Username: "chenxy"})
	if got := RequestActorFromContext(ctx); got != "chenxy" {
		t.Fatalf("actor = %q, want chenxy", got)
	}
	// A tenant API-key principal suppresses the fallback: there is no end user.
	ctx = types.WithPrincipal(ctx, types.Principal{Type: types.PrincipalAPITenant, ID: "10006"})
	if got := RequestActorFromContext(ctx); got != "weknora" {
		t.Fatalf("actor = %q, want weknora for service principal", got)
	}
}
