package modules_test

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/888xiyan/NexusCore/hub/plugin"
	"github.com/888xiyan/NexusCore/internal/envelope"
	"github.com/888xiyan/NexusCore/internal/modules/agentmesh"
	"github.com/888xiyan/NexusCore/internal/modules/botfactory"
	"github.com/888xiyan/NexusCore/internal/modules/groupchat"
)

// ScenarioHarness 统一测试脚手架，支持环境自适应 (默认 100% In-Memory / Mock Supervisor)
type ScenarioHarness struct {
	GC        groupchat.CoreService
	AM        agentmesh.CoreService
	AMReg     *agentmesh.MeshRegistry
	BF        botfactory.CoreService
	MockSuper *MockSupervisor
}

// MockSupervisor 模拟内核 Supervisor 进程管理器
type MockSupervisor struct {
	mu            sync.Mutex
	RestartedBots []string
}

func (m *MockSupervisor) Restart(ctx context.Context, botID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.RestartedBots = append(m.RestartedBots, botID)
	return nil
}

func (m *MockSupervisor) GetRestartedBots() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]string, len(m.RestartedBots))
	copy(cp, m.RestartedBots)
	return cp
}

func newScenarioHarness(t *testing.T) *ScenarioHarness {
	mockSuper := &MockSupervisor{}
	amReg := agentmesh.NewMeshRegistry()
	return &ScenarioHarness{
		GC:        groupchat.NewService(),
		AM:        agentmesh.NewService(agentmesh.WithRegistry(amReg)),
		AMReg:     amReg,
		BF:        botfactory.NewService(botfactory.WithRestartCoordinator(mockSuper.Restart)),
		MockSuper: mockSuper,
	}
}

// -------------------------------------------------------------
// Scenario 1: PM GroupChat Collaboration (Scenario A)
// Features: F01, F02, F03, F06, F19, F21
// -------------------------------------------------------------
func TestScenario1_PMGroupChatCollaboration(t *testing.T) {
	ctx := context.Background()
	h := newScenarioHarness(t)

	// 1. Governance Registration & Topological Ordering (F19, F21)
	plugins := map[string]*plugin.Manifest{
		"plugin_groupchat": {
			PluginID: "plugin_groupchat",
			Version:  "1.0.0",
		},
		"plugin_pm": {
			PluginID: "plugin_pm",
			Version:  "1.0.0",
			Dependencies: []plugin.DependencySpec{
				{
					Interface:    "nexus.groupchat.v1",
					VersionRange: "^1.0.0",
					Optional:     false,
				},
			},
		},
	}
	providerIndex := map[string]string{
		"nexus.groupchat.v1": "plugin_groupchat",
	}
	dag, err := plugin.BuildDAG(plugins, providerIndex)
	if err != nil {
		t.Fatalf("BuildDAG failed: %v", err)
	}
	order, err := plugin.TopologicalSort(dag)
	if err != nil {
		t.Fatalf("TopologicalSort failed: %v", err)
	}
	if len(order) != 2 || order[0] != "plugin_groupchat" || order[1] != "plugin_pm" {
		t.Fatalf("unexpected order: %v", order)
	}

	// 2. Headless Room Creation (F01)
	room, err := h.GC.CreateRoom(ctx, groupchat.CreateRoomRequest{
		RoomID:  "room_issue_1024",
		Topic:   "缺陷排查: Memory leak in auth cache",
		OwnerID: "plugin_pm",
		Members: []string{"user_qa_bot", "user_backend_bot", "user_human"},
		Metadata: map[string]string{
			"issue_id":   "ISSUE-1024",
			"project_id": "nexus_core",
		},
	})
	if err != nil {
		t.Fatalf("CreateRoom failed: %v", err)
	}
	if room.RoomID != "room_issue_1024" || room.Closed {
		t.Fatalf("unexpected room state: %+v", room)
	}

	// 3. User Mention Dispatch & Envelope Building (F02, F06)
	postRes, err := h.GC.PostMessage(ctx, groupchat.PostMessageRequest{
		RoomID:      room.RoomID,
		SenderID:    "user_human",
		Role:        groupchat.RoleUser,
		Content:     "@user_qa_bot @user_backend_bot 请复现并定位此缺陷",
		ClientToken: "token_msg_1",
	})
	if err != nil {
		t.Fatalf("PostMessage user failed: %v", err)
	}
	if len(postRes.TriggeredBots) != 2 {
		t.Fatalf("expected 2 triggered bots, got: %v", postRes.TriggeredBots)
	}

	// Verify Ingress Envelope (F06)
	env, err := groupchat.BuildIngressEnvelope(postRes.Message, "user_qa_bot", "user_human")
	if err != nil {
		t.Fatalf("BuildIngressEnvelope failed: %v", err)
	}
	if env.Channel != envelope.ChannelIM || env.MsgType != envelope.MsgRequest {
		t.Fatalf("invalid envelope header: channel=%s, type=%s", env.Channel, env.MsgType)
	}
	if err := env.Validate(); err != nil {
		t.Fatalf("envelope validation failed: %v", err)
	}

	// 4. Bot Reply Anti-Cascade Safety Check (F02)
	botRes, err := h.GC.PostMessage(ctx, groupchat.PostMessageRequest{
		RoomID:      room.RoomID,
		SenderID:    "user_qa_bot",
		Role:        groupchat.RoleBot,
		Content:     "@user_backend_bot 我已复现，确认是 Goroutine 泄漏！",
		ClientToken: "token_msg_2",
	})
	if err != nil {
		t.Fatalf("PostMessage bot failed: %v", err)
	}
	// CRITICAL SAFETY ASSERTION: Bot mention must never trigger another bot
	if len(botRes.TriggeredBots) != 0 {
		t.Fatalf("ANTI-CASCADE VIOLATION: Bot reply triggered downstream bots: %v", botRes.TriggeredBots)
	}

	// 5. Sliding Window & History Pagination (F03)
	history, err := h.GC.GetHistory(ctx, room.RoomID, 10, "")
	if err != nil {
		t.Fatalf("GetHistory failed: %v", err)
	}
	if len(history) != 2 {
		t.Fatalf("expected 2 messages in history, got %d", len(history))
	}
	if history[0].MessageID != postRes.Message.MessageID || history[1].MessageID != botRes.Message.MessageID {
		t.Fatalf("history chronological order mismatch")
	}
}

// -------------------------------------------------------------
// Scenario 2: CI/CD Probe Build Distillation (Scenario B)
// Features: F07, F09, F12, F13, F19, F21
// -------------------------------------------------------------
func TestScenario2_CICDProbeBuildDistillation(t *testing.T) {
	ctx := context.Background()
	h := newScenarioHarness(t)

	// 1. Governance Registration (F19, F21)
	plugins := map[string]*plugin.Manifest{
		"plugin_agent_mesh": {PluginID: "plugin_agent_mesh", Version: "1.0.0"},
		"plugin_cicd": {
			PluginID: "plugin_cicd",
			Version:  "1.0.0",
			Dependencies: []plugin.DependencySpec{
				{Interface: "nexus.agent_mesh.v1", VersionRange: ">=1.0.0 <2.0.0"},
			},
		},
	}
	dag, err := plugin.BuildDAG(plugins, map[string]string{"nexus.agent_mesh.v1": "plugin_agent_mesh"})
	if err != nil {
		t.Fatalf("BuildDAG failed: %v", err)
	}
	order, err := plugin.TopologicalSort(dag)
	if err != nil || order[0] != "plugin_agent_mesh" {
		t.Fatalf("unexpected order: %v", order)
	}

	// 2. Probe Registration & Heartbeat Tracking (F07, F12)
	probeID := "local_agent_probe"
	err = h.AMReg.RegisterProbe(&agentmesh.ProbeInfo{
		AgentID:      probeID,
		DeviceName:   "Workstation-Linux",
		Capabilities: []string{"nexus.bash.v1", "nexus.cli.codex"},
		Status:       agentmesh.ProbeStatusOnline,
	})
	if err != nil {
		t.Fatalf("RegisterProbe failed: %v", err)
	}

	err = h.AM.RegisterCLIDriver(ctx, probeID, agentmesh.CLIDriverManifest{
		DriverName: "bash",
		BinPath:    "/bin/bash",
		MaxTimeout: 30,
	})
	if err != nil {
		t.Fatalf("RegisterCLIDriver failed: %v", err)
	}

	probes, err := h.AM.ListProbes(ctx, agentmesh.ProbeFilter{})
	if err != nil || len(probes) == 0 {
		t.Fatalf("ListProbes failed: %v", err)
	}
	if probes[0].Status != agentmesh.ProbeStatusOnline {
		t.Fatalf("expected online status, got: %s", probes[0].Status)
	}

	// 3. Two-Stage Noise Interception & Build Error Distillation (F09, F13)
	// Simulate 200 lines of standard verbose build noise + ANSI colors + spinner frames + 1 compiler error
	var noiseBuilder strings.Builder
	for i := 0; i < 200; i++ {
		noiseBuilder.WriteString(fmt.Sprintf("\x1b[32m[INFO]\x1b[0m Compiling module package_%d...\n", i))
		if i%50 == 0 {
			noiseBuilder.WriteString("⠋ Resolving dependencies...\n")
		}
	}
	noiseBuilder.WriteString("internal/auth/cache.go:88:12: syntax error: unexpected newline, expecting comma or }\n")
	noiseBuilder.WriteString("FAIL: make build returned exit code 2\n")

	rawStdout := noiseBuilder.String()
	distilled, rawRef := agentmesh.DistillOutput(rawStdout, "", 2, 1200, agentmesh.DistillRuleCompilerErrorOnly)

	if rawRef == "" {
		t.Fatalf("expected non-empty rawRef")
	}
	if strings.Contains(distilled, "\x1b[") {
		t.Fatalf("distilled output contains ANSI escape codes")
	}
	if strings.Contains(distilled, "⠋") {
		t.Fatalf("distilled output contains spinner artifacts")
	}
	if !strings.Contains(distilled, "internal/auth/cache.go:88:12: syntax error") {
		t.Fatalf("distilled output lost crucial compiler error line: %s", distilled)
	}
	lineCount := len(strings.Split(strings.TrimSpace(distilled), "\n"))
	if lineCount > 5 {
		t.Fatalf("distilled output is too long (expected <= 5 lines, got %d): %s", lineCount, distilled)
	}

	// 4. Heartbeat expiration and fail-fast (F12)
	p, err := h.AMReg.GetProbe(probeID)
	if err != nil {
		t.Fatalf("GetProbe failed: %v", err)
	}
	p.LastHeartbeat = time.Now().Unix() - 35
	_ = h.AMReg.RegisterProbe(p)

	offlineList := h.AMReg.CheckHeartbeats(time.Now())
	if len(offlineList) == 0 || offlineList[0] != probeID {
		t.Fatalf("expected probe to be flagged offline by CheckHeartbeats: %v", offlineList)
	}

	// Subsequent execution fails fast with ErrPeerUnavailable
	_, err = h.AM.Execute(ctx, agentmesh.ExecutionRequest{
		AgentID: probeID,
		Payload: []byte(`{"cmd": "echo test"}`),
	})
	if err != agentmesh.ErrPeerUnavailable {
		t.Fatalf("expected ErrPeerUnavailable for offline probe, got: %v", err)
	}
}

// -------------------------------------------------------------
// Scenario 3: Specialized Bot Derivation (Scenario C)
// Features: F14, F15, F17, F18, F19, F21
// -------------------------------------------------------------
func TestScenario3_SpecializedBotDerivation(t *testing.T) {
	ctx := context.Background()
	h := newScenarioHarness(t)

	// 1. Reserved Name Blacklist Interception (F14)
	reservedBP := botfactory.BotBlueprint{
		BotID:         "security_bot",
		DisplayName:   "Illegal Security Bot",
		Role:          "reviewer",
		SystemPrompt:  "test",
		InitialMemory: "# notes\n",
	}
	valRes, err := h.BF.ValidateBlueprint(ctx, reservedBP)
	if err != nil {
		t.Fatalf("ValidateBlueprint error: %v", err)
	}
	if valRes.Valid {
		t.Fatalf("expected validation failure for reserved name 'security_bot'")
	}

	// 2. 200 Lines Memory Hard Constraint Interception (F14)
	var mem201 strings.Builder
	for i := 0; i < 201; i++ {
		mem201.WriteString(fmt.Sprintf("- rule line %d\n", i))
	}
	overflowBP := botfactory.BotBlueprint{
		BotID:         "user_overflow_bot",
		DisplayName:   "Overflow Bot",
		Role:          "reviewer",
		SystemPrompt:  "test",
		InitialMemory: mem201.String(),
	}
	valRes2, _ := h.BF.ValidateBlueprint(ctx, overflowBP)
	if valRes2.Valid {
		t.Fatalf("expected validation failure for > 200 lines memory")
	}

	// 3. Valid Blueprint & Proposal Submission (F14, F15)
	validBP := botfactory.BotBlueprint{
		BotID:         "user_code_reviewer",
		DisplayName:   "代码审查助手",
		Role:          "reviewer",
		SystemPrompt:  "严格审查并发安全与内存泄漏",
		Persona:       "客观专业",
		InitialMemory: "# 专职记忆\n- 检查死锁\n- 检查Goroutine泄露\n",
		AllowedAgents: []string{"local_agent_probe"},
	}
	valRes3, err := h.BF.ValidateBlueprint(ctx, validBP)
	if err != nil || !valRes3.Valid {
		t.Fatalf("valid blueprint failed validation: %+v", valRes3)
	}

	propID, err := h.BF.ProposeCreateBot(ctx, validBP, "plugin_workspace_init")
	if err != nil || propID == "" {
		t.Fatalf("ProposeCreateBot failed: %v", err)
	}

	// 4. Coordinated Supervisor Restart (F17)
	err = h.BF.RestartInstance(ctx, "user_code_reviewer")
	if err != nil {
		t.Fatalf("RestartInstance failed: %v", err)
	}
	restarted := h.MockSuper.GetRestartedBots()
	if len(restarted) != 1 || restarted[0] != "user_code_reviewer" {
		t.Fatalf("supervisor did not restart bot: %v", restarted)
	}

	// 5. Retrieve Active Blueprint Snapshot (F18)
	bpSnapshot, err := h.BF.GetBlueprint(ctx, "user_code_reviewer")
	if err != nil || bpSnapshot.Role != "reviewer" {
		t.Fatalf("GetBlueprint failed or mismatched: %+v", bpSnapshot)
	}
}

// -------------------------------------------------------------
// Scenario 4: Circular & Depth Dependency Attack Defense
// Features: F20, F21, F22, F23
// -------------------------------------------------------------
func TestScenario4_CircularAndDepthDependencyAttackDefense(t *testing.T) {
	// 1. Direct 2-node cycle attack: P_A <-> P_B (F20, F22)
	pluginsCycle := map[string]*plugin.Manifest{
		"plugin_a": {
			PluginID: "plugin_a",
			Dependencies: []plugin.DependencySpec{
				{Interface: "iface.b", Optional: false},
			},
		},
		"plugin_b": {
			PluginID: "plugin_b",
			Dependencies: []plugin.DependencySpec{
				{Interface: "iface.a", Optional: false},
			},
		},
	}
	providerIndex := map[string]string{
		"iface.b": "plugin_b",
		"iface.a": "plugin_a",
	}
	dagCycle, err := plugin.BuildDAG(pluginsCycle, providerIndex)
	if err != nil {
		t.Fatalf("BuildDAG failed: %v", err)
	}
	hasCycle, cyclePath := plugin.DetectCycle(dagCycle)
	if !hasCycle || len(cyclePath) == 0 {
		t.Fatalf("expected cycle detected, got hasCycle=%v, path=%v", hasCycle, cyclePath)
	}
	_, err = plugin.TopologicalSort(dagCycle)
	if err == nil {
		t.Fatalf("TopologicalSort expected circular error, got nil")
	}

	// 2. Max Depth > 2 Violation Attack: P4 -> P3 -> P2 -> P1 (F20, F23)
	pluginsDeep := map[string]*plugin.Manifest{
		"p1": {PluginID: "p1"},
		"p2": {PluginID: "p2", Dependencies: []plugin.DependencySpec{{Interface: "i1"}}},
		"p3": {PluginID: "p3", Dependencies: []plugin.DependencySpec{{Interface: "i2"}}},
		"p4": {PluginID: "p4", Dependencies: []plugin.DependencySpec{{Interface: "i3"}}},
	}
	deepProviders := map[string]string{"i1": "p1", "i2": "p2", "i3": "p3"}
	dagDeep, err := plugin.BuildDAG(pluginsDeep, deepProviders)
	if err != nil {
		t.Fatalf("BuildDAG deep failed: %v", err)
	}
	err = plugin.CheckMaxDepth(dagDeep, 2)
	if err == nil || !strings.Contains(err.Error(), "MaxDepth <= 2") {
		t.Fatalf("expected ErrExceedsMaxDependencyDepth, got: %v", err)
	}

	// 3. Diamond DAG (Valid Complex Topology with MaxDepth <= 2):
	// p_base1 (depth 1), p_base2 (depth 1), p_app depends on both (depth 2 <= 2)
	pluginsDiamond2 := map[string]*plugin.Manifest{
		"p_base1": {PluginID: "p_base1"},
		"p_base2": {PluginID: "p_base2"},
		"p_app": {
			PluginID: "p_app",
			Dependencies: []plugin.DependencySpec{
				{Interface: "i_base1"},
				{Interface: "i_base2"},
			},
		},
	}
	diamond2Providers := map[string]string{
		"i_base1": "p_base1",
		"i_base2": "p_base2",
	}
	dagDiamond2, err := plugin.BuildDAG(pluginsDiamond2, diamond2Providers)
	if err != nil {
		t.Fatalf("BuildDAG diamond2 failed: %v", err)
	}
	if hasCycle, _ := plugin.DetectCycle(dagDiamond2); hasCycle {
		t.Fatalf("diamond2 DAG should not have cycle")
	}
	if err := plugin.CheckMaxDepth(dagDiamond2, 2); err != nil {
		t.Fatalf("depth 2 diamond should pass MaxDepth <= 2: %v", err)
	}
	order2, err := plugin.TopologicalSort(dagDiamond2)
	if err != nil || len(order2) != 3 || order2[2] != "p_app" {
		t.Fatalf("unexpected diamond sort order: %v", order2)
	}

	// 4. 3-Level Diamond (Depth 3 tested against maxDepth = 3)
	pluginsDiamond := map[string]*plugin.Manifest{
		"p_core": {PluginID: "p_core"},
		"p_l1":   {PluginID: "p_l1", Dependencies: []plugin.DependencySpec{{Interface: "i_core"}}},
		"p_l2":   {PluginID: "p_l2", Dependencies: []plugin.DependencySpec{{Interface: "i_core"}}},
		"p_app":  {PluginID: "p_app", Dependencies: []plugin.DependencySpec{{Interface: "i_l1"}, {Interface: "i_l2"}}},
	}
	diamondProviders := map[string]string{
		"i_core": "p_core",
		"i_l1":   "p_l1",
		"i_l2":   "p_l2",
	}
	dagDiamond, err := plugin.BuildDAG(pluginsDiamond, diamondProviders)
	if err != nil {
		t.Fatalf("BuildDAG diamond failed: %v", err)
	}
	if hasCycle, _ := plugin.DetectCycle(dagDiamond); hasCycle {
		t.Fatalf("diamond DAG should not have cycle")
	}
	if err := plugin.CheckMaxDepth(dagDiamond, 3); err != nil {
		t.Fatalf("unexpected depth error: %v", err)
	}
	order, err := plugin.TopologicalSort(dagDiamond)
	if err != nil || order[0] != "p_core" || order[len(order)-1] != "p_app" {
		t.Fatalf("unexpected diamond sort order: %v", order)
	}
}

// -------------------------------------------------------------
// Scenario 5: Reverse Cascading Teardown & Uninstall
// Features: F20, F21, F24, F25
// -------------------------------------------------------------
func TestScenario5_ReverseCascadingTeardownAndUninstall(t *testing.T) {
	plugins := map[string]*plugin.Manifest{
		"plugin_groupchat": {PluginID: "plugin_groupchat", Version: "1.0.0"},
		"plugin_pm": {
			PluginID: "plugin_pm",
			Version:  "1.0.0",
			Dependencies: []plugin.DependencySpec{
				{Interface: "nexus.groupchat.v1", VersionRange: "^1.0.0"},
			},
		},
		"plugin_collab": {
			PluginID: "plugin_collab",
			Version:  "1.0.0",
			Dependencies: []plugin.DependencySpec{
				{Interface: "nexus.groupchat.v1", VersionRange: "^1.0.0"},
			},
		},
		"plugin_notes": {PluginID: "plugin_notes", Version: "1.0.0"},
	}
	providerIndex := map[string]string{"nexus.groupchat.v1": "plugin_groupchat"}
	dag, err := plugin.BuildDAG(plugins, providerIndex)
	if err != nil {
		t.Fatalf("BuildDAG failed: %v", err)
	}

	// 1. Non-cascade uninstall rejection (F24)
	seq, err := plugin.CheckUninstallReverseDependencies(dag, "plugin_groupchat", false)
	if err == nil {
		t.Fatalf("expected uninstall rejection for plugin with active dependents, got sequence: %v", seq)
	}

	// 2. Cascade reverse topological teardown (F24)
	cascadeSeq, err := plugin.CheckUninstallReverseDependencies(dag, "plugin_groupchat", true)
	if err != nil {
		t.Fatalf("CheckUninstallReverseDependencies cascade failed: %v", err)
	}
	// Expected: dependents first (pm, collab), base last (groupchat), notes excluded
	if len(cascadeSeq) != 3 {
		t.Fatalf("expected 3 plugins in teardown sequence, got: %v", cascadeSeq)
	}
	if cascadeSeq[len(cascadeSeq)-1] != "plugin_groupchat" {
		t.Fatalf("base plugin must be uninstalled LAST, got: %v", cascadeSeq)
	}

	// 3. SemVer Range Matcher Compatibility (F25)
	match, err := plugin.MatchSemVer("1.1.0", "^1.0.0")
	if err != nil || !match {
		t.Fatalf("expected 1.1.0 to match ^1.0.0")
	}
	matchMajor, err := plugin.MatchSemVer("2.0.0", "^1.0.0")
	if err != nil || matchMajor {
		t.Fatalf("expected 2.0.0 to NOT match ^1.0.0")
	}
}

// -------------------------------------------------------------
// Tier 3: Pairwise Cross-Feature Interactions
// -------------------------------------------------------------
func TestTier3_PairwiseCrossFeatureInteractions(t *testing.T) {
	ctx := context.Background()
	h := newScenarioHarness(t)

	// P1: GroupChat × Governance (F01, F02, F06 × F19, F20, F21, F24)
	t.Run("P1_GroupChat_x_Governance", func(t *testing.T) {
		plugins := map[string]*plugin.Manifest{
			"plugin_groupchat": {PluginID: "plugin_groupchat", Version: "1.0.0"},
			"plugin_pm": {
				PluginID: "plugin_pm",
				Version:  "1.0.0",
				Dependencies: []plugin.DependencySpec{
					{Interface: "nexus.groupchat.v1", VersionRange: "^1.0.0"},
				},
			},
		}
		dag, err := plugin.BuildDAG(plugins, map[string]string{"nexus.groupchat.v1": "plugin_groupchat"})
		if err != nil {
			t.Fatalf("P1 BuildDAG failed: %v", err)
		}
		order, err := plugin.TopologicalSort(dag)
		if err != nil || order[0] != "plugin_groupchat" {
			t.Fatalf("P1 TopologicalSort failed: %v", err)
		}

		room, err := h.GC.CreateRoom(ctx, groupchat.CreateRoomRequest{
			RoomID:  "room_p1",
			Topic:   "P1 Room",
			OwnerID: "plugin_pm",
			Members: []string{"user_bot", "user_1"},
		})
		if err != nil || room.RoomID != "room_p1" {
			t.Fatalf("P1 room creation failed: %v", err)
		}

		// Reverse uninstall without cascade must fail
		_, err = plugin.CheckUninstallReverseDependencies(dag, "plugin_groupchat", false)
		if err == nil {
			t.Fatalf("expected reverse uninstall blocked without cascade")
		}
	})

	// P2: AgentMesh × Governance (F07, F09, F13 × F19, F20, F21, F25)
	t.Run("P2_AgentMesh_x_Governance", func(t *testing.T) {
		plugins := map[string]*plugin.Manifest{
			"plugin_agent_mesh": {PluginID: "plugin_agent_mesh", Version: "1.2.0"},
			"plugin_cicd": {
				PluginID: "plugin_cicd",
				Version:  "1.0.0",
				Dependencies: []plugin.DependencySpec{
					{Interface: "nexus.agent_mesh.v1", VersionRange: ">=1.0.0 <2.0.0"},
				},
			},
		}
		dag, err := plugin.BuildDAG(plugins, map[string]string{"nexus.agent_mesh.v1": "plugin_agent_mesh"})
		if err != nil {
			t.Fatalf("P2 BuildDAG failed: %v", err)
		}
		order, err := plugin.TopologicalSort(dag)
		if err != nil || order[0] != "plugin_agent_mesh" {
			t.Fatalf("P2 sort failed: %v", err)
		}

		// Register probe & execute with distillation
		err = h.AMReg.RegisterProbe(&agentmesh.ProbeInfo{
			AgentID:      "probe_p2",
			Capabilities: []string{"nexus.agent_mesh.v1"},
			Status:       agentmesh.ProbeStatusOnline,
		})
		if err != nil {
			t.Fatalf("RegisterProbe failed: %v", err)
		}

		err = h.AM.RegisterCLIDriver(ctx, "probe_p2", agentmesh.CLIDriverManifest{
			DriverName: "echo",
			BinPath:    "/bin/echo",
		})
		if err != nil {
			t.Fatalf("RegisterCLIDriver failed: %v", err)
		}
		res, err := h.AM.Execute(ctx, agentmesh.ExecutionRequest{
			AgentID:     "probe_p2",
			ProtocolID:  "nexus.agent_mesh.v1",
			Payload:     []byte(`{"cmd": "echo build finished"}`),
			DistillRule: agentmesh.DistillRuleCleanNoise,
		})
		if err != nil || res.ExitCode != 0 {
			t.Fatalf("Execute failed: %v", err)
		}

		// Incompatible major bump (2.0.0) is blocked by range
		match, err := plugin.MatchSemVer("2.0.0", ">=1.0.0 <2.0.0")
		if err != nil || match {
			t.Fatalf("major version bump 2.0.0 should not match range >=1.0.0 <2.0.0")
		}
	})

	// P3: BotFactory × Governance (F14, F15, F17, F18 × F19, F20, F21, F23)
	t.Run("P3_BotFactory_x_Governance", func(t *testing.T) {
		plugins := map[string]*plugin.Manifest{
			"plugin_bot_factory": {PluginID: "plugin_bot_factory", Version: "1.0.0"},
			"plugin_workspace": {
				PluginID: "plugin_workspace",
				Dependencies: []plugin.DependencySpec{
					{Interface: "nexus.bot_factory.v1", VersionRange: "^1.0.0"},
				},
			},
		}
		dag, err := plugin.BuildDAG(plugins, map[string]string{"nexus.bot_factory.v1": "plugin_bot_factory"})
		if err != nil {
			t.Fatalf("P3 BuildDAG failed: %v", err)
		}
		if err := plugin.CheckMaxDepth(dag, 2); err != nil {
			t.Fatalf("P3 depth check failed: %v", err)
		}

		// Invalid blueprint fails fast
		badBP := botfactory.BotBlueprint{BotID: "create_bot"} // reserved
		val, _ := h.BF.ValidateBlueprint(ctx, badBP)
		if val.Valid {
			t.Fatalf("expected validation failure for reserved bot ID")
		}

		// Valid blueprint succeeds
		goodBP := botfactory.BotBlueprint{
			BotID:        "user_p3_bot",
			DisplayName:  "P3 Bot",
			Role:         "reviewer",
			SystemPrompt: "test",
		}
		propID, err := h.BF.ProposeCreateBot(ctx, goodBP, "plugin_workspace")
		if err != nil || propID == "" {
			t.Fatalf("ProposeCreateBot failed: %v", err)
		}
	})

	// P4: GroupChat × AgentMesh (F01, F02, F06 × F09, F13)
	t.Run("P4_GroupChat_x_AgentMesh", func(t *testing.T) {
		// User mentions @bot -> Bot calls AM -> Output distilled -> Bot replies -> Anti-cascade stops loop
		raw := "Thinking...\n[====================] 100%\nSTATUS: OK\nFinished in 0.5s\n"
		distilled, _ := agentmesh.DistillOutput(raw, "", 0, 500, agentmesh.DistillRuleCleanNoise)
		if strings.Contains(distilled, "Thinking") {
			t.Fatalf("noise not cleaned in P4: %s", distilled)
		}

		res, err := h.GC.PostMessage(ctx, groupchat.PostMessageRequest{
			RoomID:   "room_p1",
			SenderID: "user_ops_bot",
			Role:     groupchat.RoleBot,
			Content:  "@user_dev_bot Distilled Result: " + distilled,
		})
		if err != nil {
			t.Fatalf("bot post message failed: %v", err)
		}
		if len(res.TriggeredBots) != 0 {
			t.Fatalf("anti-cascade failed in P4: %v", res.TriggeredBots)
		}
	})

	// P5: BotFactory × GroupChat (F14, F15, F17, F18 × F01, F02, F06)
	t.Run("P5_BotFactory_x_GroupChat", func(t *testing.T) {
		// Derived bot joins room and is triggered by user
		bp := botfactory.BotBlueprint{
			BotID:         "user_qa_lead",
			DisplayName:   "QA Lead",
			Role:          "reviewer",
			SystemPrompt:  "Lead QA",
			InitialMemory: "# QA\n- rule 1\n",
		}
		_, err := h.BF.ProposeCreateBot(ctx, bp, "test")
		if err != nil {
			t.Fatalf("ProposeCreateBot failed: %v", err)
		}
		msgRes, err := h.GC.PostMessage(ctx, groupchat.PostMessageRequest{
			RoomID:   "room_p1",
			SenderID: "user_human",
			Role:     groupchat.RoleUser,
			Content:  "@user_qa_lead please review",
		})
		if err != nil || len(msgRes.TriggeredBots) != 1 || msgRes.TriggeredBots[0] != "user_qa_lead" {
			t.Fatalf("failed to wake up derived bot: %v", msgRes)
		}
	})

	// P6: BotFactory × AgentMesh (F14, F15 × F07, F09, F12)
	t.Run("P6_BotFactory_x_AgentMesh", func(t *testing.T) {
		bp := botfactory.BotBlueprint{
			BotID:         "user_probe_bot",
			DisplayName:   "Probe Worker",
			Role:          "ops",
			SystemPrompt:  "Run probe commands",
			AllowedAgents: []string{"probe_p2"},
		}
		assembled := botfactory.AssembleBotConfig(bp)

		// Check if probe is allowed by bot blueprint
		isAllowed := func(agentID string) bool {
			for _, a := range assembled.AllowedAgents {
				if a == agentID {
					return true
				}
			}
			return false
		}

		if !isAllowed("probe_p2") {
			t.Fatalf("expected probe_p2 to be allowed")
		}
		if isAllowed("unauthorized_probe_99") {
			t.Fatalf("unauthorized probe should be rejected by Bot ACL")
		}

		// Register probe & execute allowed probe
		err := h.AMReg.RegisterProbe(&agentmesh.ProbeInfo{
			AgentID:      "probe_p2",
			Capabilities: []string{"nexus.cli.ops"},
			Status:       agentmesh.ProbeStatusOnline,
		})
		if err != nil {
			t.Fatalf("RegisterProbe failed: %v", err)
		}

		res, err := h.AM.Execute(ctx, agentmesh.ExecutionRequest{
			AgentID:     "probe_p2",
			ProtocolID:  "nexus.cli.ops",
			Payload:     []byte(`{"cmd": "echo probe_p2_ok"}`),
			DistillRule: agentmesh.DistillRuleCleanNoise,
		})
		if err != nil || res.ExitCode != 0 {
			t.Fatalf("Execute on allowed probe failed: %v", err)
		}
	})

	// P7: 4-Way Orchestration (Gov -> BF -> AM -> GC -> Anti-cascade -> Cascade Teardown)
	t.Run("P7_FourWayOrchestration", func(t *testing.T) {
		// 1. Governance initializes DAG
		plugins := map[string]*plugin.Manifest{
			"plugin_core": {PluginID: "plugin_core", Version: "1.0.0"},
			"plugin_orchestrator": {
				PluginID: "plugin_orchestrator",
				Version:  "1.0.0",
				Dependencies: []plugin.DependencySpec{
					{Interface: "nexus.core.v1", VersionRange: "^1.0.0"},
				},
			},
		}
		dag, err := plugin.BuildDAG(plugins, map[string]string{"nexus.core.v1": "plugin_core"})
		if err != nil {
			t.Fatalf("P7 BuildDAG failed: %v", err)
		}
		order, err := plugin.TopologicalSort(dag)
		if err != nil || order[0] != "plugin_core" {
			t.Fatalf("P7 sort failed: %v", err)
		}

		// 2. BotFactory creates bot bound to probe
		err = h.AMReg.RegisterProbe(&agentmesh.ProbeInfo{
			AgentID:      "probe_p7",
			Capabilities: []string{"nexus.cli.ops"},
			Status:       agentmesh.ProbeStatusOnline,
		})
		if err != nil {
			t.Fatalf("P7 RegisterProbe failed: %v", err)
		}

		err = h.AM.RegisterCLIDriver(ctx, "probe_p7", agentmesh.CLIDriverManifest{
			DriverName: "echo",
			BinPath:    "/bin/echo",
		})
		if err != nil {
			t.Fatalf("P7 RegisterCLIDriver failed: %v", err)
		}

		bp := botfactory.BotBlueprint{
			BotID:         "user_p7_bot",
			DisplayName:   "P7 Bot",
			Role:          "ops",
			SystemPrompt:  "P7 Orchestration",
			AllowedAgents: []string{"probe_p7"},
		}
		_, err = h.BF.ProposeCreateBot(ctx, bp, "plugin_orchestrator")
		if err != nil {
			t.Fatalf("P7 ProposeCreateBot failed: %v", err)
		}

		// 3. Bot enters room
		room, err := h.GC.CreateRoom(ctx, groupchat.CreateRoomRequest{
			RoomID:  "room_p7",
			Topic:   "P7 Room",
			OwnerID: "plugin_orchestrator",
			Members: []string{"user_p7_bot", "user_human"},
		})
		if err != nil {
			t.Fatalf("P7 CreateRoom failed: %v", err)
		}

		// 4. User triggers bot
		postRes, err := h.GC.PostMessage(ctx, groupchat.PostMessageRequest{
			RoomID:   room.RoomID,
			SenderID: "user_human",
			Role:     groupchat.RoleUser,
			Content:  "@user_p7_bot execute task",
		})
		if err != nil || len(postRes.TriggeredBots) != 1 {
			t.Fatalf("P7 user post failed: %v", err)
		}

		// 5. Bot runs probe and distills output
		amRes, err := h.AM.Execute(ctx, agentmesh.ExecutionRequest{
			AgentID:     "probe_p7",
			ProtocolID:  "nexus.cli.ops",
			Payload:     []byte(`{"cmd": "echo task completed"}`),
			DistillRule: agentmesh.DistillRuleCleanNoise,
		})
		if err != nil || amRes.ExitCode != 0 {
			t.Fatalf("P7 AM Execute failed: %v", err)
		}

		// 6. Bot replies to room (Anti-cascade verified)
		botPost, err := h.GC.PostMessage(ctx, groupchat.PostMessageRequest{
			RoomID:   room.RoomID,
			SenderID: "user_p7_bot",
			Role:     groupchat.RoleBot,
			Content:  "@user_human Task done: " + amRes.DistilledOutput,
		})
		if err != nil || len(botPost.TriggeredBots) != 0 {
			t.Fatalf("P7 anti-cascade failed: %v", botPost.TriggeredBots)
		}

		// 7. Governance cascade teardown
		teardownSeq, err := plugin.CheckUninstallReverseDependencies(dag, "plugin_core", true)
		if err != nil || len(teardownSeq) != 2 || teardownSeq[1] != "plugin_core" {
			t.Fatalf("P7 cascade teardown failed: %v", teardownSeq)
		}
	})
}
