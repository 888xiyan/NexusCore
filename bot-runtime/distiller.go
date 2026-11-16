package main

import (
	"fmt"
	"regexp"
	"strings"
)

var (
	// 匹配常见的底层 CLI 动态状态与进度条行
	noiseLineRegex = regexp.MustCompile(`(?i)^(loading|thinking|resolving|fetching|indexing|waiting|\s*\[[=\-#\s]+\]\s*\d*%).*`)
)

// DistillCLIOutput 针对底层 CLI 原始终端输出进行抗噪音蒸馏
func DistillCLIOutput(stdout, stderr string, exitCode int, elapsedMS int64) string {
	if exitCode != 0 {
		var errDetail string
		if strings.TrimSpace(stderr) != "" {
			errDetail = cleanNoise(stderr)
		} else if strings.TrimSpace(stdout) != "" {
			errDetail = cleanNoise(stdout)
		} else {
			errDetail = "底层 CLI 未返回具体错误信息"
		}
		return fmt.Sprintf("- **执行状态**: 失败 (退出码: %d, 耗时: %dms)\n- **错误摘要**:\n```text\n%s\n```", exitCode, elapsedMS, strings.TrimSpace(errDetail))
	}

	cleaned := cleanNoise(stdout)
	if strings.TrimSpace(cleaned) == "" {
		cleaned = "执行成功，底层 CLI 无输出。"
	}

	return fmt.Sprintf("- **执行状态**: 成功 (耗时: %dms)\n- **精选产物**:\n%s", elapsedMS, strings.TrimSpace(cleaned))
}

func cleanNoise(raw string) string {
	lines := strings.Split(raw, "\n")
	var result []string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			// 保留段落空行，但连续空行压缩
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
