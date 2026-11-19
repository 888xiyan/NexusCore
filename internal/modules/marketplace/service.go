// Package marketplace 是插件商场的无头目录。
// 下载只校验制品并生成安装提案；不把插件标成已安装，也不拉起进程。
// 安装批准仍走内核既有的 security 预审与 user_bot checksum。目录不进配置中心。
package marketplace

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/888xiyan/NexusCore/hub/config"
	"github.com/888xiyan/NexusCore/hub/plugin"
)

const maxDependencyDepth = 2

var (
	ErrDigestMismatch      = errors.New("摘要不符")
	ErrProposalRequired    = errors.New("尚未生成安装提案")
	ErrInstallNotPermitted = errors.New("商场只提交安装提案，不能把插件标成已安装")
	ErrNotInstalled        = errors.New("插件不在已安装集合")
	ErrReverseDependency   = errors.New("仍被依赖，拒绝单点卸载")
)

// Entry 是目录中的一项。目录项不是已安装插件。
type Entry struct {
	PluginID    string          `json:"plugin_id"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	RiskLevel   string          `json:"risk_level"`
	Tags        []string        `json:"tags"`
	Digest      string          `json:"digest"`
	Artifact    string          `json:"artifact"`
	Manifest    json.RawMessage `json:"manifest"`
}

// Hit 是一次检索结果，附带依赖、深度、环路和摘要是否匹配。
type Hit struct {
	PluginID     string   `json:"plugin_id"`
	Name         string   `json:"name"`
	Version      string   `json:"version"`
	Description  string   `json:"description"`
	RiskLevel    string   `json:"risk_level"`
	Tags         []string `json:"tags"`
	Dependencies []string `json:"dependencies"`
	Depth        int      `json:"depth"`
	MaxDepth     int      `json:"max_depth"`
	HasCycle     bool     `json:"has_cycle"`
	Digest       string   `json:"digest"`
	DigestMatch  bool     `json:"digest_match"`
	Installed    bool     `json:"installed"`
}

// Proposal 是下载成功后留下的安装提案。Installed 与 ProcessStarted 恒为 false。
type Proposal struct {
	ProposalID       string          `json:"proposal_id"`
	PluginID         string          `json:"plugin_id"`
	ArtifactDigest   string          `json:"artifact_digest"`
	ManifestChecksum string          `json:"manifest_checksum"`
	Manifest         json.RawMessage `json:"manifest"`
	Depth            int             `json:"depth"`
	Dependencies     []string        `json:"dependencies"`
	Status           string          `json:"status"`
	Installed        bool            `json:"installed"`
	ProcessStarted   bool            `json:"process_started"`
	CreatedAt        time.Time       `json:"created_at"`
}

type catalogFile struct {
	Plugins []Entry `json:"plugins"`
}

// Service 读写商场自己的目录。
type Service struct {
	mu        sync.Mutex
	dir       string
	entries   []Entry
	proposals []Proposal
}

// Open 读取目录文件。不存在的提案文件视为还没有提案。
func Open(dir string) (*Service, error) {
	if dir == "" {
		return nil, errors.New("商场目录不能为空")
	}
	raw, err := os.ReadFile(filepath.Join(dir, "catalog.json"))
	if err != nil {
		return nil, fmt.Errorf("读取商场目录: %w", err)
	}
	var cat catalogFile
	if err := json.Unmarshal(raw, &cat); err != nil {
		return nil, fmt.Errorf("解析商场目录: %w", err)
	}
	svc := &Service{dir: dir, entries: cat.Plugins}
	propPath := filepath.Join(dir, "proposals.json")
	if b, err := os.ReadFile(propPath); err == nil && len(b) > 0 {
		if err := json.Unmarshal(b, &svc.proposals); err != nil {
			return nil, fmt.Errorf("解析安装提案: %w", err)
		}
	}
	return svc, nil
}

// Search 用一句话匹配名称、说明、标签或插件 ID，并附上依赖检查。
func (s *Service) Search(query string) ([]Hit, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var hits []Hit
	for _, entry := range s.entries {
		if !matches(entry, query) {
			continue
		}
		hit, err := s.inspect(entry)
		if err != nil {
			return nil, err
		}
		hits = append(hits, hit)
	}
	if hits == nil {
		hits = []Hit{}
	}
	return hits, nil
}

// Download 校验制品摘要、环路和深度。通过后只写入提案。
func (s *Service) Download(pluginID, claimedDigest string) (Proposal, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.entry(pluginID)
	if !ok {
		return Proposal{}, fmt.Errorf("%w: 目录中没有 %s", plugin.ErrPluginNotFound, pluginID)
	}
	hit, err := s.inspect(entry)
	if err != nil {
		return Proposal{}, err
	}
	sum, err := s.hashArtifact(entry)
	if err != nil {
		return Proposal{}, err
	}
	claimed := strings.ToLower(strings.TrimSpace(claimedDigest))
	if claimed != "" && claimed != sum {
		return Proposal{}, fmt.Errorf("%w: 调用方给出的摘要与制品不一致", ErrDigestMismatch)
	}
	if !hit.DigestMatch || sum != strings.ToLower(entry.Digest) {
		return Proposal{}, fmt.Errorf("%w: 制品与目录声明不一致", ErrDigestMismatch)
	}
	if hit.HasCycle {
		return Proposal{}, fmt.Errorf("循环依赖: %w", plugin.ErrCircularDependency)
	}
	if hit.Depth > maxDependencyDepth {
		return Proposal{}, fmt.Errorf("依赖层级超过最大限制: %w", plugin.ErrExceedsMaxDependencyDepth)
	}
	manifestSum, err := checksumManifest(entry.Manifest)
	if err != nil {
		return Proposal{}, err
	}
	prop := Proposal{
		ProposalID:       fmt.Sprintf("mp_%d", time.Now().UnixNano()),
		PluginID:         pluginID,
		ArtifactDigest:   sum,
		ManifestChecksum: manifestSum,
		Manifest:         append(json.RawMessage(nil), entry.Manifest...),
		Depth:            hit.Depth,
		Dependencies:     hit.Dependencies,
		Status:           "proposed",
		Installed:        false,
		ProcessStarted:   false,
		CreatedAt:        time.Now().UTC(),
	}
	s.proposals = append(s.proposals, prop)
	if err := s.saveProposals(); err != nil {
		return Proposal{}, err
	}
	return prop, nil
}

// LatestProposal 返回该插件最近一次下载提案。
func (s *Service) LatestProposal(pluginID string) (Proposal, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := len(s.proposals) - 1; i >= 0; i-- {
		if s.proposals[i].PluginID == pluginID {
			return s.proposals[i], true
		}
	}
	return Proposal{}, false
}

// RejectInstall 拒绝商场把提案直接装成已安装插件。
func (s *Service) RejectInstall(pluginID string) (Proposal, error) {
	prop, ok := s.LatestProposal(pluginID)
	if !ok {
		return Proposal{}, fmt.Errorf("%w: %s", ErrProposalRequired, pluginID)
	}
	prop.Installed = false
	prop.ProcessStarted = false
	return prop, fmt.Errorf("%w: %s", ErrInstallNotPermitted, pluginID)
}

// PlanUninstall 根据商场记录的已安装集合给出逆拓扑。不执行停机。
func (s *Service) PlanUninstall(pluginID string, cascade bool) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	raw, err := os.ReadFile(filepath.Join(s.dir, "installed.json"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%w: %s", ErrNotInstalled, pluginID)
		}
		return nil, fmt.Errorf("读取已安装集合: %w", err)
	}
	var manifests []json.RawMessage
	if err := json.Unmarshal(raw, &manifests); err != nil {
		return nil, fmt.Errorf("解析已安装集合: %w", err)
	}
	plugins := map[string]*plugin.Manifest{}
	for _, item := range manifests {
		var mf plugin.Manifest
		if err := json.Unmarshal(item, &mf); err != nil {
			return nil, fmt.Errorf("解析已安装清单: %w", err)
		}
		if mf.PluginID != "" {
			plugins[mf.PluginID] = &mf
		}
	}
	if _, ok := plugins[pluginID]; !ok {
		return nil, fmt.Errorf("%w: %s", ErrNotInstalled, pluginID)
	}
	dag, err := plugin.BuildDAG(plugins, nil)
	if err != nil {
		return nil, err
	}
	order, err := plugin.CheckUninstallReverseDependencies(dag, pluginID, cascade)
	if err != nil {
		if errors.Is(err, plugin.ErrReverseDependencyBlocked) {
			return nil, fmt.Errorf("%w: %v", ErrReverseDependency, err)
		}
		return nil, err
	}
	return order, nil
}

func (s *Service) entry(pluginID string) (Entry, bool) {
	for _, entry := range s.entries {
		if entry.PluginID == pluginID {
			return entry, true
		}
	}
	return Entry{}, false
}

func (s *Service) inspect(entry Entry) (Hit, error) {
	var mf plugin.Manifest
	if err := json.Unmarshal(entry.Manifest, &mf); err != nil {
		return Hit{}, fmt.Errorf("解析目录清单 %s: %w", entry.PluginID, err)
	}
	if mf.PluginID == "" {
		mf.PluginID = entry.PluginID
	}
	nodes := map[string]*plugin.Manifest{}
	var walk func(string) error
	walk = func(id string) error {
		if _, seen := nodes[id]; seen {
			return nil
		}
		var current plugin.Manifest
		if id == mf.PluginID {
			current = mf
		} else {
			dep, ok := s.entry(id)
			if !ok {
				return fmt.Errorf("%w: %s 依赖的 %s 不在目录中", plugin.ErrUnmetDependency, entry.PluginID, id)
			}
			if err := json.Unmarshal(dep.Manifest, &current); err != nil {
				return fmt.Errorf("解析依赖清单 %s: %w", id, err)
			}
			if current.PluginID == "" {
				current.PluginID = id
			}
		}
		nodes[current.PluginID] = &current
		for _, depID := range current.Deps {
			if err := walk(depID); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk(mf.PluginID); err != nil {
		return Hit{}, err
	}
	dag, err := plugin.BuildDAG(nodes, nil)
	if err != nil {
		return Hit{}, err
	}
	hasCycle, _ := plugin.DetectCycle(dag)
	depth := 0
	if !hasCycle {
		depth = nodeDepth(dag, mf.PluginID)
	}
	sum, hashErr := s.hashArtifact(entry)
	digestMatch := hashErr == nil && sum == strings.ToLower(strings.TrimSpace(entry.Digest))
	deps := append([]string{}, mf.Deps...)
	for _, spec := range mf.Dependencies {
		if spec.Interface != "" {
			deps = append(deps, spec.Interface)
		}
	}
	if deps == nil {
		deps = []string{}
	}
	return Hit{
		PluginID:     entry.PluginID,
		Name:         entry.Name,
		Version:      mf.Version,
		Description:  entry.Description,
		RiskLevel:    entry.RiskLevel,
		Tags:         entry.Tags,
		Dependencies: deps,
		Depth:        depth,
		MaxDepth:     maxDependencyDepth,
		HasCycle:     hasCycle,
		Digest:       strings.ToLower(strings.TrimSpace(entry.Digest)),
		DigestMatch:  digestMatch,
		Installed:    false,
	}, nil
}

func (s *Service) hashArtifact(entry Entry) (string, error) {
	path := entry.Artifact
	if path == "" {
		return "", fmt.Errorf("%w: %s 没有制品路径", ErrDigestMismatch, entry.PluginID)
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(s.dir, path)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("读取制品 %s: %w", entry.PluginID, err)
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:]), nil
}

func (s *Service) saveProposals() error {
	body, err := json.MarshalIndent(s.proposals, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化安装提案: %w", err)
	}
	path := filepath.Join(s.dir, "proposals.json")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, body, 0o644); err != nil {
		return fmt.Errorf("写入安装提案: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("保存安装提案: %w", err)
	}
	return nil
}

func checksumManifest(raw json.RawMessage) (string, error) {
	spec, err := config.DecodeSpec(raw)
	if err != nil {
		return "", fmt.Errorf("规范化清单: %w", err)
	}
	sum, err := config.Checksum(spec)
	if err != nil {
		return "", fmt.Errorf("计算清单 checksum: %w", err)
	}
	return sum, nil
}

func nodeDepth(dag *plugin.DependencyDAG, id string) int {
	memo := map[string]int{}
	var depth func(string) int
	depth = func(nodeID string) int {
		if d, ok := memo[nodeID]; ok {
			return d
		}
		deps := dag.Dependencies[nodeID]
		if len(deps) == 0 {
			memo[nodeID] = 1
			return 1
		}
		maxDep := 0
		for _, depID := range deps {
			if d := depth(depID); d > maxDep {
				maxDep = d
			}
		}
		d := 1 + maxDep
		memo[nodeID] = d
		return d
	}
	return depth(id)
}

func matches(entry Entry, query string) bool {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return true
	}
	blob := strings.ToLower(strings.Join([]string{
		entry.PluginID, entry.Name, entry.Description, strings.Join(entry.Tags, " "),
	}, " "))
	if strings.Contains(blob, q) || strings.Contains(q, strings.ToLower(entry.Name)) && entry.Name != "" {
		return true
	}
	for _, tag := range entry.Tags {
		tag = strings.ToLower(strings.TrimSpace(tag))
		if tag != "" && strings.Contains(q, tag) {
			return true
		}
	}
	for _, tok := range strings.FieldsFunc(q, func(r rune) bool {
		return r == ' ' || r == ',' || r == '，' || r == '。' || r == '、'
	}) {
		if len([]rune(tok)) >= 2 && strings.Contains(blob, tok) {
			return true
		}
	}
	return false
}
