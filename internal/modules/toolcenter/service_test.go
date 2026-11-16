package toolcenter

import (
	"errors"
	"testing"
	"time"
)

func TestToolCenterService_BuiltinAndSkills(t *testing.T) {
	svc := NewToolCenterService()

	// 1. 验证内置系统工具
	tools := svc.ListTools("")
	if len(tools) < 3 {
		t.Fatalf("预期至少装载 3 个内置工具，实际获取: %d", len(tools))
	}

	sysTool, err := svc.GetTool("sys.probe.cli")
	if err != nil {
		t.Fatalf("获取内置探针工具失败: %v", err)
	}
	if sysTool.IsEditable {
		t.Errorf("内置系统工具不能标记为可编辑")
	}

	// 2. 尝试修改系统不可编辑工具源码应当拦截
	err = svc.UpdateSkill("sys.probe.cli", "print('hacked')", "")
	if !errors.Is(err, ErrToolNotEditable) {
		t.Errorf("预期拦截不可编辑系统工具修改，实际: %v", err)
	}
	err = svc.DeleteSkill("sys.probe.cli")
	if !errors.Is(err, ErrToolNotEditable) {
		t.Errorf("预期拦截不可编辑系统工具删除，实际: %v", err)
	}

	// 3. 创建可编辑技能 (Skill)
	skillID := "skill.python.summary"
	customSkill := ToolDefinition{
		ToolID:             skillID,
		Name:               "文章智能提炼技能",
		Description:        "提取超长文本的结构化要点",
		Category:           CategoryEditableSkill,
		IsEditable:         true,
		Runtime:            "python",
		Code:               "def run(text): return text[:100]",
		ConfigSchema:       `{"type":"object"}`,
		NetworkRequirement: NetworkNone,
		Tier:               Tier1SafeControlled,
		Version:            "0.1.0",
	}

	if err := svc.CreateSkill(customSkill); err != nil {
		t.Fatalf("创建自定义 Skill 失败: %v", err)
	}

	// 重复创建应当拦截
	if err := svc.CreateSkill(customSkill); !errors.Is(err, ErrToolAlreadyExists) {
		t.Errorf("预期拦截重复创建，实际: %v", err)
	}

	// 更新技能源码
	newCode := "def run(text): return 'Summary: ' + text[:50]"
	if err := svc.UpdateSkill(skillID, newCode, ""); err != nil {
		t.Fatalf("更新自定义 Skill 源码失败: %v", err)
	}

	updated, err := svc.GetTool(skillID)
	if err != nil || updated.Code != newCode {
		t.Fatalf("获取更新后的 Skill 失败或源码不匹配: code=%s", updated.Code)
	}

	// 删除技能
	if err := svc.DeleteSkill(skillID); err != nil {
		t.Fatalf("删除 Skill 失败: %v", err)
	}
	if _, err := svc.GetTool(skillID); !errors.Is(err, ErrToolNotFound) {
		t.Errorf("预期工具已被删除，实际: %v", err)
	}
}

func TestToolCenterService_GrantsAndAccess(t *testing.T) {
	svc := NewToolCenterService()
	botID := "coder_bot"
	toolID := "sys.probe.cli"

	// 1. 未授权前调用应当被拦截
	err := svc.CheckAccess(botID, toolID, PermissionExecute)
	if !errors.Is(err, ErrToolAccessDenied) {
		t.Fatalf("未授权前预期拦截，实际: %v", err)
	}

	// 2. 超级用户全局特权
	if err := svc.CheckAccess("user_bot", toolID, PermissionAdmin); err != nil {
		t.Errorf("user_bot 应当拥有特权放行，实际: %v", err)
	}

	// 3. 授予只读权限
	grant := BotToolGrant{
		BotID:      botID,
		ToolID:     toolID,
		Permission: PermissionReadOnly,
		GrantedBy:  "user_bot",
	}
	if err := svc.GrantTool(grant); err != nil {
		t.Fatalf("授权失败: %v", err)
	}

	// 验证列表
	grants := svc.ListGrants(botID)
	if len(grants) != 1 || grants[0].ToolID != toolID {
		t.Fatalf("获取授权列表不符: %+v", grants)
	}

	// 只读权限执行应当被拦截
	err = svc.CheckAccess(botID, toolID, PermissionExecute)
	if !errors.Is(err, ErrToolAccessDenied) {
		t.Errorf("只读权限预期无法执行，实际: %v", err)
	}

	// 4. 升级为可执行权限
	grant.Permission = PermissionExecute
	if err := svc.GrantTool(grant); err != nil {
		t.Fatalf("升级授权失败: %v", err)
	}
	if err := svc.CheckAccess(botID, toolID, PermissionExecute); err != nil {
		t.Errorf("预期拥有执行权限，实际报错: %v", err)
	}

	// 5. 吊销权限
	if err := svc.RevokeGrant(botID, toolID); err != nil {
		t.Fatalf("吊销权限失败: %v", err)
	}
	if err := svc.CheckAccess(botID, toolID, PermissionExecute); !errors.Is(err, ErrToolAccessDenied) {
		t.Errorf("吊销后预期拦截，实际: %v", err)
	}
}

func TestToolCenterService_ExecutionAndNetworkAudit(t *testing.T) {
	svc := NewToolCenterService()
	botID := "search_bot"
	searchTool := "sys.search.web"

	// 授予执行权限
	_ = svc.GrantTool(BotToolGrant{
		BotID:      botID,
		ToolID:     searchTool,
		Permission: PermissionExecute,
	})

	// 注册外联网络回调监听
	notified := false
	var egressTarget string
	svc.SetNetworkAuditCallback(func(b, tool, target string, ts time.Time) {
		if b == botID && tool == searchTool {
			notified = true
			egressTarget = target
		}
	})

	// 执行网络搜索工具
	res, err := svc.ExecuteTool(botID, searchTool, map[string]interface{}{
		"query": "NexusCore architecture",
	}, func() (string, int, error) {
		return "Found 10 results", 0, nil
	})

	if err != nil {
		t.Fatalf("执行工具报错: %v", err)
	}
	if res.ExitCode != 0 || res.Output != "Found 10 results" {
		t.Errorf("工具执行结果不符合预期: %+v", res)
	}
	if !res.NetworkTriggered {
		t.Errorf("预期标记 network_triggered = true")
	}
	if !notified || egressTarget != "NexusCore architecture" {
		t.Errorf("网络审计告警未触发或目标不匹配: notified=%v, target=%s", notified, egressTarget)
	}

	// 未授权执行拦截
	_, err = svc.ExecuteTool("unauthorized_bot", searchTool, nil, func() (string, int, error) {
		return "should not run", 0, nil
	})
	if !errors.Is(err, ErrToolAccessDenied) {
		t.Errorf("未授权执行预期拦截，实际: %v", err)
	}
}
