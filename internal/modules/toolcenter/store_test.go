package toolcenter

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestPersistentCatalogSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	svc, err := OpenToolCenterService(dir)
	if err != nil {
		t.Fatalf("打开工具目录失败: %v", err)
	}

	skillID := "skill.m12.notes"
	if err := svc.CreateSkill(ToolDefinition{
		ToolID: skillID,
		Name:   "笔记技能",
		Code:   "print('v1')",
	}); err != nil {
		t.Fatalf("创建 Skill 失败: %v", err)
	}
	if err := svc.UpdateSkill(skillID, "print('v2')", ""); err != nil {
		t.Fatalf("更新 Skill 失败: %v", err)
	}
	revs := svc.ListRevisions(skillID)
	if len(revs) != 1 || revs[0].Code != "print('v1')" {
		t.Fatalf("Skill 版本记录不符: %+v", revs)
	}

	botID := "user_m12_bot"
	if err := svc.GrantTool(BotToolGrant{
		BotID:      botID,
		ToolID:     skillID,
		Permission: PermissionExecute,
		GrantedBy:  "user_bot",
	}); err != nil {
		t.Fatalf("授权失败: %v", err)
	}

	if err := svc.GrantTool(BotToolGrant{
		BotID:      "user_other_bot",
		ToolID:     "sys.search.web",
		Permission: PermissionExecute,
		GrantedBy:  "user_bot",
	}); !errors.Is(err, ErrToolNotNamed) {
		t.Fatalf("未点名 Bot 应拒绝搜索授权，实际: %v", err)
	}
	if err := svc.SetNamedBots("sys.search.web", []string{botID}); err != nil {
		t.Fatalf("点名失败: %v", err)
	}
	if err := svc.GrantTool(BotToolGrant{
		BotID:      botID,
		ToolID:     "sys.search.web",
		Permission: PermissionExecute,
		GrantedBy:  "user_bot",
	}); err != nil {
		t.Fatalf("点名后授予搜索失败: %v", err)
	}

	if err := svc.CreateMCP(ToolDefinition{
		ToolID:      "mcp.github",
		Name:        "GitHub",
		EndpointRef: "stdio://github",
		SecretRef:   "secret://mcp/github",
		Code:        "token=plain",
	}); !errors.Is(err, ErrPlaintextSecret) {
		t.Fatalf("含源码或明文的 MCP 应被拒绝，实际: %v", err)
	}
	if err := svc.CreateMCP(ToolDefinition{
		ToolID:      "mcp.github",
		Name:        "GitHub",
		EndpointRef: "stdio://github",
		SecretRef:   "secret://mcp/github",
	}); err != nil {
		t.Fatalf("登记 MCP 失败: %v", err)
	}
	if err := svc.CreateMCP(ToolDefinition{
		ToolID:      "mcp.bad",
		Name:        "Bad",
		EndpointRef: "https://example.test?api_key=abcd",
	}); !errors.Is(err, ErrPlaintextSecret) {
		t.Fatalf("明文连接串应被拒绝，实际: %v", err)
	}

	_, err = svc.ExecuteTool("user_stranger_bot", skillID, map[string]interface{}{"q": "x"}, func() (string, int, error) {
		return "nope", 0, nil
	})
	if !errors.Is(err, ErrToolAccessDenied) {
		t.Fatalf("未授权调用应被拒绝，实际: %v", err)
	}
	traces := svc.ListTraces("user_stranger_bot")
	if len(traces) != 1 || traces[0].Allowed || traces[0].ToolID != skillID {
		t.Fatalf("未授权轨迹未留下: %+v", traces)
	}

	reopened, err := OpenToolCenterService(dir)
	if err != nil {
		t.Fatalf("重启后打开工具目录失败: %v", err)
	}
	got, err := reopened.GetTool(skillID)
	if err != nil || got.Code != "print('v2')" || got.Category != CategoryEditableSkill {
		t.Fatalf("重启后 Skill 丢失: %+v err=%v", got, err)
	}
	grants := reopened.ListGrants(botID)
	if len(grants) != 2 {
		t.Fatalf("重启后授权数量不符: %+v", grants)
	}
	search, err := reopened.GetTool("sys.search.web")
	if err != nil || !search.Bound || !containsBot(search.NamedBots, botID) {
		t.Fatalf("重启后搜索强绑点名丢失: %+v err=%v", search, err)
	}
	mcp, err := reopened.GetTool("mcp.github")
	if err != nil || mcp.SecretRef != "secret://mcp/github" || mcp.Code != "" {
		t.Fatalf("重启后 MCP 登记不符: %+v err=%v", mcp, err)
	}
	if reopened.ListRevisions(skillID)[0].Code != "print('v1')" {
		t.Fatalf("重启后版本记录丢失")
	}
	if len(reopened.ListTraces("user_stranger_bot")) != 1 {
		t.Fatalf("重启后轨迹丢失")
	}
	if filepath.Base(reopened.storePath) != "catalog.json" {
		t.Fatalf("存储文件名不符: %s", reopened.storePath)
	}
}
