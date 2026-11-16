package agentmesh

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
)

var (
	// ANSI 颜色与控制码正则（包含 DEC Private Mode 控制序列如 \x1b[?25l / \x1b[?25h）
	ansiRegex = regexp.MustCompile(`\x1b\[[0-9;?]*[a-zA-Z]`)

	// 终端 Spinner 与动态进度条特征正则
	noiseLineRegex = regexp.MustCompile(`(?i)^(loading|thinking|resolving|fetching|indexing|waiting|\s*\[[=\-#>\s]+\]\s*\d*%).*`)

	// 编译与测试错误特征匹配正则
	compilerErrorRegex = regexp.MustCompile(`(?i)(error:|syntax error|fatal error|undefined:|cannot find|cannot use|FAIL:|FAILED:|=== RUN|--- FAIL|undefined variable|panic:)`)
)

// StripANSI 移除文本中的 ANSI 转义序列。
func StripANSI(s string) string {
	return ansiRegex.ReplaceAllString(s, "")
}

// CleanStageOneNoise 第一阶段通用清洗：
// 剥离 ANSI 转义字符、去除 Spinner 与下载进度条、合并压缩连续空行。
func CleanStageOneNoise(raw string) string {
	stripped := StripANSI(raw)
	lines := strings.Split(stripped, "\n")
	var result []string

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			// 保留段落空行，但压缩连续空行
			if len(result) > 0 && result[len(result)-1] != "" {
				result = append(result, "")
			}
			continue
		}
		if noiseLineRegex.MatchString(trimmed) {
			continue
		}
		result = append(result, line)
	}

	return strings.Join(result, "\n")
}

// DistillOutput 两阶段噪音蒸馏引擎核心：
// 阶段一执行通用噪音清洗；阶段二按 DistillRule 提炼高价值结论或结构化数据。
func DistillOutput(stdout, stderr string, exitCode int, durationMs int64, rule string) (distilledOutput string, rawRef string) {
	rawCombined := stdout
	if stderr != "" {
		if rawCombined != "" {
			rawCombined += "\n--- STDERR ---\n" + stderr
		} else {
			rawCombined = stderr
		}
	}

	// 生成全量原始输出哈希引用 (RawStreamRef)
	h := sha256.New()
	h.Write([]byte(rawCombined))
	h.Write([]byte(fmt.Sprintf("%d_%d", exitCode, time.Now().UnixNano())))
	rawRef = "raw_stream_" + hex.EncodeToString(h.Sum(nil))[:16]

	// 第一阶段：通用噪音剔除
	cleanOut := CleanStageOneNoise(stdout)
	cleanErr := CleanStageOneNoise(stderr)

	// 第二阶段：按规则提炼
	switch rule {
	case DistillRuleCompilerErrorOnly:
		distilledOutput = distillCompilerErrors(cleanOut, cleanErr, exitCode)

	case DistillRuleJSONExtract:
		distilledOutput = distillJSON(cleanOut, cleanErr, exitCode)

	case DistillRuleCleanNoise:
		fallthrough
	default:
		distilledOutput = distillDefaultNoise(cleanOut, cleanErr, exitCode, durationMs)
	}

	return distilledOutput, rawRef
}

func distillCompilerErrors(cleanOut, cleanErr string, exitCode int) string {
	combined := cleanOut
	if cleanErr != "" {
		combined += "\n" + cleanErr
	}

	lines := strings.Split(combined, "\n")
	var errorLines []string
	for _, l := range lines {
		trimmed := strings.TrimSpace(l)
		if trimmed == "" {
			continue
		}
		if compilerErrorRegex.MatchString(trimmed) {
			errorLines = append(errorLines, l)
		}
	}

	if len(errorLines) > 0 {
		return strings.Join(errorLines, "\n")
	}

	if exitCode == 0 {
		return "编译成功，无错误输出。"
	}

	// 退出码非 0 但未精确匹配到典型错误关键字时，回退至精简的 stderr/stdout
	if strings.TrimSpace(cleanErr) != "" {
		return strings.TrimSpace(cleanErr)
	}
	if strings.TrimSpace(cleanOut) != "" {
		return strings.TrimSpace(cleanOut)
	}
	return "执行失败，未提取到明确编译错误行。"
}

func distillJSON(cleanOut, cleanErr string, exitCode int) string {
	source := cleanOut
	if strings.TrimSpace(source) == "" {
		source = cleanErr
	}

	// 搜索最外层的 JSON 块 (对象 { ... } 或数组 [ ... ])
	startObj := strings.Index(source, "{")
	endObj := strings.LastIndex(source, "}")
	if startObj != -1 && endObj != -1 && endObj > startObj {
		candidate := strings.TrimSpace(source[startObj : endObj+1])
		var js any
		if err := json.Unmarshal([]byte(candidate), &js); err == nil {
			pretty, err := json.MarshalIndent(js, "", "  ")
			if err == nil {
				return string(pretty)
			}
			return candidate
		}
	}

	startArr := strings.Index(source, "[")
	endArr := strings.LastIndex(source, "]")
	if startArr != -1 && endArr != -1 && endArr > startArr {
		candidate := strings.TrimSpace(source[startArr : endArr+1])
		var js any
		if err := json.Unmarshal([]byte(candidate), &js); err == nil {
			pretty, err := json.MarshalIndent(js, "", "  ")
			if err == nil {
				return string(pretty)
			}
			return candidate
		}
	}

	// 若未发现有效 JSON，返回清洗后的文本
	if exitCode != 0 && strings.TrimSpace(cleanErr) != "" {
		return fmt.Sprintf("未提取到 JSON (执行失败): %s", strings.TrimSpace(cleanErr))
	}
	return strings.TrimSpace(source)
}

func distillDefaultNoise(cleanOut, cleanErr string, exitCode int, durationMs int64) string {
	if exitCode != 0 {
		var errDetail string
		if strings.TrimSpace(cleanErr) != "" {
			errDetail = cleanErr
		} else if strings.TrimSpace(cleanOut) != "" {
			errDetail = cleanOut
		} else {
			errDetail = "底层探针未返回具体错误信息"
		}
		return fmt.Sprintf("- **执行状态**: 失败 (退出码: %d, 耗时: %dms)\n- **错误摘要**:\n```text\n%s\n```", exitCode, durationMs, strings.TrimSpace(errDetail))
	}

	cleaned := strings.TrimSpace(cleanOut)
	if cleaned == "" {
		cleaned = "执行成功，底层 CLI 无输出。"
	}

	return fmt.Sprintf("- **执行状态**: 成功 (耗时: %dms)\n- **精选产物**:\n%s", durationMs, cleaned)
}
