package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
)

func parseAgentBinding(raw json.RawMessage) (agentID, agentCLI string) {
	var caps []string
	if len(raw) == 0 || string(raw) == "null" {
		return "", ""
	}
	if err := json.Unmarshal(raw, &caps); err != nil {
		return "", ""
	}
	for _, cap := range caps {
		switch {
		case strings.HasPrefix(cap, "agent-id:"):
			agentID = strings.TrimPrefix(cap, "agent-id:")
		case strings.HasPrefix(cap, "agent:"):
			agentID = strings.TrimPrefix(cap, "agent:")
		case strings.HasPrefix(cap, "agent-cli:"):
			agentCLI = strings.TrimPrefix(cap, "agent-cli:")
		case strings.HasPrefix(cap, "cli:"):
			agentCLI = strings.TrimPrefix(cap, "cli:")
		}
	}
	return agentID, agentCLI
}

// mergeAgentBinding 用搜寻到的探针替换 agent-id。agentCLI 为 nil 时保留原有 CLI 能力。
func mergeAgentBinding(raw json.RawMessage, agentID string, agentCLI *string) ([]string, error) {
	var caps []string
	if len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &caps); err != nil {
			return nil, err
		}
	}
	next := make([]string, 0, len(caps)+2)
	for _, cap := range caps {
		if strings.HasPrefix(cap, "agent-id:") || strings.HasPrefix(cap, "agent:") {
			continue
		}
		if agentCLI != nil && (strings.HasPrefix(cap, "agent-cli:") || strings.HasPrefix(cap, "cli:")) {
			continue
		}
		next = append(next, cap)
	}
	if agentID != "" {
		next = append(next, "agent-id:"+agentID)
	}
	if agentCLI != nil && *agentCLI != "" {
		next = append(next, "agent-cli:"+*agentCLI)
	}
	return next, nil
}

func (a *API) handleBotBinding(w http.ResponseWriter, r *http.Request) {
	if a.st == nil {
		writeErr(w, http.StatusServiceUnavailable, "数据库未连接")
		return
	}
	botID := r.PathValue("bot_id")
	if botID == "" {
		writeErr(w, http.StatusBadRequest, "bot_id 不能为空")
		return
	}
	var req struct {
		AgentID  string  `json:"agent_id"`
		AgentCLI *string `json:"agent_cli"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}
	req.AgentID = strings.TrimSpace(req.AgentID)
	if req.AgentID != "" {
		var one int
		err := a.st.Pool().QueryRow(r.Context(), `SELECT 1 FROM kernel_agents WHERE agent_id=$1`, req.AgentID).Scan(&one)
		if errors.Is(err, pgx.ErrNoRows) {
			writeErr(w, http.StatusBadRequest, "探针不在搜寻结果中")
			return
		}
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	var raw json.RawMessage
	err := a.st.Pool().QueryRow(r.Context(), `SELECT capabilities FROM bot_manifest WHERE bot_id=$1`, botID).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		writeErr(w, http.StatusNotFound, "bot 不存在")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	caps, err := mergeAgentBinding(raw, req.AgentID, req.AgentCLI)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	body, err := json.Marshal(caps)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	tag, err := a.st.Pool().Exec(r.Context(), `UPDATE bot_manifest SET capabilities=$2, updated_at=now() WHERE bot_id=$1`, botID, body)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "写入探针绑定: "+err.Error())
		return
	}
	if tag.RowsAffected() == 0 {
		writeErr(w, http.StatusNotFound, "bot 不存在")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":           "saved",
		"bot_id":           botID,
		"bound_agent_id":   req.AgentID,
		"restart_required": true,
	})
}
