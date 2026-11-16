package plugin

import (
	"testing"
)

func TestParseVersion(t *testing.T) {
	tests := []struct {
		input      string
		wantErr    bool
		major      int
		minor      int
		patch      int
		prerelease string
		build      string
	}{
		{"1.0.0", false, 1, 0, 0, "", ""},
		{"v1.2.3", false, 1, 2, 3, "", ""},
		{"V2.0.1", false, 2, 0, 1, "", ""},
		{"1.2.3-alpha.1", false, 1, 2, 3, "alpha.1", ""},
		{"1.2.3+20130313144700", false, 1, 2, 3, "", "20130313144700"},
		{"1.2.3-beta.2+exp.sha.5114f85", false, 1, 2, 3, "beta.2", "exp.sha.5114f85"},
		{"2", false, 2, 0, 0, "", ""},
		{"2.1", false, 2, 1, 0, "", ""},
		{"", true, 0, 0, 0, "", ""},
		{"abc", true, 0, 0, 0, "", ""},
		{"1.2.3.4", true, 0, 0, 0, "", ""},
		{"-1.0.0", true, 0, 0, 0, "", ""},
		{"1.-2.0", true, 0, 0, 0, "", ""},
		{"1.2.-3", true, 0, 0, 0, "", ""},
	}

	for _, tt := range tests {
		v, err := ParseVersion(tt.input)
		if (err != nil) != tt.wantErr {
			t.Errorf("ParseVersion(%q) err = %v, wantErr %v", tt.input, err, tt.wantErr)
			continue
		}
		if !tt.wantErr {
			if v.Major != tt.major || v.Minor != tt.minor || v.Patch != tt.patch ||
				v.Prerelease != tt.prerelease || v.Build != tt.build {
				t.Errorf("ParseVersion(%q) = %+v, want major=%d, minor=%d, patch=%d, pre=%s, build=%s",
					tt.input, v, tt.major, tt.minor, tt.patch, tt.prerelease, tt.build)
			}
		}
	}
}

func TestVersionString(t *testing.T) {
	v1, _ := ParseVersion("1.2.3")
	if v1.String() != "1.2.3" {
		t.Errorf("v1.String() = %q, want %q", v1.String(), "1.2.3")
	}

	v2, _ := ParseVersion("1.2.3-alpha.1+build.10")
	if v2.String() != "1.2.3-alpha.1+build.10" {
		t.Errorf("v2.String() = %q, want %q", v2.String(), "1.2.3-alpha.1+build.10")
	}
}

func TestCompareVersion(t *testing.T) {
	tests := []struct {
		v1   string
		v2   string
		want int
	}{
		{"1.0.0", "1.0.0", 0},
		{"1.0.1", "1.0.0", 1},
		{"1.0.0", "1.0.1", -1},
		{"1.1.0", "1.0.9", 1},
		{"2.0.0", "1.99.99", 1},
		{"1.0.0-alpha", "1.0.0", -1},
		{"1.0.0", "1.0.0-alpha", 1},
		{"1.0.0-alpha", "1.0.0-alpha.1", -1},
		{"1.0.0-alpha.1", "1.0.0-alpha.beta", -1}, // 数字 < 非数字
		{"1.0.0-alpha.beta", "1.0.0-beta", -1},
		{"1.0.0-beta", "1.0.0-beta.2", -1},
		{"1.0.0-beta.11", "1.0.0-beta.2", 1}, // 11 > 2 (数字比较)
		{"1.0.0-rc.1", "1.0.0-beta.11", 1},
		{"1.0.0-alpha", "1.0.0-alpha", 0},
		{"1.0.0+build1", "1.0.0+build2", 0}, // build metadata 忽略
	}

	for _, tt := range tests {
		v1, err1 := ParseVersion(tt.v1)
		v2, err2 := ParseVersion(tt.v2)
		if err1 != nil || err2 != nil {
			t.Fatalf("ParseVersion 失败: %v, %v", err1, err2)
		}
		got := v1.Compare(v2)
		if got != tt.want {
			t.Errorf("Compare(%s, %s) = %d, want %d", tt.v1, tt.v2, got, tt.want)
		}

		// 测试 CompareVersions 辅助函数
		cmpGot, err := CompareVersions(tt.v1, tt.v2)
		if err != nil || cmpGot != tt.want {
			t.Errorf("CompareVersions(%s, %s) = %d (err: %v), want %d", tt.v1, tt.v2, cmpGot, err, tt.want)
		}
	}

	// 错误测试
	if _, err := CompareVersions("bad", "1.0.0"); err == nil {
		t.Errorf("CompareVersions 非法输入未报错")
	}
	if _, err := CompareVersions("1.0.0", "bad"); err == nil {
		t.Errorf("CompareVersions 非法输入未报错")
	}
}

func TestMatchSemVer(t *testing.T) {
	tests := []struct {
		ver      string
		rangeStr string
		want     bool
	}{
		// 精确匹配
		{"1.0.0", "1.0.0", true},
		{"1.0.0", "=1.0.0", true},
		{"1.0.1", "1.0.0", false},
		{"1.0.0", "!=1.0.0", false},
		{"1.0.1", "!=1.0.0", true},

		// 通配符
		{"1.2.3", "*", true},
		{"1.2.3", "", true},
		{"1.2.3", "x", true},
		{"1.2.3", "1.x", true},
		{"1.2.3", "1.2.x", true},
		{"2.0.0", "1.x", false},

		// Caret ^
		{"1.0.0", "^1.0.0", true},
		{"1.2.3", "^1.0.0", true},
		{"1.9.9", "^1.0.0", true},
		{"2.0.0", "^1.0.0", false},
		{"0.9.9", "^1.0.0", false},
		{"0.2.3", "^0.2.0", true},
		{"0.2.9", "^0.2.0", true},
		{"0.3.0", "^0.2.0", false},
		{"0.0.3", "^0.0.3", true},
		{"0.0.4", "^0.0.3", false},
		{"1.5.0", "^1", true},
		{"2.0.0", "^1", false},

		// Tilde ~
		{"1.2.3", "~1.2.3", true},
		{"1.2.9", "~1.2.3", true},
		{"1.3.0", "~1.2.3", false},
		{"1.1.9", "~1.2.3", false},
		{"1.2.5", "~1.2", true},
		{"1.3.0", "~1.2", false},
		{"1.9.0", "~1", true},
		{"2.0.0", "~1", false},

		// 比较符
		{"1.5.0", ">=1.0.0", true},
		{"0.9.0", ">=1.0.0", false},
		{"1.5.0", ">1.0.0", true},
		{"1.0.0", ">1.0.0", false},
		{"1.0.0", "<=1.0.0", true},
		{"1.0.1", "<=1.0.0", false},
		{"0.9.0", "<1.0.0", true},
		{"1.0.0", "<1.0.0", false},

		// 复合 AND 范围
		{"1.5.0", ">=1.0.0 <2.0.0", true},
		{"2.0.0", ">=1.0.0 <2.0.0", false},
		{"0.9.0", ">=1.0.0 <2.0.0", false},
		{"1.2.0", ">=1.0.0, <=1.5.0", true},
		{"1.6.0", ">=1.0.0, <=1.5.0", false},

		// OR 范围 (||)
		{"1.5.0", "^1.0.0 || ^2.0.0", true},
		{"2.5.0", "^1.0.0 || ^2.0.0", true},
		{"3.0.0", "^1.0.0 || ^2.0.0", false},
	}

	for _, tt := range tests {
		got, err := MatchSemVer(tt.ver, tt.rangeStr)
		if err != nil {
			t.Errorf("MatchSemVer(%q, %q) 发生错误: %v", tt.ver, tt.rangeStr, err)
			continue
		}
		if got != tt.want {
			t.Errorf("MatchSemVer(%q, %q) = %v, want %v", tt.ver, tt.rangeStr, got, tt.want)
		}
	}

	// 错误路径
	if _, err := MatchSemVer("invalid_version", "^1.0.0"); err == nil {
		t.Errorf("非法版本未报错")
	}
	if _, err := MatchSemVer("1.0.0", ">=bad_version"); err == nil {
		t.Errorf("非法范围未报错")
	}
	if _, err := MatchSemVer("1.0.0", ">"); err == nil {
		t.Errorf("操作符缺少版本未报错")
	}
}

func TestIsValidIdentifier(t *testing.T) {
	if !IsValidIdentifier("nexus.groupchat.v1") {
		t.Errorf("合法标识符判断失败")
	}
	if !IsValidIdentifier("plugin_pm") {
		t.Errorf("合法标识符判断失败")
	}
	if IsValidIdentifier("") {
		t.Errorf("空标识符应为非法")
	}
	if IsValidIdentifier("bad name with spaces") {
		t.Errorf("包含空格的标识符应为非法")
	}
}

func TestSemVerEdgeCases(t *testing.T) {
	// 1. Prerelease 比较分支覆盖
	v1, _ := ParseVersion("1.0.0-alpha.1")
	v2, _ := ParseVersion("1.0.0-alpha.2")
	if v1.Compare(v2) >= 0 {
		t.Errorf("1.0.0-alpha.1 应小于 1.0.0-alpha.2")
	}
	if v2.Compare(v1) <= 0 {
		t.Errorf("1.0.0-alpha.2 应大于 1.0.0-alpha.1")
	}

	vA, _ := ParseVersion("1.0.0-alpha")
	vB, _ := ParseVersion("1.0.0-beta")
	if vB.Compare(vA) <= 0 {
		t.Errorf("1.0.0-beta 应大于 1.0.0-alpha")
	}

	vStrNum, _ := ParseVersion("1.0.0-alpha.beta")
	vNumStr, _ := ParseVersion("1.0.0-alpha.1")
	if vStrNum.Compare(vNumStr) <= 0 {
		t.Errorf("alpha.beta 应大于 alpha.1")
	}

	vShort, _ := ParseVersion("1.0.0-alpha")
	vLong, _ := ParseVersion("1.0.0-alpha.1")
	if vShort.Compare(vLong) >= 0 {
		t.Errorf("alpha 应小于 alpha.1")
	}
	if vLong.Compare(vShort) <= 0 {
		t.Errorf("alpha.1 应大于 alpha")
	}

	// 2. ParseRange 边界
	r1, err := ParseRange("1.0.0 || || 2.0.0")
	if err != nil || len(r1.Sets) != 2 {
		t.Errorf("含空分批 ParseRange 失败: %v, sets=%d", err, len(r1.Sets))
	}

	rCommaTokens, err := ParseRange("1.0.0 || ,,, || 2.0.0")
	if err != nil || len(rCommaTokens.Sets) != 2 {
		t.Errorf("含纯逗号分批 ParseRange 失败: %v, sets=%d", err, len(rCommaTokens.Sets))
	}

	rAllComma, err := ParseRange(",,, || ,,,")
	if err != nil || len(rAllComma.Sets) != 0 {
		t.Errorf("全逗号 ParseRange 失败: %v", err)
	}

	rWildcardInCondition, err := ParseRange(">=1.0.0 *")
	if err != nil || len(rWildcardInCondition.Sets) != 1 {
		t.Errorf("复合含通配符 ParseRange 失败: %v", err)
	}

	rEmpty, err := ParseRange("   ")
	if err != nil || len(rEmpty.Sets) != 0 {
		t.Errorf("空白范围 ParseRange 失败: %v", err)
	}

	// 3. parseSingleCondition 错误分支
	if _, err := ParseRange("^bad.version"); err == nil {
		t.Errorf("非法 caret 范围未报错")
	}
	if _, err := ParseRange("~bad.version"); err == nil {
		t.Errorf("非法 tilde 范围未报错")
	}
	if _, err := ParseRange("bad.x"); err == nil {
		t.Errorf("非法 x 范围未报错")
	}

	// 4. Comparator 缺省操作符
	comp := Comparator{Op: "", Version: Version{Major: 1, Minor: 0, Patch: 0}}
	if !comp.Matches(Version{Major: 1, Minor: 0, Patch: 0}) {
		t.Errorf("缺省操作符匹配自身失败")
	}
	if comp.Matches(Version{Major: 1, Minor: 1, Patch: 0}) {
		t.Errorf("缺省操作符错误匹配不同版本")
	}
}
