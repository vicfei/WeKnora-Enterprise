package web_search

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
)

// v3.1 应急适配验证：DMZ 形态（自定义 endpoint + 免鉴权）与公网形态（默认地址 + Bearer）
func TestDoubaoV31DMZAdaptation(t *testing.T) {
	var gotAuth, gotPath string
	var gotBody map[string]interface{}

	// Mock DMZ 网关
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"Result":{"ResultCount":1,"WebResults":[{"Title":"mock result","Url":"https://example.com","Snippet":"mock snippet","Content":"mock content body","PublishTime":""}]}}`)
	}))
	defer srv.Close()

	t.Run("DMZ form: endpoint override + keyless", func(t *testing.T) {
		params := types.WebSearchProviderParameters{
			APIKey:      "", // DMZ 免鉴权
			ExtraConfig: map[string]string{"endpoint": srv.URL + "/apihub/proxy/doubao/search"},
		}
		// v3.1 校验应放行（有 endpoint 覆盖时空 key 允许）
		if err := ValidateDoubaoParameters(params); err != nil {
			t.Fatalf("keyless DMZ should pass validation: %v", err)
		}
		provider, err := NewDoubaoProvider(params)
		if err != nil {
			t.Fatal(err)
		}
		dp := provider.(*DoubaoProvider)
		if dp.baseURL != srv.URL+"/apihub/proxy/doubao/search" {
			t.Fatalf("endpoint override failed: got %s", dp.baseURL)
		}
		// 测试专用：mock 在 127.0.0.1 上，SSRF 安全客户端会拦截；直接替换为裸客户端
		dp.client = srv.Client()
		results, err := dp.Search(context.Background(), "白内障手术", 5, false)
		if err != nil {
			t.Fatalf("DMZ search failed: %v", err)
		}
		if len(results) == 0 {
			t.Fatal("expected results")
		}
		// DMZ 形态不应发 Authorization 头
		if gotAuth != "" {
			t.Fatalf("DMZ form should NOT send Authorization, got %q", gotAuth)
		}
		// 载荷形状验证
		if gotBody["Query"] != "白内障手术" {
			t.Fatalf("expected Query field, got body: %v", gotBody)
		}
		if gotBody["SearchType"] != "web" {
			t.Fatalf("expected SearchType=web, got %v", gotBody["SearchType"])
		}
		if _, ok := gotBody["Count"]; !ok {
			t.Fatal("expected Count field in payload")
		}
		if _, ok := gotBody["ContentFormats"]; !ok {
			t.Fatal("expected ContentFormats field in payload")
		}
		if _, ok := gotBody["QueryControl"]; !ok {
			t.Fatal("expected QueryControl field in payload")
		}
		t.Logf("DMZ: auth=%q path=%s payload_keys=%v", gotAuth, gotPath, func() []string {
			keys := []string{}
			for k := range gotBody {
				keys = append(keys, k)
			}
			return keys
		}())
	})

	t.Run("Public form: default endpoint + Bearer", func(t *testing.T) {
		params := types.WebSearchProviderParameters{
			APIKey: "test-key-123",
		}
		if err := ValidateDoubaoParameters(params); err != nil {
			t.Fatal(err)
		}
		provider, err := NewDoubaoProvider(params)
		if err != nil {
			t.Fatal(err)
		}
		dp := provider.(*DoubaoProvider)
		if dp.baseURL != "https://open.feedcoopapi.com/search_api/web_search" {
			t.Fatalf("default endpoint should be public URL, got %s", dp.baseURL)
		}
		if dp.apiKey == "" {
			t.Fatal("API key should be set")
		}
		// 不实际调用公网（mock 只验证 DMZ 路径），只验证配置正确
	})

	t.Run("Validation: no key and no endpoint should fail", func(t *testing.T) {
		params := types.WebSearchProviderParameters{}
		if err := ValidateDoubaoParameters(params); err == nil {
			t.Fatal("should reject when both key and endpoint are empty")
		}
	})
}
