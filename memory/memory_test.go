package memory

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func setupTestManager(t *testing.T) (*Manager, string) {
	t.Helper()
	tmpDir, err := os.MkdirTemp("", "nexus_memory_test_*")
	if err != nil {
		t.Fatalf("创建临时目录失败: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(tmpDir) })
	return NewManager(tmpDir), tmpDir
}

func TestEnsureDirsAndDefaultMemory(t *testing.T) {
	mgr, tmpDir := setupTestManager(t)
	botID := "ops_bot"

	if err := mgr.EnsureDirs(botID); err != nil {
		t.Fatalf("EnsureDirs 失败: %v", err)
	}

	memDir := filepath.Join(tmpDir, botID, "memory")
	for _, sub := range []string{DirTranscripts, DirNotes, DirRepresentations} {
		p := filepath.Join(memDir, sub)
		fi, err := os.Stat(p)
		if err != nil || !fi.IsDir() {
			t.Errorf("目录 %s 未正确创建", p)
		}
	}

	content, err := mgr.LoadInjectedContext(botID)
	if err != nil {
		t.Fatalf("LoadInjectedContext 失败: %v", err)
	}
	if !strings.Contains(content, "ops_bot 长期记忆") {
		t.Errorf("初始 MEMORY.md 内容不符合预期: %s", content)
	}
}

func TestTranscriptsAppendAndRead(t *testing.T) {
	mgr, _ := setupTestManager(t)
	botID := "test_bot"
	sessionID := "sess_001"

	e1 := TranscriptEntry{
		SessionID: sessionID,
		Timestamp: time.Now(),
		Role:      "user",
		Content:   "@test_bot 检查服务器状态",
	}
	e2 := TranscriptEntry{
		SessionID:  sessionID,
		Timestamp:  time.Now(),
		Role:       "bot",
		Content:    "已执行检查，状态良好",
		ToolCall:   "check_status",
		ToolResult: "OK",
	}

	if err := mgr.RecordTranscript(botID, e1); err != nil {
		t.Fatalf("记录第一条流水失败: %v", err)
	}
	if err := mgr.RecordTranscript(botID, e2); err != nil {
		t.Fatalf("记录第二条流水失败: %v", err)
	}

	entries, err := mgr.ReadTranscripts(botID, sessionID)
	if err != nil {
		t.Fatalf("读取流水失败: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("期望 2 条流水，实际 %d 条", len(entries))
	}
	if entries[0].Content != e1.Content || entries[1].ToolResult != "OK" {
		t.Errorf("流水内容不匹配: %+v", entries)
	}
}

func TestNotesCRUD(t *testing.T) {
	mgr, _ := setupTestManager(t)
	botID := "ops_bot"

	note1 := &MemoryNote{
		ID:         "note_001",
		BotID:      botID,
		Category:   "pitfall",
		Title:      "编译时缺少依赖",
		Content:    "需提前运行 apt install libssl-dev",
		Evidence:   "sess_123",
		Confidence: 0.95,
	}
	note2 := &MemoryNote{
		ID:         "note_002",
		BotID:      botID,
		Category:   "preference",
		Title:      "输出偏好",
		Content:    "使用结构化表格返回结果",
		Confidence: 0.90,
	}

	if err := mgr.SaveNote(note1); err != nil {
		t.Fatalf("保存 note1 失败: %v", err)
	}
	if err := mgr.SaveNote(note2); err != nil {
		t.Fatalf("保存 note2 失败: %v", err)
	}

	got, err := mgr.GetNote(botID, "note_001")
	if err != nil {
		t.Fatalf("获取 note_001 失败: %v", err)
	}
	if got.Title != note1.Title || got.Category != "pitfall" {
		t.Errorf("获取的笔记内容不匹配: %+v", got)
	}

	list, err := mgr.ListNotes(botID)
	if err != nil {
		t.Fatalf("ListNotes 失败: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("期望 2 条笔记，实际 %d 条", len(list))
	}
}

func TestMemoryMD200LinesHardConstraint(t *testing.T) {
	mgr, _ := setupTestManager(t)
	botID := "ops_bot"

	// 构造 250 行的内容
	var lines []string
	for i := 1; i <= 250; i++ {
		lines = append(lines, fmt.Sprintf("Line %d: 记忆条目测试内容", i))
	}
	hugeContent := strings.Join(lines, "\n")

	// SaveInjectedContext 必须严格拒绝超过 200 行
	err := mgr.SaveInjectedContext(botID, hugeContent)
	if err == nil {
		t.Fatal("期望 SaveInjectedContext 拒绝超过 200 行，但返回 nil")
	}
	if !strings.Contains(err.Error(), "超过 200 行硬约束限制") {
		t.Errorf("错误信息不符合预期: %v", err)
	}

	// 测试 TruncateToMaxLines
	truncated := TruncateToMaxLines(hugeContent, MaxMemoryLines)
	tCount := CountLines(truncated)
	if tCount > MaxMemoryLines {
		t.Fatalf("截断后行数 %d 超过限制 %d", tCount, MaxMemoryLines)
	}
	if !strings.Contains(truncated, "[WARNING]") {
		t.Errorf("截断后缺少警告提示标记")
	}

	// 写入合规的 150 行
	var validLines []string
	for i := 1; i <= 150; i++ {
		validLines = append(validLines, fmt.Sprintf("Line %d: 合规记忆条目", i))
	}
	validContent := strings.Join(validLines, "\n")
	if err := mgr.SaveInjectedContext(botID, validContent); err != nil {
		t.Fatalf("保存合规内容失败: %v", err)
	}

	loaded, err := mgr.LoadInjectedContext(botID)
	if err != nil {
		t.Fatalf("加载合规内容失败: %v", err)
	}
	if CountLines(loaded) != 150 {
		t.Errorf("期望 150 行，实际 %d 行", CountLines(loaded))
	}
}

func TestAutoDreamPipeline(t *testing.T) {
	mgr, _ := setupTestManager(t)
	botID := "ops_bot"

	// 预置几条笔记
	_ = mgr.SaveNote(&MemoryNote{
		ID:         "p1",
		BotID:      botID,
		Category:   "pitfall",
		Title:      "Docker 容器权限不足",
		Content:    "挂载宿主机 socket 时需要给予用户 docker 组权限",
		Evidence:   "session_01",
		Confidence: 0.98,
	})
	_ = mgr.SaveNote(&MemoryNote{
		ID:         "pref1",
		BotID:      botID,
		Category:   "preference",
		Title:      "回复风格",
		Content:    "先给一句话结论，再呈现详细执行卡片",
		Confidence: 0.95,
	})
	_ = mgr.SaveNote(&MemoryNote{
		ID:         "f1",
		BotID:      botID,
		Category:   "fact",
		Title:      "生产环境架构",
		Content:    "内核运行在 7100/7101/7102 端口",
		Confidence: 1.0,
	})

	ctx := context.Background()
	report, err := mgr.RunDream(ctx, botID)
	if err != nil {
		t.Fatalf("RunDream 失败: %v", err)
	}

	if report.NotesConsolidated != 3 {
		t.Errorf("期望整合 3 条笔记，实际 %d", report.NotesConsolidated)
	}
	if report.MemoryLinesFinal > MaxMemoryLines || report.MemoryLinesFinal == 0 {
		t.Errorf("最终 MEMORY.md 行数异常: %d (上限 %d)", report.MemoryLinesFinal, MaxMemoryLines)
	}

	// 检验生成的 MEMORY.md 内容
	memContent, err := mgr.LoadInjectedContext(botID)
	if err != nil {
		t.Fatalf("加载整合后的 MEMORY.md 失败: %v", err)
	}
	if !strings.Contains(memContent, "Docker 容器权限不足") {
		t.Errorf("未包含踩坑条目: %s", memContent)
	}
	if !strings.Contains(memContent, "先给一句话结论") {
		t.Errorf("未包含偏好条目: %s", memContent)
	}
	if !strings.Contains(memContent, "7100/7101/7102") {
		t.Errorf("未包含事实条目: %s", memContent)
	}

	// 检验 Representations 生成
	reps, err := mgr.ListRepresentations(botID)
	if err != nil {
		t.Fatalf("ListRepresentations 失败: %v", err)
	}
	if len(reps) == 0 {
		t.Errorf("未生成任何主题表征 Wiki")
	}
}
