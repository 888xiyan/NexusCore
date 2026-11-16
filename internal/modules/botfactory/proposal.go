package botfactory

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// CanonicalJSON 生成无空白紧凑且键排序的规范化 JSON 字节流。
func CanonicalJSON(v any) ([]byte, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("序列化配置对象失败: %w", err)
	}

	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var decoded any
	if err := dec.Decode(&decoded); err != nil {
		return nil, fmt.Errorf("解码规范化配置失败: %w", err)
	}

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(decoded); err != nil {
		return nil, fmt.Errorf("规范化编码失败: %w", err)
	}

	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// ComputeChecksum 计算规范化 JSON 的 64 位 SHA-256 小写十六进制校验和。
func ComputeChecksum(v any) (string, error) {
	c, err := CanonicalJSON(v)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(c)
	return hex.EncodeToString(sum[:]), nil
}

// CreateBotProposal 编排并生成符合内核配置中心（ConfigCenter）规范的 Bot 提案。
func CreateBotProposal(bp BotBlueprint, proposerBotID string) (*Proposal, error) {
	proposer := strings.TrimSpace(proposerBotID)
	if proposer == "" {
		return nil, ErrEmptyProposer
	}

	assembled := AssembleBotConfig(bp)
	canonicalBytes, err := CanonicalJSON(assembled)
	if err != nil {
		return nil, err
	}

	checksum, err := ComputeChecksum(assembled)
	if err != nil {
		return nil, err
	}

	now := time.Now().UnixMilli()
	proposalID := fmt.Sprintf("prop_bot_%s_%d", bp.BotID, now)

	return &Proposal{
		ProposalID: proposalID,
		ScopeType:  "bot",
		ScopeID:    bp.BotID,
		ConfigKey:  "default",
		Spec:       canonicalBytes,
		Checksum:   checksum,
		Status:     "proposed",
		CreatedBy:  proposer,
		CreatedAt:  now,
	}, nil
}
