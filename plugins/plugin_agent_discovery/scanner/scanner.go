package scanner

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// DiscoveredAgent 描述一个探测到的本地 Agent 资产
type DiscoveredAgent struct {
	AgentID      string    `json:"agent_id"`
	DisplayName  string    `json:"display_name"`
	BinName      string    `json:"bin_name"`
	BinPath      string    `json:"bin_path"`
	Version      string    `json:"version"`
	Capabilities []string  `json:"capabilities"`
	Modes        []string  `json:"modes"`
	Status       string    `json:"status"`
	DetectedAt   time.Time `json:"detected_at"`
}

// Scanner 负责遍历环境变量及特定目录搜寻 Coding Agent CLI
type Scanner struct {
	mu           sync.RWMutex
	candidates   []string
	extraDirs    []string
	cachedAgents []DiscoveredAgent
}

var defaultCandidates = []string{
	"claude",
	"codex",
	"pi",
	"agy",
	"aider",
	"gemini",
	"nexus-mock-agent",
}

func NewScanner(extraDirs ...string) *Scanner {
	return &Scanner{
		candidates: defaultCandidates,
		extraDirs:  extraDirs,
	}
}

// Scan 执行宿主机 CLI 探测
func (s *Scanner) Scan(ctx context.Context) ([]DiscoveredAgent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	searchDirs := s.collectSearchDirs()
	foundMap := make(map[string]DiscoveredAgent)

	for _, cand := range s.candidates {
		binPath := s.findExecutable(cand, searchDirs)
		if binPath == "" {
			continue
		}

		agent := s.probeAgent(ctx, cand, binPath)
		foundMap[agent.AgentID] = agent
	}

	result := make([]DiscoveredAgent, 0, len(foundMap))
	for _, a := range foundMap {
		result = append(result, a)
	}

	s.cachedAgents = result
	return result, nil
}

// GetCachedAgents 返回最近一次扫描缓存的资产
func (s *Scanner) GetCachedAgents() []DiscoveredAgent {
	s.mu.RLock()
	defer s.mu.RUnlock()
	copied := make([]DiscoveredAgent, len(s.cachedAgents))
	copy(copied, s.cachedAgents)
	return copied
}

func (s *Scanner) collectSearchDirs() []string {
	var dirs []string
	seen := make(map[string]bool)

	addDir := func(d string) {
		d = filepath.Clean(d)
		if fi, err := os.Stat(d); err == nil && fi.IsDir() && !seen[d] {
			seen[d] = true
			dirs = append(dirs, d)
		}
	}

	// 1. Extra 优先目录
	for _, d := range s.extraDirs {
		addDir(d)
	}

	// 2. PATH 环境变量
	pathEnv := os.Getenv("PATH")
	for _, p := range filepath.SplitList(pathEnv) {
		addDir(p)
	}

	// 3. 常见包管理器目录
	home, _ := os.UserHomeDir()
	if home != "" {
		addDir(filepath.Join(home, ".cargo", "bin"))
		addDir(filepath.Join(home, ".local", "bin"))
		// NVM 典型目录扫描
		nvmDir := filepath.Join(home, ".nvm", "versions", "node")
		if entries, err := os.ReadDir(nvmDir); err == nil {
			for _, entry := range entries {
				if entry.IsDir() {
					addDir(filepath.Join(nvmDir, entry.Name(), "bin"))
				}
			}
		}
	}

	addDir("/usr/local/bin")
	addDir("/usr/bin")

	return dirs
}

func (s *Scanner) findExecutable(name string, dirs []string) string {
	for _, d := range dirs {
		candidate := filepath.Join(d, name)
		if fi, err := os.Stat(candidate); err == nil && !fi.IsDir() && fi.Mode()&0111 != 0 {
			return candidate
		}
	}
	return ""
}

var versionRegex = regexp.MustCompile(`([0-9]+\.[0-9]+(\.[0-9]+(-[a-zA-Z0-9.]+)?)*)`)

func (s *Scanner) probeAgent(ctx context.Context, name, binPath string) DiscoveredAgent {
	probeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	cmd := exec.CommandContext(probeCtx, binPath, "--version")
	var outBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &outBuf

	versionStr := "unknown"
	if err := cmd.Run(); err == nil {
		output := strings.TrimSpace(outBuf.String())
		if match := versionRegex.FindString(output); match != "" {
			versionStr = match
		} else if len(output) > 0 {
			versionStr = strings.Split(output, "\n")[0]
		}
	}

	modes := []string{"non_interactive"}
	caps := []string{"cli_execution", "code_generation"}

	switch name {
	case "claude":
		modes = append(modes, "pipe", "interactive")
		caps = append(caps, "architecture_design", "git_commit")
	case "codex":
		modes = append(modes, "pipe")
		caps = append(caps, "codebase_search", "refactoring")
	case "pi":
		modes = append(modes, "pipe", "streaming")
		caps = append(caps, "lightweight_task")
	case "agy":
		modes = append(modes, "interactive", "daemon")
		caps = append(caps, "full_stack_agent")
	}

	return DiscoveredAgent{
		AgentID:      fmt.Sprintf("agent_%s", name),
		DisplayName:  strings.ToUpper(name) + " CLI",
		BinName:      name,
		BinPath:      binPath,
		Version:      versionStr,
		Capabilities: caps,
		Modes:        modes,
		Status:       "online",
		DetectedAt:   time.Now().UTC(),
	}
}
