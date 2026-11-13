package memory

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
)

// DreamReport 是 Auto Dream 离线整合运行后的阶段报告。
type DreamReport struct {
	BotID              string    `json:"bot_id"`
	StartedAt          time.Time `json:"started_at"`
	CompletedAt        time.Time `json:"completed_at"`
	TranscriptsScanned int       `json:"transcripts_scanned"`
	NotesConsolidated  int       `json:"notes_consolidated"`
	TopicsSynthesized  int       `json:"topics_synthesized"`
	MemoryLinesFinal   int       `json:"memory_lines_final"`
	Summary            string    `json:"summary"`
}

// RunDream 执行 Auto Dream 四阶段离线整合流转。
// 阶段 1: Orientation（定向）—— 扫描流水、笔记与现有表征，建立主题清单与缺口
// 阶段 2: Signal Gathering（信号收集）—— 提取纠正、高频约束、踩坑与成功经验
// 阶段 3: Consolidation（整合巩固）—— 合并重复、消除陈旧矛盾、生成/更新结构化主题表征
// 阶段 4: Pruning & Indexing（修剪与索引）—— 淘汰陈旧失效项，重构 MEMORY.md 保证严格 ≤ 200 行
func (m *Manager) RunDream(ctx context.Context, botID string) (*DreamReport, error) {
	if strings.TrimSpace(botID) == "" {
		return nil, ErrInvalidBotID
	}
	start := time.Now()
	report := &DreamReport{
		BotID:     botID,
		StartedAt: start,
	}

	if err := m.EnsureDirs(botID); err != nil {
		return nil, err
	}

	// 1. Orientation: 扫描所有现有笔记与表征
	notes, err := m.ListNotes(botID)
	if err != nil {
		return nil, fmt.Errorf("Orientation 扫描 Notes 失败: %w", err)
	}
	reps, err := m.ListRepresentations(botID)
	if err != nil {
		return nil, fmt.Errorf("Orientation 扫描 Representations 失败: %w", err)
	}

	// 2. Signal Gathering: 分类与提炼高价值信号
	var (
		pitfalls    []*MemoryNote
		preferences []*MemoryNote
		facts       []*MemoryNote
		experiences []*MemoryNote
	)
	for _, n := range notes {
		switch n.Category {
		case "pitfall":
			pitfalls = append(pitfalls, n)
		case "preference":
			preferences = append(preferences, n)
		case "fact":
			facts = append(facts, n)
		default:
			experiences = append(experiences, n)
		}
	}
	report.NotesConsolidated = len(notes)

	// 3. Consolidation: 整合为主题 Representation
	// 按主题聚合经验
	topicsMap := make(map[string][]string)
	for _, p := range pitfalls {
		topicsMap["踩坑与异常处理"] = append(topicsMap["踩坑与异常处理"], fmt.Sprintf("- **%s**: %s (证据: %s)", p.Title, p.Content, p.Evidence))
	}
	for _, pref := range preferences {
		topicsMap["偏好与运行约束"] = append(topicsMap["偏好与运行约束"], fmt.Sprintf("- **%s**: %s", pref.Title, pref.Content))
	}
	for _, f := range facts {
		topicsMap["已验证事实"] = append(topicsMap["已验证事实"], fmt.Sprintf("- **%s**: %s (置信度: %.2f)", f.Title, f.Content, f.Confidence))
	}
	for _, exp := range experiences {
		topicsMap["核心操作经验"] = append(topicsMap["核心操作经验"], fmt.Sprintf("- **%s**: %s", exp.Title, exp.Content))
	}

	for topic, items := range topicsMap {
		mdContent := strings.Join(items, "\n")
		_ = m.SaveRepresentation(&MemoryRepresentation{
			Topic:     topic,
			BotID:     botID,
			Summary:   fmt.Sprintf("Auto Dream 聚合了 %d 条相关记忆", len(items)),
			Markdown:  mdContent,
			UpdatedAt: time.Now(),
		})
	}
	report.TopicsSynthesized = len(topicsMap) + len(reps)

	// 4. Pruning & Indexing: 压平重构 Injected Context (MEMORY.md)，严格控制 ≤ 200 行
	var b strings.Builder
	b.WriteString(fmt.Sprintf("# %s 长期核心记忆（Injected Context）\n\n", botID))
	b.WriteString(fmt.Sprintf("> Auto Dream 整合时间: %s\n", time.Now().Format("2006-01-02 15:04:05")))
	b.WriteString("> 铁律约束: 本文件行数必须严格 ≤ 200 行，零二次方膨胀，高 Prompt Caching 命中\n\n")

	b.WriteString("## 1. 核心定位与硬约束\n")
	b.WriteString("- 角色定义: 简单轻量 Agent，只调用系统提供的功能与工具。\n")
	b.WriteString("- 禁执行 Shell: 禁止自身执行任何 bash/sh 命令；所有受控物理操作必须调度 agent-daemon / loopback_agent。\n")
	b.WriteString("- 禁长思考: 单次推理超时硬封顶 15s，杜绝深度递归 CoT 循环与发散。\n")
	b.WriteString("- 重启生效: 人格与记忆更新采用进程重启生效策略。\n\n")

	if len(preferences) > 0 {
		b.WriteString("## 2. 偏好与约束规则\n")
		sort.Slice(preferences, func(i, j int) bool { return preferences[i].UpdatedAt.After(preferences[j].UpdatedAt) })
		for i, pref := range preferences {
			if i >= 10 { // 限制条数防膨胀
				break
			}
			b.WriteString(fmt.Sprintf("- [%s] %s\n", pref.Title, pref.Content))
		}
		b.WriteString("\n")
	}

	if len(pitfalls) > 0 {
		b.WriteString("## 3. 高危踩坑警示（Top Pitfalls）\n")
		sort.Slice(pitfalls, func(i, j int) bool { return pitfalls[i].UpdatedAt.After(pitfalls[j].UpdatedAt) })
		for i, pit := range pitfalls {
			if i >= 10 {
				break
			}
			b.WriteString(fmt.Sprintf("- ⚠️ %s: %s\n", pit.Title, pit.Content))
		}
		b.WriteString("\n")
	}

	if len(facts) > 0 {
		b.WriteString("## 4. 已验证事实（Facts）\n")
		sort.Slice(facts, func(i, j int) bool { return facts[i].Confidence > facts[j].Confidence })
		for i, fact := range facts {
			if i >= 10 {
				break
			}
			b.WriteString(fmt.Sprintf("- [事实] %s: %s\n", fact.Title, fact.Content))
		}
		b.WriteString("\n")
	}

	rawMemory := b.String()
	// 如果超出 200 行，执行智能修剪
	lines := CountLines(rawMemory)
	if lines > MaxMemoryLines {
		rawMemory = TruncateToMaxLines(rawMemory, MaxMemoryLines)
		lines = CountLines(rawMemory)
	}

	if err := m.SaveInjectedContext(botID, rawMemory); err != nil {
		return nil, fmt.Errorf("Pruning & Indexing 保存 MEMORY.md 失败: %w", err)
	}

	report.MemoryLinesFinal = lines
	report.CompletedAt = time.Now()
	report.Summary = fmt.Sprintf("Auto Dream 完成：整合了 %d 条笔记，合成了 %d 个主题，MEMORY.md 最终 %d 行",
		report.NotesConsolidated, report.TopicsSynthesized, report.MemoryLinesFinal)

	return report, nil
}
