package service

import (
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
)

func TestNormalizeVersionTitle(t *testing.T) {
	cases := []struct{ in, want string }{
		// 版本标记剥除（半/全角括号、方括号、vN、修订版）
		{"XX评级报告（第三版）", "xx评级报告"},
		{"XX评级报告(第2版).pdf", "xx评级报告"},
		{"公司制度汇编【修订版】", "公司制度汇编"},
		{"管理办法(v2)", "管理办法"},
		{"管理办法（v2.1）.docx", "管理办法"},
		{"授信审批流程[终版]", "授信审批流程"},
		{"多重标记（第一版）（修订版）", "多重标记"},
		// 年份是文档身份，不是版本标记——季报年报不得误挂
		{"XX公司2024年报", "xx公司2024年报"},
		{"XX债项跟踪（2024）", "xx债项跟踪（2024）"},
		{"2024年三季度报告.pdf", "2024年三季度报告"},
		// 无标记与空
		{"普通文档", "普通文档"},
		{"  spaced   out  ", "spaced out"},
	}
	for _, tc := range cases {
		if got := NormalizeVersionTitle(tc.in); got != tc.want {
			t.Errorf("NormalizeVersionTitle(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestVersionGroupsMatch(t *testing.T) {
	group := "xx评级报告"
	cases := []struct {
		name string
		k    *types.Knowledge
		want bool
	}{
		{"same group", &types.Knowledge{ID: "a", Title: "任意", VersionGroup: group}, true},
		{"other group", &types.Knowledge{ID: "b", Title: "xx评级报告", VersionGroup: "另一组"}, false},
		{"legacy unversioned same normalized title", &types.Knowledge{ID: "c", Title: "XX评级报告（第三版）"}, true},
		{"legacy different", &types.Knowledge{ID: "d", Title: "别的文档"}, false},
		{"nil", nil, false},
		{"empty id", &types.Knowledge{Title: "XX评级报告"}, false},
	}
	for _, tc := range cases {
		if got := versionGroupsMatch(tc.k, group); got != tc.want {
			t.Errorf("%s: got %v want %v", tc.name, got, tc.want)
		}
	}
}

func TestUnionTagIDs(t *testing.T) {
	cases := []struct {
		name      string
		own       []string
		inherited []string
		want      []string
	}{
		{"both empty", nil, nil, []string{}},
		{"own only", []string{"a"}, nil, []string{"a"}},
		{"inherit into empty", nil, []string{"a", "b"}, []string{"a", "b"}},
		{"disjoint keeps own order first", []string{"x", "y"}, []string{"a"}, []string{"x", "y", "a"}},
		{"overlap dedupes", []string{"x", "y"}, []string{"y", "z"}, []string{"x", "y", "z"}},
		{"fully covered adds nothing", []string{"a", "b"}, []string{"a", "b"}, []string{"a", "b"}},
	}
	for _, tc := range cases {
		got := unionTagIDs(tc.own, tc.inherited)
		if len(got) != len(tc.want) {
			t.Errorf("%s: got %v want %v", tc.name, got, tc.want)
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("%s: got %v want %v", tc.name, got, tc.want)
				break
			}
		}
	}
}
