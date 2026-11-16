package plugin

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode"
)

var (
	ErrInvalidSemVer = errors.New("无效的语义化版本")
	ErrInvalidRange  = errors.New("无效的版本范围表达式")
)

// Version 表示 SemVer 2.0.0 语义化版本
type Version struct {
	Major      int
	Minor      int
	Patch      int
	Prerelease string
	Build      string
	Raw        string
}

// ParseVersion 解析 SemVer 版本字符串 (如 "v1.2.3", "1.2.3-alpha.1", "1.2.3+20130313144700")
func ParseVersion(vStr string) (Version, error) {
	raw := strings.TrimSpace(vStr)
	if raw == "" {
		return Version{}, fmt.Errorf("%w: 版本字符串不能为空", ErrInvalidSemVer)
	}

	s := raw
	if strings.HasPrefix(s, "v") || strings.HasPrefix(s, "V") {
		s = s[1:]
	}

	var build string
	if idx := strings.Index(s, "+"); idx >= 0 {
		build = s[idx+1:]
		s = s[:idx]
	}

	var prerelease string
	if idx := strings.Index(s, "-"); idx >= 0 {
		prerelease = s[idx+1:]
		s = s[:idx]
	}

	parts := strings.Split(s, ".")
	if len(parts) < 1 || len(parts) > 3 {
		return Version{}, fmt.Errorf("%w: %q 格式不符合 X.Y.Z", ErrInvalidSemVer, raw)
	}

	major, err := strconv.Atoi(parts[0])
	if err != nil || major < 0 {
		return Version{}, fmt.Errorf("%w: major 版本 %q 非法", ErrInvalidSemVer, parts[0])
	}

	minor := 0
	if len(parts) >= 2 {
		minor, err = strconv.Atoi(parts[1])
		if err != nil || minor < 0 {
			return Version{}, fmt.Errorf("%w: minor 版本 %q 非法", ErrInvalidSemVer, parts[1])
		}
	}

	patch := 0
	if len(parts) == 3 {
		patch, err = strconv.Atoi(parts[2])
		if err != nil || patch < 0 {
			return Version{}, fmt.Errorf("%w: patch 版本 %q 非法", ErrInvalidSemVer, parts[2])
		}
	}

	return Version{
		Major:      major,
		Minor:      minor,
		Patch:      patch,
		Prerelease: prerelease,
		Build:      build,
		Raw:        raw,
	}, nil
}

// String 返回标准字符串表示
func (v Version) String() string {
	res := fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch)
	if v.Prerelease != "" {
		res += "-" + v.Prerelease
	}
	if v.Build != "" {
		res += "+" + v.Build
	}
	return res
}

// Compare 比较两个版本 (SemVer 2.0.0 规范，Build 元数据不参与比较)
// 返回: -1 (v < other), 0 (v == other), 1 (v > other)
func (v Version) Compare(other Version) int {
	if v.Major != other.Major {
		if v.Major > other.Major {
			return 1
		}
		return -1
	}
	if v.Minor != other.Minor {
		if v.Minor > other.Minor {
			return 1
		}
		return -1
	}
	if v.Patch != other.Patch {
		if v.Patch > other.Patch {
			return 1
		}
		return -1
	}

	// 预发布版本优先级低于正式版本
	// 1.0.0-alpha < 1.0.0
	if v.Prerelease == "" && other.Prerelease == "" {
		return 0
	}
	if v.Prerelease == "" && other.Prerelease != "" {
		return 1
	}
	if v.Prerelease != "" && other.Prerelease == "" {
		return -1
	}

	// 两者均有 Prerelease: 按 . 分割逐个标识符比较
	partsA := strings.Split(v.Prerelease, ".")
	partsB := strings.Split(other.Prerelease, ".")
	minLen := len(partsA)
	if len(partsB) < minLen {
		minLen = len(partsB)
	}

	for i := 0; i < minLen; i++ {
		a := partsA[i]
		b := partsB[i]

		numA, errA := strconv.Atoi(a)
		numB, errB := strconv.Atoi(b)

		if errA == nil && errB == nil {
			// 都是纯数字: 数值比较
			if numA != numB {
				if numA > numB {
					return 1
				}
				return -1
			}
		} else if errA != nil && errB != nil {
			// 都是字符串: 字典序比较
			if a != b {
				if a > b {
					return 1
				}
				return -1
			}
		} else {
			// 一个是数字，一个是字符串: 数字标识符优先级低于非数字标识符
			if errA == nil {
				return -1
			}
			return 1
		}
	}

	if len(partsA) > len(partsB) {
		return 1
	} else if len(partsA) < len(partsB) {
		return -1
	}

	return 0
}

// CompareVersions 比较两个版本字符串
func CompareVersions(v1Str, v2Str string) (int, error) {
	v1, err := ParseVersion(v1Str)
	if err != nil {
		return 0, err
	}
	v2, err := ParseVersion(v2Str)
	if err != nil {
		return 0, err
	}
	return v1.Compare(v2), nil
}

// Comparator 单个比较条件 (如 ">=1.0.0")
type Comparator struct {
	Op      string // ">", ">=", "<", "<=", "=", "!=", ""
	Version Version
}

func (c Comparator) Matches(v Version) bool {
	cmp := v.Compare(c.Version)
	switch c.Op {
	case ">":
		return cmp > 0
	case ">=":
		return cmp >= 0
	case "<":
		return cmp < 0
	case "<=":
		return cmp <= 0
	case "=", "==":
		return cmp == 0
	case "!=":
		return cmp != 0
	default:
		return cmp == 0
	}
}

// RangeSet 一组 AND 约束条件
type RangeSet []Comparator

func (rs RangeSet) Matches(v Version) bool {
	for _, comp := range rs {
		if !comp.Matches(v) {
			return false
		}
	}
	return true
}

// Range 完整的版本范围 (支持 || 逻辑)
type Range struct {
	Raw  string
	Sets []RangeSet
}

// Matches 判定版本是否匹配该范围
func (r *Range) Matches(v Version) bool {
	if len(r.Sets) == 0 {
		return true // 空范围或 "*" 匹配所有版本
	}
	for _, rs := range r.Sets {
		if rs.Matches(v) {
			return true
		}
	}
	return false
}

// ParseRange 解析版本范围表达式
// 支持语法:
// - "*" 或 "" (任意版本)
// - "=1.2.3" 或 "1.2.3" (精确匹配)
// - "^1.2.3" (Caret: 保持主版本兼容)
// - "~1.2.3" (Tilde: 保持次版本兼容)
// - ">=1.0.0 <2.0.0" (空格或逗号连接的 AND 组合)
// - "^1.0.0 || ^2.0.0" (|| 连接的 OR 组合)
func ParseRange(rangeStr string) (*Range, error) {
	raw := strings.TrimSpace(rangeStr)
	if raw == "" || raw == "*" || strings.ToLower(raw) == "x" {
		return &Range{Raw: raw, Sets: nil}, nil
	}

	orBranches := strings.Split(raw, "||")
	var sets []RangeSet

	for _, branch := range orBranches {
		branch = strings.TrimSpace(branch)
		if branch == "" {
			continue
		}

		tokens := tokenizeRange(branch)
		if len(tokens) == 0 {
			continue
		}

		var rangeSet RangeSet
		for _, token := range tokens {
			comps, err := parseSingleCondition(token)
			if err != nil {
				return nil, fmt.Errorf("%w: %v", ErrInvalidRange, err)
			}
			rangeSet = append(rangeSet, comps...)
		}

		if len(rangeSet) > 0 {
			sets = append(sets, rangeSet)
		}
	}

	if len(sets) == 0 {
		return &Range{Raw: raw, Sets: nil}, nil
	}

	return &Range{Raw: raw, Sets: sets}, nil
}

// MatchSemVer 检查 versionStr 是否符合 rangeStr
func MatchSemVer(versionStr string, rangeStr string) (bool, error) {
	v, err := ParseVersion(versionStr)
	if err != nil {
		return false, err
	}
	r, err := ParseRange(rangeStr)
	if err != nil {
		return false, err
	}
	return r.Matches(v), nil
}

func tokenizeRange(s string) []string {
	// 将逗号替换为空格
	s = strings.ReplaceAll(s, ",", " ")
	fields := strings.Fields(s)
	return fields
}

func parseSingleCondition(token string) ([]Comparator, error) {
	token = strings.TrimSpace(token)
	if token == "" || token == "*" || strings.ToLower(token) == "x" {
		return nil, nil
	}

	// Caret: ^1.2.3
	if strings.HasPrefix(token, "^") {
		verStr := strings.TrimSpace(token[1:])
		v, err := ParseVersion(verStr)
		if err != nil {
			return nil, err
		}
		// ^1.2.3 -> >=1.2.3 <2.0.0 (if major > 0)
		// ^0.2.3 -> >=0.2.3 <0.3.0 (if major == 0 && minor > 0)
		// ^0.0.3 -> >=0.0.3 <0.0.4 (if major == 0 && minor == 0)
		var maxVer Version
		if v.Major > 0 {
			maxVer = Version{Major: v.Major + 1, Minor: 0, Patch: 0}
		} else if v.Minor > 0 {
			maxVer = Version{Major: 0, Minor: v.Minor + 1, Patch: 0}
		} else {
			maxVer = Version{Major: 0, Minor: 0, Patch: v.Patch + 1}
		}
		return []Comparator{
			{Op: ">=", Version: v},
			{Op: "<", Version: maxVer},
		}, nil
	}

	// Tilde: ~1.2.3
	if strings.HasPrefix(token, "~") {
		verStr := strings.TrimSpace(token[1:])
		v, err := ParseVersion(verStr)
		if err != nil {
			return nil, err
		}
		// ~1.2.3 -> >=1.2.3 <1.3.0
		// ~1.2   -> >=1.2.0 <1.3.0
		// ~1     -> >=1.0.0 <2.0.0
		var maxVer Version
		parts := strings.Split(strings.TrimPrefix(strings.TrimPrefix(verStr, "v"), "V"), ".")
		if len(parts) == 1 {
			maxVer = Version{Major: v.Major + 1, Minor: 0, Patch: 0}
		} else {
			maxVer = Version{Major: v.Major, Minor: v.Minor + 1, Patch: 0}
		}
		return []Comparator{
			{Op: ">=", Version: v},
			{Op: "<", Version: maxVer},
		}, nil
	}

	// 比较操作符: >=, <=, >, <, =, !=, ==
	ops := []string{">=", "<=", "!=", "==", ">", "<", "="}
	var matchedOp string
	for _, op := range ops {
		if strings.HasPrefix(token, op) {
			matchedOp = op
			break
		}
	}

	verStr := token
	if matchedOp != "" {
		verStr = strings.TrimSpace(token[len(matchedOp):])
	} else {
		matchedOp = "="
	}

	// 如果 verStr 开头还有非数字非 v 的字符，格式不合法
	if verStr == "" {
		return nil, fmt.Errorf("比较操作符 %s 后面缺少版本号", matchedOp)
	}

	// 针对可能包含通配符的边界 (如 "1.x", "1.2.x")
	if strings.HasSuffix(verStr, ".x") || strings.HasSuffix(verStr, ".*") {
		base := strings.TrimSuffix(strings.TrimSuffix(verStr, ".x"), ".*")
		v, err := ParseVersion(base)
		if err != nil {
			return nil, err
		}
		parts := strings.Split(base, ".")
		var maxVer Version
		if len(parts) == 1 {
			maxVer = Version{Major: v.Major + 1, Minor: 0, Patch: 0}
		} else {
			maxVer = Version{Major: v.Major, Minor: v.Minor + 1, Patch: 0}
		}
		return []Comparator{
			{Op: ">=", Version: v},
			{Op: "<", Version: maxVer},
		}, nil
	}

	v, err := ParseVersion(verStr)
	if err != nil {
		return nil, err
	}

	return []Comparator{
		{Op: matchedOp, Version: v},
	}, nil
}

// IsValidIdentifier 校验标识符字符 (字母、数字、下划线、中划线、点)
func IsValidIdentifier(id string) bool {
	if id == "" {
		return false
	}
	for _, r := range id {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_' && r != '-' && r != '.' {
			return false
		}
	}
	return true
}
