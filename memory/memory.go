// Package memory 实现 Bot 原生单体记忆系统（基于 Claude Code 四层文件系统记忆栈与 Auto Dream 离线整合）。
// 四层栈包括：
//  1. Transcripts（原始会话层）：data/bots/<bot_id>/memory/transcripts/<session>.jsonl，Append-Only 事实底座；
//  2. Memory Notes（离散笔记层）：data/bots/<bot_id>/memory/notes/<note_id>.json，任务中捕获的显著性经验与踩坑；
//  3. Memory Representations（结构化表征层）：data/bots/<bot_id>/memory/representations/<topic>.md，离线合成的主题 Wiki；
//  4. Injected Context（常驻注入层）：data/bots/<bot_id>/memory/MEMORY.md，启动时预加载，铁律严格约束 ≤ 200 行。
package memory

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	// MaxMemoryLines 是 Injected Context (MEMORY.md) 的铁律硬约束上限（约 500~2000 Tokens）。
	MaxMemoryLines = 200

	// 目录结构常量
	DirTranscripts     = "transcripts"
	DirNotes           = "notes"
	DirRepresentations = "representations"
	FileMemoryMD       = "MEMORY.md"
)

var (
	ErrMemoryExceedsLimit = errors.New("单体记忆 MEMORY.md 超过 200 行硬约束限制")
	ErrInvalidBotID       = errors.New("bot_id 不能为空")
	ErrNotFound           = errors.New("记忆条目未找到")
)

// TranscriptEntry 是 Transcripts 层的会话记录条目。
type TranscriptEntry struct {
	SessionID      string    `json:"session_id"`
	Timestamp      time.Time `json:"timestamp"`
	Role           string    `json:"role"` // "user" | "bot" | "tool" | "system"
	Content        string    `json:"content"`
	ToolCall       string    `json:"tool_call,omitempty"`
	ToolResult     string    `json:"tool_result,omitempty"`
	CorrelationID  string    `json:"correlation_id,omitempty"`
	RootEnvelopeID string    `json:"root_envelope_id,omitempty"`
}

// MemoryNote 是 Memory Notes 层的单条离散经验或踩坑记录。
type MemoryNote struct {
	ID         string    `json:"id"`
	BotID      string    `json:"bot_id"`
	Category   string    `json:"category"` // "experience" | "pitfall" | "preference" | "fact"
	Title      string    `json:"title"`
	Content    string    `json:"content"`
	Evidence   string    `json:"evidence,omitempty"`   // 事实来源，如 session_id、命令、commit
	Confidence float64   `json:"confidence,omitempty"` // 置信度 0.0 ~ 1.0
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// MemoryRepresentation 是 Memory Representations 层的结构化主题 Wiki。
type MemoryRepresentation struct {
	Topic     string    `json:"topic"`
	BotID     string    `json:"bot_id"`
	Summary   string    `json:"summary"`
	Markdown  string    `json:"markdown"`
	SourceIDs []string  `json:"source_ids,omitempty"` // 引用的 Note ID 或 Transcript Session ID
	UpdatedAt time.Time `json:"updated_at"`
}

// Manager 是单体记忆系统的读写管理器。
type Manager struct {
	baseDir string
	mu      sync.RWMutex
}

// NewManager 创建单体记忆管理器。若 baseDir 为空，默认使用 "data/bots"。
func NewManager(baseDir string) *Manager {
	if baseDir == "" {
		baseDir = filepath.Join("data", "bots")
	}
	return &Manager{baseDir: baseDir}
}

// botMemoryDir 返回特定 Bot 的记忆根目录: <baseDir>/<bot_id>/memory
func (m *Manager) botMemoryDir(botID string) string {
	return filepath.Join(m.baseDir, botID, "memory")
}

// EnsureDirs 初始化 Bot 的记忆四层栈目录结构。
func (m *Manager) EnsureDirs(botID string) error {
	if strings.TrimSpace(botID) == "" {
		return ErrInvalidBotID
	}
	memDir := m.botMemoryDir(botID)
	for _, sub := range []string{DirTranscripts, DirNotes, DirRepresentations} {
		p := filepath.Join(memDir, sub)
		if err := os.MkdirAll(p, 0755); err != nil {
			return fmt.Errorf("创建记忆目录 %s 失败: %w", p, err)
		}
	}
	// 若 MEMORY.md 不存在，创建初始模板
	memFile := filepath.Join(memDir, FileMemoryMD)
	if _, err := os.Stat(memFile); os.IsNotExist(err) {
		initial := fmt.Sprintf("# %s 长期记忆（Injected Context）\n\n- 角色与定位: 简单轻量 Agent\n- 运行约束: 严格禁止执行系统 Shell，所有物理操作经 Hub 调度受控 Agent；思考封顶 15s。\n", botID)
		_ = os.WriteFile(memFile, []byte(initial), 0644)
	}
	return nil
}

// RecordTranscript 追加一条原始流水到 Transcripts 层（Append-Only）。
func (m *Manager) RecordTranscript(botID string, entry TranscriptEntry) error {
	if strings.TrimSpace(botID) == "" {
		return ErrInvalidBotID
	}
	if err := m.EnsureDirs(botID); err != nil {
		return err
	}
	if entry.SessionID == "" {
		entry.SessionID = "default"
	}
	if entry.Timestamp.IsZero() {
		entry.Timestamp = time.Now()
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	filePath := filepath.Join(m.botMemoryDir(botID), DirTranscripts, entry.SessionID+".jsonl")
	f, err := os.OpenFile(filePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return fmt.Errorf("打开会话流水文件失败: %w", err)
	}
	defer f.Close()

	data, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("序列化流水条目失败: %w", err)
	}
	if _, err := f.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("写入流水条目失败: %w", err)
	}
	return nil
}

// ReadTranscripts 读取指定会话的所有原始会话流水。
func (m *Manager) ReadTranscripts(botID, sessionID string) ([]TranscriptEntry, error) {
	if strings.TrimSpace(botID) == "" {
		return nil, ErrInvalidBotID
	}
	if sessionID == "" {
		sessionID = "default"
	}
	filePath := filepath.Join(m.botMemoryDir(botID), DirTranscripts, sessionID+".jsonl")

	m.mu.RLock()
	defer m.mu.RUnlock()

	f, err := os.Open(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("打开会话流水失败: %w", err)
	}
	defer f.Close()

	var entries []TranscriptEntry
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var e TranscriptEntry
		if err := json.Unmarshal(line, &e); err != nil {
			return nil, fmt.Errorf("解析流水条目失败: %w", err)
		}
		entries = append(entries, e)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("扫描流水条目失败: %w", err)
	}
	return entries, nil
}

// SaveNote 保存或更新一条离散记忆笔记到 Memory Notes 层。
func (m *Manager) SaveNote(note *MemoryNote) error {
	if note == nil || strings.TrimSpace(note.BotID) == "" || strings.TrimSpace(note.ID) == "" {
		return fmt.Errorf("无效的记忆笔记对象")
	}
	if err := m.EnsureDirs(note.BotID); err != nil {
		return err
	}
	now := time.Now()
	if note.CreatedAt.IsZero() {
		note.CreatedAt = now
	}
	note.UpdatedAt = now

	m.mu.Lock()
	defer m.mu.Unlock()

	filePath := filepath.Join(m.botMemoryDir(note.BotID), DirNotes, note.ID+".json")
	data, err := json.MarshalIndent(note, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化记忆笔记失败: %w", err)
	}
	if err := os.WriteFile(filePath, data, 0644); err != nil {
		return fmt.Errorf("写入记忆笔记文件失败: %w", err)
	}
	return nil
}

// GetNote 获取单条记忆笔记。
func (m *Manager) GetNote(botID, noteID string) (*MemoryNote, error) {
	if strings.TrimSpace(botID) == "" || strings.TrimSpace(noteID) == "" {
		return nil, fmt.Errorf("参数不完整")
	}
	filePath := filepath.Join(m.botMemoryDir(botID), DirNotes, noteID+".json")

	m.mu.RLock()
	defer m.mu.RUnlock()

	data, err := os.ReadFile(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("读取记忆笔记失败: %w", err)
	}
	var note MemoryNote
	if err := json.Unmarshal(data, &note); err != nil {
		return nil, fmt.Errorf("解析记忆笔记失败: %w", err)
	}
	return &note, nil
}

// ListNotes 列出指定 Bot 的所有记忆笔记。
func (m *Manager) ListNotes(botID string) ([]*MemoryNote, error) {
	if strings.TrimSpace(botID) == "" {
		return nil, ErrInvalidBotID
	}
	dirPath := filepath.Join(m.botMemoryDir(botID), DirNotes)

	m.mu.RLock()
	defer m.mu.RUnlock()

	entries, err := os.ReadDir(dirPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("读取笔记目录失败: %w", err)
	}

	var notes []*MemoryNote
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dirPath, entry.Name()))
		if err != nil {
			continue
		}
		var note MemoryNote
		if err := json.Unmarshal(data, &note); err == nil {
			notes = append(notes, &note)
		}
	}
	return notes, nil
}

// SaveRepresentation 保存结构化主题 Wiki。
func (m *Manager) SaveRepresentation(rep *MemoryRepresentation) error {
	if rep == nil || strings.TrimSpace(rep.BotID) == "" || strings.TrimSpace(rep.Topic) == "" {
		return fmt.Errorf("无效的主题表征对象")
	}
	if err := m.EnsureDirs(rep.BotID); err != nil {
		return err
	}
	rep.UpdatedAt = time.Now()

	m.mu.Lock()
	defer m.mu.Unlock()

	filePath := filepath.Join(m.botMemoryDir(rep.BotID), DirRepresentations, rep.Topic+".md")
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("# 主题表征: %s\n\n", rep.Topic))
	sb.WriteString(fmt.Sprintf("> 摘要: %s\n", rep.Summary))
	sb.WriteString(fmt.Sprintf("> 更新时间: %s\n\n", rep.UpdatedAt.Format(time.RFC3339)))
	sb.WriteString(rep.Markdown)
	sb.WriteString("\n")

	if err := os.WriteFile(filePath, []byte(sb.String()), 0644); err != nil {
		return fmt.Errorf("写入主题表征失败: %w", err)
	}
	return nil
}

// ListRepresentations 获取所有结构化主题表征。
func (m *Manager) ListRepresentations(botID string) ([]*MemoryRepresentation, error) {
	if strings.TrimSpace(botID) == "" {
		return nil, ErrInvalidBotID
	}
	dirPath := filepath.Join(m.botMemoryDir(botID), DirRepresentations)

	m.mu.RLock()
	defer m.mu.RUnlock()

	entries, err := os.ReadDir(dirPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("读取主题表征目录失败: %w", err)
	}

	var reps []*MemoryRepresentation
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		topic := strings.TrimSuffix(entry.Name(), ".md")
		data, err := os.ReadFile(filepath.Join(dirPath, entry.Name()))
		if err != nil {
			continue
		}
		reps = append(reps, &MemoryRepresentation{
			Topic:     topic,
			BotID:     botID,
			Markdown:  string(data),
			UpdatedAt: time.Now(),
		})
	}
	return reps, nil
}

// LoadInjectedContext 加载 Injected Context (MEMORY.md)，并执行 ≤ 200 行铁律校验。
// 若文件超限，自动裁剪并返回告警截断后的内容。
func (m *Manager) LoadInjectedContext(botID string) (string, error) {
	if strings.TrimSpace(botID) == "" {
		return "", ErrInvalidBotID
	}
	if err := m.EnsureDirs(botID); err != nil {
		return "", err
	}
	filePath := filepath.Join(m.botMemoryDir(botID), FileMemoryMD)

	m.mu.RLock()
	defer m.mu.RUnlock()

	data, err := os.ReadFile(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("读取 MEMORY.md 失败: %w", err)
	}

	content := string(data)
	lines := CountLines(content)
	if lines > MaxMemoryLines {
		// 自动修剪前 200 行，并附带超限提示
		truncated := TruncateToMaxLines(content, MaxMemoryLines)
		return truncated, nil
	}
	return content, nil
}

// SaveInjectedContext 严格校验行数并写入 MEMORY.md。
func (m *Manager) SaveInjectedContext(botID string, content string) error {
	if strings.TrimSpace(botID) == "" {
		return ErrInvalidBotID
	}
	lines := CountLines(content)
	if lines > MaxMemoryLines {
		return fmt.Errorf("%w: 当前 %d 行，上限 %d 行", ErrMemoryExceedsLimit, lines, MaxMemoryLines)
	}
	if err := m.EnsureDirs(botID); err != nil {
		return err
	}
	filePath := filepath.Join(m.botMemoryDir(botID), FileMemoryMD)

	m.mu.Lock()
	defer m.mu.Unlock()

	if err := os.WriteFile(filePath, []byte(content), 0644); err != nil {
		return fmt.Errorf("写入 MEMORY.md 失败: %w", err)
	}
	return nil
}

// CountLines 计算字符串的行数。
func CountLines(content string) int {
	if len(content) == 0 {
		return 0
	}
	lines := 0
	scanner := bufio.NewScanner(strings.NewReader(content))
	for scanner.Scan() {
		lines++
	}
	return lines
}

// TruncateToMaxLines 将内容裁剪至最多 maxLines 行，若被裁剪追加警告标记。
func TruncateToMaxLines(content string, maxLines int) string {
	if maxLines <= 0 {
		return ""
	}
	scanner := bufio.NewScanner(strings.NewReader(content))
	var out []string
	// 预留 1 行放置警告标记
	targetLines := maxLines - 1
	if targetLines < 1 {
		targetLines = 1
	}
	cur := 0
	truncated := false
	for scanner.Scan() {
		cur++
		if cur > targetLines {
			truncated = true
			break
		}
		out = append(out, scanner.Text())
	}
	if truncated {
		out = append(out, fmt.Sprintf("<!-- [WARNING] 记忆超限，已强制自动裁剪至 %d 行以内以保证 Prompt Caching -->", maxLines))
	}
	return strings.Join(out, "\n")
}
