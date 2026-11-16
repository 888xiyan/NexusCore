package agentmesh

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// mockExecutor 用于确定性模拟探针命令输出
type mockExecutor struct {
	mu       sync.Mutex
	lastCmd  string
	lastArgs []string
	result   *CommandExecutionResult
	err      error
}

func (m *mockExecutor) Execute(ctx context.Context, cmdName string, args []string, timeout time.Duration) (*CommandExecutionResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastCmd = cmdName
	m.lastArgs = args
	return m.result, m.err
}

func TestProbeRegistryAndHeartbeat(t *testing.T) {
	reg := NewMeshRegistry()

	// 1. Register probes
	err := reg.RegisterProbe(&ProbeInfo{
		AgentID:      "agent_probe_1",
		DeviceName:   "Workstation-Linux",
		Capabilities: []string{"nexus.cli.claude", "nexus.echo.v1"},
		Status:       ProbeStatusOnline,
	})
	if err != nil {
		t.Fatalf("RegisterProbe failed: %v", err)
	}

	err = reg.RegisterProbe(&ProbeInfo{
		AgentID:      "agent_probe_2",
		DeviceName:   "MacBook-M3",
		Capabilities: []string{"nexus.cli.codex"},
		Status:       ProbeStatusOnline,
	})
	if err != nil {
		t.Fatalf("RegisterProbe failed: %v", err)
	}

	// Invalid agent_id
	if err := reg.RegisterProbe(&ProbeInfo{AgentID: ""}); err != ErrInvalidAgentID {
		t.Fatalf("expected ErrInvalidAgentID, got %v", err)
	}

	// 2. GetProbe
	p1, err := reg.GetProbe("agent_probe_1")
	if err != nil || p1.AgentID != "agent_probe_1" {
		t.Fatalf("GetProbe failed: %v", err)
	}

	_, err = reg.GetProbe("unknown_probe")
	if err != ErrPeerUnavailable {
		t.Fatalf("expected ErrPeerUnavailable, got %v", err)
	}

	// 3. ListProbes with filters
	all := reg.ListProbes(ProbeFilter{})
	if len(all) != 2 {
		t.Fatalf("expected 2 probes, got %d", len(all))
	}

	claudeProbes := reg.ListProbes(ProbeFilter{Capability: "nexus.cli.claude"})
	if len(claudeProbes) != 1 || claudeProbes[0].AgentID != "agent_probe_1" {
		t.Fatalf("expected agent_probe_1 for claude capability, got %v", claudeProbes)
	}

	statusProbes := reg.ListProbes(ProbeFilter{Status: ProbeStatusOnline})
	if len(statusProbes) != 2 {
		t.Fatalf("expected 2 online probes, got %d", len(statusProbes))
	}

	agentFilterProbes := reg.ListProbes(ProbeFilter{AgentID: "agent_probe_2"})
	if len(agentFilterProbes) != 1 {
		t.Fatalf("expected 1 probe filtered by AgentID, got %d", len(agentFilterProbes))
	}

	// 4. Heartbeat expiration (30s threshold)
	p1.LastHeartbeat = time.Now().Unix() - 35 // 35s ago
	reg.mu.Lock()
	reg.probes["agent_probe_1"].LastHeartbeat = time.Now().Unix() - 35
	reg.mu.Unlock()

	// Dynamic detection marks it offline
	p1Expired, err := reg.GetProbe("agent_probe_1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p1Expired.Status != ProbeStatusOffline {
		t.Fatalf("expected status ProbeStatusOffline after 35s without heartbeat, got %s", p1Expired.Status)
	}

	// CheckHeartbeats batch scan
	offline := reg.CheckHeartbeats(time.Now())
	if len(offline) != 1 || offline[0] != "agent_probe_1" {
		t.Fatalf("expected offline list [agent_probe_1], got %v", offline)
	}

	// RecordHeartbeat revives the probe
	err = reg.RecordHeartbeat("agent_probe_1")
	if err != nil {
		t.Fatalf("RecordHeartbeat failed: %v", err)
	}
	p1Revived, _ := reg.GetProbe("agent_probe_1")
	if p1Revived.Status != ProbeStatusOnline {
		t.Fatalf("expected status ProbeStatusOnline after heartbeat, got %s", p1Revived.Status)
	}

	// RecordHeartbeat for unknown probe
	if err := reg.RecordHeartbeat("unknown"); err != ErrPeerUnavailable {
		t.Fatalf("expected ErrPeerUnavailable for unknown probe, got %v", err)
	}
}

func TestDistillationEngineRules(t *testing.T) {
	// 1. Stage 1 ANSI and Progress Bar cleaning
	dirtyANSI := "\x1b[32m[SUCCESS]\x1b[0m \x1b[1mCompilation finished\x1b[0m"
	cleaned := CleanStageOneNoise(dirtyANSI)
	if strings.Contains(cleaned, "\x1b") {
		t.Fatalf("ANSI not cleaned: %s", cleaned)
	}
	if !strings.Contains(cleaned, "[SUCCESS] Compilation finished") {
		t.Fatalf("Content mangled: %s", cleaned)
	}

	dirtyProgress := "loading dependencies...\n[=====>    ] 50% downloading\nresolving artifacts\nBuild complete."
	cleanedProgress := CleanStageOneNoise(dirtyProgress)
	if strings.Contains(cleanedProgress, "50%") || strings.Contains(cleanedProgress, "loading") {
		t.Fatalf("progress bars not stripped: %s", cleanedProgress)
	}
	if !strings.Contains(cleanedProgress, "Build complete.") {
		t.Fatalf("actual content lost: %s", cleanedProgress)
	}

	// DEC Private Mode cursor sequences (\x1b[?25l, \x1b[?25h)
	dirtyDECCursor := "\x1b[?25l\x1b[32mCompiling...\x1b[0m\x1b[?25h\nBuild finished."
	cleanedDEC := CleanStageOneNoise(dirtyDECCursor)
	if strings.Contains(cleanedDEC, "\x1b") || strings.Contains(cleanedDEC, "?25") {
		t.Fatalf("DEC cursor sequence not stripped: %q", cleanedDEC)
	}
	if !strings.Contains(cleanedDEC, "Compiling...") || !strings.Contains(cleanedDEC, "Build finished.") {
		t.Fatalf("DEC content mangled: %q", cleanedDEC)
	}

	// 2. Stage 2: compiler_error_only with 1000 lines of noise and 2 error lines (Edge Case E12)
	var noiseLog strings.Builder
	for i := 0; i < 500; i++ {
		noiseLog.WriteString(fmt.Sprintf("Step %d: compiling module pkg_%d\n", i, i))
	}
	noiseLog.WriteString("pkg/router/handler.go:42:15: error: undefined: OrderManager\n")
	noiseLog.WriteString("pkg/router/handler.go:45:10: syntax error: unexpected semicolon\n")
	for i := 500; i < 1000; i++ {
		noiseLog.WriteString(fmt.Sprintf("Step %d: compiling module pkg_%d\n", i, i))
	}

	distilledErr, rawRef := DistillOutput(noiseLog.String(), "", 1, 1500, DistillRuleCompilerErrorOnly)
	if !strings.HasPrefix(rawRef, "raw_stream_") {
		t.Fatalf("expected rawRef prefix raw_stream_, got %s", rawRef)
	}
	if strings.Contains(distilledErr, "Step 100:") {
		t.Fatalf("compiler_error_only failed to strip non-error steps")
	}
	if !strings.Contains(distilledErr, "undefined: OrderManager") || !strings.Contains(distilledErr, "syntax error") {
		t.Fatalf("compiler_error_only missing actual error lines: %s", distilledErr)
	}

	// compiler_error_only with exitCode 0
	distilledSuccess, _ := DistillOutput("built 42 packages successfully", "", 0, 800, DistillRuleCompilerErrorOnly)
	if distilledSuccess != "编译成功，无错误输出。" {
		t.Fatalf("expected '编译成功，无错误输出。', got %q", distilledSuccess)
	}

	// compiler_error_only with exitCode 1 and stderr fallback (no compiler keywords)
	distilledErrFallback, _ := DistillOutput("", "command failed mysteriously", 1, 300, DistillRuleCompilerErrorOnly)
	if !strings.Contains(distilledErrFallback, "command failed mysteriously") {
		t.Fatalf("expected stderr fallback, got %q", distilledErrFallback)
	}

	// compiler_error_only with exitCode 1 and stdout fallback (no compiler keywords)
	distilledOutFallback, _ := DistillOutput("generic failure occurred", "", 1, 300, DistillRuleCompilerErrorOnly)
	if !strings.Contains(distilledOutFallback, "generic failure occurred") {
		t.Fatalf("expected stdout fallback, got %q", distilledOutFallback)
	}

	// compiler_error_only with exitCode 1 and empty output
	distilledEmptyFallback, _ := DistillOutput("", "", 1, 300, DistillRuleCompilerErrorOnly)
	if !strings.Contains(distilledEmptyFallback, "未提取到明确编译错误行") {
		t.Fatalf("expected empty fallback message, got %q", distilledEmptyFallback)
	}

	// 3. Stage 2: json_extract
	noisyJSONOutput := "Connecting to database...\nFound record:\n{\"name\": \"NexusCore\", \"version\": \"0.1.0\", \"active\": true}\nDone."
	extractedJSON, _ := DistillOutput(noisyJSONOutput, "", 0, 200, DistillRuleJSONExtract)
	if !strings.Contains(extractedJSON, `"name": "NexusCore"`) || !strings.Contains(extractedJSON, `"version": "0.1.0"`) {
		t.Fatalf("JSON extraction failed: %s", extractedJSON)
	}

	// Array JSON
	noisyArrOutput := "Result list: [1, 2, 3, 4] end."
	extractedArr, _ := DistillOutput(noisyArrOutput, "", 0, 100, DistillRuleJSONExtract)
	if !strings.Contains(extractedArr, "1") || !strings.Contains(extractedArr, "4") {
		t.Fatalf("JSON array extraction failed: %s", extractedArr)
	}

	// Invalid JSON with error
	invalidJSONErr, _ := DistillOutput("", "internal server error 500", 1, 100, DistillRuleJSONExtract)
	if !strings.Contains(invalidJSONErr, "未提取到 JSON") {
		t.Fatalf("expected failed JSON extraction notice, got %s", invalidJSONErr)
	}

	// 4. Stage 2: clean_noise / default
	noisyDefault := "thinking about plan...\n[###       ] 25%\nAll tasks finished."
	distilledDefault, _ := DistillOutput(noisyDefault, "", 0, 50, DistillRuleCleanNoise)
	if !strings.Contains(distilledDefault, "**执行状态**: 成功") || !strings.Contains(distilledDefault, "All tasks finished.") {
		t.Fatalf("default distillation output unexpected: %s", distilledDefault)
	}

	// Default with empty output
	distilledEmpty, _ := DistillOutput("", "", 0, 10, DistillRuleCleanNoise)
	if !strings.Contains(distilledEmpty, "无输出") {
		t.Fatalf("expected empty output message, got %s", distilledEmpty)
	}

	// Default with failure and stderr
	distilledFailErr, _ := DistillOutput("", "fatal crash in runtime", 1, 40, DistillRuleCleanNoise)
	if !strings.Contains(distilledFailErr, "**执行状态**: 失败") || !strings.Contains(distilledFailErr, "fatal crash in runtime") {
		t.Fatalf("expected failure error summary, got %s", distilledFailErr)
	}

	// Default with failure and stdout
	distilledFailOut, _ := DistillOutput("panic in main", "", 2, 40, DistillRuleCleanNoise)
	if !strings.Contains(distilledFailOut, "panic in main") {
		t.Fatalf("expected failure stdout summary, got %s", distilledFailOut)
	}

	// Default with failure and empty details
	distilledFailNone, _ := DistillOutput("", "", 1, 40, DistillRuleCleanNoise)
	if !strings.Contains(distilledFailNone, "未返回具体错误信息") {
		t.Fatalf("expected default error message, got %s", distilledFailNone)
	}
}

func TestCommandWhitelistAndTimeoutClamping(t *testing.T) {
	// Whitelist validation
	if !IsCommandAllowed("claude") || !IsCommandAllowed("codex") || !IsCommandAllowed("echo") {
		t.Fatalf("standard whitelist command rejected")
	}
	if !IsCommandAllowed("/usr/bin/bash") || !IsCommandAllowed("./mock-agent") {
		t.Fatalf("path-prefixed whitelist command rejected")
	}
	if IsCommandAllowed("rm") || IsCommandAllowed("curl") || IsCommandAllowed("nmap") {
		t.Fatalf("dangerous unwhitelisted command allowed")
	}

	// Timeout clamping
	if ClampTimeout(0) != DefaultTimeout {
		t.Fatalf("expected DefaultTimeout for 0, got %v", ClampTimeout(0))
	}
	if ClampTimeout(-5) != DefaultTimeout {
		t.Fatalf("expected DefaultTimeout for -5, got %v", ClampTimeout(-5))
	}
	if ClampTimeout(15) != 15*time.Second {
		t.Fatalf("expected 15s, got %v", ClampTimeout(15))
	}
	if ClampTimeout(300) != MaxTimeout {
		t.Fatalf("expected clamped MaxTimeout (120s), got %v", ClampTimeout(300))
	}
}

func TestParseCommandPayload(t *testing.T) {
	// Empty payload
	_, _, err := ParseCommandPayload(nil)
	if err == nil {
		t.Fatalf("expected error on nil payload")
	}

	_, _, err = ParseCommandPayload([]byte("   "))
	if err == nil {
		t.Fatalf("expected error on empty payload")
	}

	// JSON payload with cmd
	cmd, args, err := ParseCommandPayload([]byte(`{"cmd": "echo hello world"}`))
	if err != nil || cmd != "echo" || len(args) != 2 {
		t.Fatalf("ParseCommandPayload with cmd failed: %s %v %v", cmd, args, err)
	}

	// JSON payload with command and args
	cmd, args, err = ParseCommandPayload([]byte(`{"command": "claude", "args": ["--version", "--debug"]}`))
	if err != nil || cmd != "claude" || len(args) != 2 {
		t.Fatalf("ParseCommandPayload with command and args failed: %s %v %v", cmd, args, err)
	}

	// Plain text payload
	cmd, args, err = ParseCommandPayload([]byte("codex review src/main.go"))
	if err != nil || cmd != "codex" || len(args) != 2 {
		t.Fatalf("ParseCommandPayload plain text failed: %s %v %v", cmd, args, err)
	}
}

func TestExecuteAndExecuteStream(t *testing.T) {
	ctx := context.Background()

	mock := &mockExecutor{
		result: &CommandExecutionResult{
			Stdout:     "{\"result\": \"success\", \"code\": 200}",
			Stderr:     "",
			ExitCode:   0,
			DurationMs: 120,
		},
	}

	reg := NewMeshRegistry()
	_ = reg.RegisterProbe(&ProbeInfo{
		AgentID:      "probe_worker_1",
		Capabilities: []string{"nexus.cli.claude"},
		Status:       ProbeStatusOnline,
	})

	svc := NewService(WithRegistry(reg), WithExecutor(mock))

	// Direct service calls for ListProbes & GetProbe
	probes, err := svc.ListProbes(ctx, ProbeFilter{})
	if err != nil || len(probes) != 1 {
		t.Fatalf("svc.ListProbes failed: %v %d", err, len(probes))
	}

	p, err := svc.GetProbe(ctx, "probe_worker_1")
	if err != nil || p.AgentID != "probe_worker_1" {
		t.Fatalf("svc.GetProbe failed: %v", err)
	}

	// Test svc.Registry()
	if svc.(*agentMeshService).Registry() != reg {
		t.Fatalf("svc.Registry() mismatch")
	}

	// 1. Successful execution with JSON distillation
	res, err := svc.Execute(ctx, ExecutionRequest{
		AgentID:     "probe_worker_1",
		ProtocolID:  "nexus.cli.claude",
		Payload:     []byte(`{"cmd": "claude --version"}`),
		TimeoutSec:  30,
		DistillRule: DistillRuleJSONExtract,
	})
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("expected exit code 0, got %d", res.ExitCode)
	}
	if !strings.Contains(res.DistilledOutput, `"result": "success"`) {
		t.Fatalf("distilled output unexpected: %s", res.DistilledOutput)
	}

	// Verify raw stream ref can be retrieved
	rawLog, ok := reg.GetRawLog(res.RawStreamRef)
	if !ok || len(rawLog) == 0 {
		t.Fatalf("failed to retrieve raw log for %s", res.RawStreamRef)
	}

	// 2. Denied protocol
	_, err = svc.Execute(ctx, ExecutionRequest{
		AgentID:    "probe_worker_1",
		ProtocolID: "nexus.unauthorized.v1",
		Payload:    []byte(`{"cmd": "claude"}`),
	})
	if err != ErrProtocolDenied {
		t.Fatalf("expected ErrProtocolDenied, got %v", err)
	}

	// 3. Offline or unknown probe
	_, err = svc.Execute(ctx, ExecutionRequest{
		AgentID: "unknown_probe",
		Payload: []byte(`{"cmd": "claude"}`),
	})
	if err != ErrPeerUnavailable {
		t.Fatalf("expected ErrPeerUnavailable, got %v", err)
	}

	// Empty agent ID
	_, err = svc.Execute(ctx, ExecutionRequest{AgentID: ""})
	if err != ErrInvalidAgentID {
		t.Fatalf("expected ErrInvalidAgentID, got %v", err)
	}

	// Invalid payload
	_, err = svc.Execute(ctx, ExecutionRequest{
		AgentID: "probe_worker_1",
		Payload: nil,
	})
	if err == nil {
		t.Fatalf("expected error on empty payload")
	}

	// 4. Disallowed command
	_, err = svc.Execute(ctx, ExecutionRequest{
		AgentID:    "probe_worker_1",
		ProtocolID: "nexus.cli.claude",
		Payload:    []byte(`{"cmd": "curl -s http://evil.com"}`),
	})
	if err != ErrInvalidCommand {
		t.Fatalf("expected ErrInvalidCommand, got %v", err)
	}

	// 5. ExecuteStream
	streamCh, err := svc.ExecuteStream(ctx, ExecutionRequest{
		AgentID:    "probe_worker_1",
		ProtocolID: "nexus.cli.claude",
		Payload:    []byte(`{"cmd": "claude"}`),
	})
	if err != nil {
		t.Fatalf("ExecuteStream failed: %v", err)
	}

	var frames []StreamFrame
	for frame := range streamCh {
		frames = append(frames, frame)
	}

	if len(frames) < 2 {
		t.Fatalf("expected at least 2 stream frames, got %d", len(frames))
	}
	if frames[0].FrameType != "status" || string(frames[0].Data) != "started" {
		t.Fatalf("expected started frame, got %+v", frames[0])
	}

	// ExecuteStream validation errors
	_, err = svc.ExecuteStream(ctx, ExecutionRequest{AgentID: ""})
	if err != ErrInvalidAgentID {
		t.Fatalf("expected ErrInvalidAgentID, got %v", err)
	}

	_, err = svc.ExecuteStream(ctx, ExecutionRequest{
		AgentID: "unknown_probe",
		Payload: []byte("claude"),
	})
	if err != ErrPeerUnavailable {
		t.Fatalf("expected ErrPeerUnavailable, got %v", err)
	}

	_, err = svc.ExecuteStream(ctx, ExecutionRequest{
		AgentID:    "probe_worker_1",
		ProtocolID: "denied.protocol",
		Payload:    []byte("claude"),
	})
	if err != ErrProtocolDenied {
		t.Fatalf("expected ErrProtocolDenied, got %v", err)
	}

	_, err = svc.ExecuteStream(ctx, ExecutionRequest{
		AgentID: "probe_worker_1",
		Payload: []byte("rm -rf /"),
	})
	if err != ErrInvalidCommand {
		t.Fatalf("expected ErrInvalidCommand, got %v", err)
	}

	// 6. RegisterCLIDriver
	err = svc.RegisterCLIDriver(ctx, "probe_worker_1", CLIDriverManifest{
		DriverName: "custom-claude-driver",
		BinPath:    "/usr/local/bin/claude",
		MaxTimeout: 60,
	})
	if err != nil {
		t.Fatalf("RegisterCLIDriver failed: %v", err)
	}

	d, ok := reg.GetCLIDriver("probe_worker_1")
	if !ok || d.DriverName != "custom-claude-driver" {
		t.Fatalf("GetCLIDriver failed: %+v", d)
	}

	// RegisterCLIDriver invalid inputs
	if err := svc.RegisterCLIDriver(ctx, "", CLIDriverManifest{}); err != ErrInvalidAgentID {
		t.Fatalf("expected ErrInvalidAgentID on empty agentID, got %v", err)
	}
	if err := svc.RegisterCLIDriver(ctx, "probe_worker_1", CLIDriverManifest{}); err == nil {
		t.Fatalf("expected error on empty manifest")
	}
}

func TestRealOSCommandExecution(t *testing.T) {
	exec := &DefaultOSCommandExecutor{}
	ctx := context.Background()

	// Execute echo (in whitelist)
	res, err := exec.Execute(ctx, "echo", []string{"hello", "nexuscore"}, 5*time.Second)
	if err != nil {
		t.Fatalf("real echo execution failed: %v", err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("expected exit code 0, got %d", res.ExitCode)
	}
	if !strings.Contains(res.Stdout, "hello nexuscore") {
		t.Fatalf("unexpected stdout: %q", res.Stdout)
	}

	// Execute non-zero exit command (sh -c "exit 3")
	resExit, _ := exec.Execute(ctx, "sh", []string{"-c", "exit 3"}, 5*time.Second)
	if resExit.ExitCode != 3 {
		t.Fatalf("expected exit code 3, got %d", resExit.ExitCode)
	}

	// Execute invalid binary command
	resBad, errBad := exec.Execute(ctx, "non_existent_binary_xyz", nil, 2*time.Second)
	if errBad == nil || resBad.ExitCode == 0 {
		t.Fatalf("expected error on non-existent binary")
	}

	// Output truncation limited writer test
	writer := &limitedWriter{w: new(strings.Builder), remaining: 10}
	n, err := writer.Write([]byte("0123456789extra"))
	if err != nil || n != 15 || !writer.truncated {
		t.Fatalf("limitedWriter truncation failed: n=%d, truncated=%v", n, writer.truncated)
	}
	// Write after remaining <= 0
	n2, err2 := writer.Write([]byte("more"))
	if err2 != nil || n2 != 4 || !writer.truncated {
		t.Fatalf("limitedWriter secondary write failed: %v", err2)
	}
}

func TestListProbes_StatusAndAgentIDFilter(t *testing.T) {
	ctx := context.Background()
	reg := NewMeshRegistry()
	_ = reg.RegisterProbe(&ProbeInfo{
		AgentID:      "probe_filter_1",
		Capabilities: []string{"cap_a"},
		Status:       ProbeStatusOnline,
	})
	_ = reg.RegisterProbe(&ProbeInfo{
		AgentID:      "probe_filter_2",
		Capabilities: []string{"cap_b"},
		Status:       ProbeStatusOffline,
	})

	svc := NewService(WithRegistry(reg))

	// Filter by status offline
	offlineProbes, err := svc.ListProbes(ctx, ProbeFilter{Status: ProbeStatusOffline})
	if err != nil || len(offlineProbes) != 1 || offlineProbes[0].AgentID != "probe_filter_2" {
		t.Fatalf("expected 1 offline probe (probe_filter_2), got: %v", offlineProbes)
	}

	// Filter by agentID
	idProbes, err := svc.ListProbes(ctx, ProbeFilter{AgentID: "probe_filter_1"})
	if err != nil || len(idProbes) != 1 || idProbes[0].AgentID != "probe_filter_1" {
		t.Fatalf("expected probe_filter_1, got: %v", idProbes)
	}
}

func TestListProbes_EmptyRegistryAndUnmatchedFilters(t *testing.T) {
	ctx := context.Background()
	emptyReg := NewMeshRegistry()
	svc := NewService(WithRegistry(emptyReg))

	all, err := svc.ListProbes(ctx, ProbeFilter{})
	if err != nil {
		t.Fatalf("ListProbes on empty registry failed: %v", err)
	}
	if len(all) != 0 {
		t.Fatalf("expected 0 probes on empty registry, got: %d", len(all))
	}

	// Unmatched capability filter
	unmatched, err := svc.ListProbes(ctx, ProbeFilter{Capability: "non_existent_cap"})
	if err != nil || len(unmatched) != 0 {
		t.Fatalf("expected 0 unmatched probes, got: %v", unmatched)
	}
}

func TestListProbes_DynamicOfflineDetection(t *testing.T) {
	ctx := context.Background()
	reg := NewMeshRegistry()
	_ = reg.RegisterProbe(&ProbeInfo{
		AgentID:      "probe_dyn_offline",
		Capabilities: []string{"cap_test"},
		Status:       ProbeStatusOnline,
	})

	// Manually age last heartbeat to 35 seconds ago
	reg.mu.Lock()
	reg.probes["probe_dyn_offline"].LastHeartbeat = time.Now().Unix() - 35
	reg.mu.Unlock()

	svc := NewService(WithRegistry(reg))
	probes, err := svc.ListProbes(ctx, ProbeFilter{AgentID: "probe_dyn_offline"})
	if err != nil || len(probes) != 1 {
		t.Fatalf("ListProbes failed: %v", err)
	}
	if probes[0].Status != ProbeStatusOffline {
		t.Fatalf("expected dynamic offline status, got: %s", probes[0].Status)
	}
}

func TestGetProbe_RevivedAndSafeCopy(t *testing.T) {
	ctx := context.Background()
	reg := NewMeshRegistry()
	_ = reg.RegisterProbe(&ProbeInfo{
		AgentID:      "probe_revive",
		Capabilities: []string{"cap_revive"},
		Status:       ProbeStatusOnline,
	})

	// Age heartbeat to expire it
	reg.mu.Lock()
	reg.probes["probe_revive"].LastHeartbeat = time.Now().Unix() - 35
	reg.mu.Unlock()

	svc := NewService(WithRegistry(reg))

	// GetProbe on expired probe returns ErrPeerUnavailable
	_, err := svc.GetProbe(ctx, "probe_revive")
	if err != ErrPeerUnavailable {
		t.Fatalf("expected ErrPeerUnavailable on expired probe, got: %v", err)
	}

	// Record heartbeat revives the probe
	_ = reg.RecordHeartbeat("probe_revive")

	p, err := svc.GetProbe(ctx, "probe_revive")
	if err != nil || p.Status != ProbeStatusOnline {
		t.Fatalf("expected online revived probe, got: %v, %+v", err, p)
	}

	// Mutate returned struct
	p.Capabilities = append(p.Capabilities, "mutated_capability")

	// Verify internal registry is not mutated
	p2, _ := svc.GetProbe(ctx, "probe_revive")
	if len(p2.Capabilities) != 1 || p2.Capabilities[0] != "cap_revive" {
		t.Fatalf("internal registry was mutated via returned probe copy: %v", p2.Capabilities)
	}
}

func TestGetProbe_EmptyAgentID(t *testing.T) {
	ctx := context.Background()
	svc := NewService()

	_, err := svc.GetProbe(ctx, "   ")
	if err != ErrPeerUnavailable {
		t.Fatalf("expected ErrPeerUnavailable on empty agentID, got: %v", err)
	}
}

func TestGetProbe_ExactHeartbeatBoundary(t *testing.T) {
	ctx := context.Background()
	reg := NewMeshRegistry()

	_ = reg.RegisterProbe(&ProbeInfo{
		AgentID: "probe_29s",
		Status:  ProbeStatusOnline,
	})
	_ = reg.RegisterProbe(&ProbeInfo{
		AgentID: "probe_31s",
		Status:  ProbeStatusOnline,
	})

	now := time.Now().Unix()
	reg.mu.Lock()
	reg.probes["probe_29s"].LastHeartbeat = now - 29
	reg.probes["probe_31s"].LastHeartbeat = now - 31
	reg.mu.Unlock()

	svc := NewService(WithRegistry(reg))

	// 29s probe is still online (< 30s threshold)
	p29, err := svc.GetProbe(ctx, "probe_29s")
	if err != nil || p29.Status != ProbeStatusOnline {
		t.Fatalf("expected probe_29s to be online: %v", err)
	}

	// 31s probe is offline (>= 30s threshold)
	_, err = svc.GetProbe(ctx, "probe_31s")
	if err != ErrPeerUnavailable {
		t.Fatalf("expected ErrPeerUnavailable for probe_31s, got: %v", err)
	}
}

func TestExecute_TimeoutErrorHandling(t *testing.T) {
	ctx := context.Background()

	mock := &mockExecutor{
		result: &CommandExecutionResult{
			Stdout:     "partial build output",
			Stderr:     "building...\n[ERROR] 执行超时硬截断",
			ExitCode:   124,
			DurationMs: 30005,
		},
		err: ErrExecutionTimeout,
	}

	reg := NewMeshRegistry()
	_ = reg.RegisterProbe(&ProbeInfo{
		AgentID:      "probe_timeout",
		Capabilities: []string{"nexus.cli.codex"},
		Status:       ProbeStatusOnline,
	})

	svc := NewService(WithRegistry(reg), WithExecutor(mock))

	res, err := svc.Execute(ctx, ExecutionRequest{
		AgentID:     "probe_timeout",
		ProtocolID:  "nexus.cli.codex",
		Payload:     []byte(`{"cmd": "codex generate"}`),
		TimeoutSec:  30,
		DistillRule: DistillRuleCleanNoise,
	})
	if err != nil {
		t.Fatalf("Execute should not return error on controlled execution timeout: %v", err)
	}
	if res.ExitCode != 124 {
		t.Fatalf("expected ExitCode 124, got %d", res.ExitCode)
	}
	if !strings.Contains(res.DistilledOutput, "执行超时硬截断") {
		t.Fatalf("distilled output missing timeout notice: %s", res.DistilledOutput)
	}
}

func TestExecute_CommandPathStripping(t *testing.T) {
	ctx := context.Background()

	mock := &mockExecutor{
		result: &CommandExecutionResult{
			Stdout:   "path stripped ok",
			ExitCode: 0,
		},
	}

	reg := NewMeshRegistry()
	_ = reg.RegisterProbe(&ProbeInfo{
		AgentID: "probe_strip",
		Status:  ProbeStatusOnline,
	})

	svc := NewService(WithRegistry(reg), WithExecutor(mock))

	res, err := svc.Execute(ctx, ExecutionRequest{
		AgentID: "probe_strip",
		Payload: []byte(`{"cmd": "/bin/echo testing path"}`),
	})
	if err != nil {
		t.Fatalf("Execute with /bin/echo failed: %v", err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("expected exit code 0, got %d", res.ExitCode)
	}
}

func TestExecuteStream_ErrorScenarios(t *testing.T) {
	ctx := context.Background()
	reg := NewMeshRegistry()
	_ = reg.RegisterProbe(&ProbeInfo{
		AgentID:      "probe_stream_err",
		Capabilities: []string{"nexus.cli.claude"},
		Status:       ProbeStatusOnline,
	})

	svc := NewService(WithRegistry(reg))

	// 1. Offline probe
	reg.mu.Lock()
	reg.probes["probe_stream_err"].Status = ProbeStatusOffline
	reg.mu.Unlock()

	_, err := svc.ExecuteStream(ctx, ExecutionRequest{
		AgentID: "probe_stream_err",
		Payload: []byte("claude"),
	})
	if err != ErrPeerUnavailable {
		t.Fatalf("expected ErrPeerUnavailable for offline probe, got: %v", err)
	}

	// Revert to online
	reg.mu.Lock()
	reg.probes["probe_stream_err"].Status = ProbeStatusOnline
	reg.mu.Unlock()

	// 2. Unauthorized protocol
	_, err = svc.ExecuteStream(ctx, ExecutionRequest{
		AgentID:    "probe_stream_err",
		ProtocolID: "unauthorized.protocol",
		Payload:    []byte("claude"),
	})
	if err != ErrProtocolDenied {
		t.Fatalf("expected ErrProtocolDenied, got: %v", err)
	}

	// 3. Disallowed command
	_, err = svc.ExecuteStream(ctx, ExecutionRequest{
		AgentID:    "probe_stream_err",
		ProtocolID: "nexus.cli.claude",
		Payload:    []byte("nmap 127.0.0.1"),
	})
	if err != ErrInvalidCommand {
		t.Fatalf("expected ErrInvalidCommand, got: %v", err)
	}

	// 4. Empty agent ID
	_, err = svc.ExecuteStream(ctx, ExecutionRequest{
		AgentID: "",
		Payload: []byte("claude"),
	})
	if err != ErrInvalidAgentID {
		t.Fatalf("expected ErrInvalidAgentID, got: %v", err)
	}
}

func TestExecuteStream_NonZeroExitStatusFrame(t *testing.T) {
	ctx := context.Background()

	mock := &mockExecutor{
		result: &CommandExecutionResult{
			Stdout:   "compilation failure details",
			Stderr:   "exit status 2",
			ExitCode: 2,
		},
	}

	reg := NewMeshRegistry()
	_ = reg.RegisterProbe(&ProbeInfo{
		AgentID: "probe_stream_exit2",
		Status:  ProbeStatusOnline,
	})

	svc := NewService(WithRegistry(reg), WithExecutor(mock))

	ch, err := svc.ExecuteStream(ctx, ExecutionRequest{
		AgentID: "probe_stream_exit2",
		Payload: []byte("echo failure"),
	})
	if err != nil {
		t.Fatalf("ExecuteStream failed: %v", err)
	}

	var frames []StreamFrame
	for f := range ch {
		frames = append(frames, f)
	}

	hasExit2 := false
	for _, f := range frames {
		if f.FrameType == "status" && string(f.Data) == "exit_2" {
			hasExit2 = true
			break
		}
	}
	if !hasExit2 {
		t.Fatalf("expected status frame with 'exit_2', got frames: %+v", frames)
	}
}

func TestExecuteStream_ContextCancellation(t *testing.T) {
	cancelCtx, cancel := context.WithCancel(context.Background())

	mock := &mockExecutor{
		result: &CommandExecutionResult{
			Stdout:   "streaming line 1",
			ExitCode: 0,
		},
	}

	reg := NewMeshRegistry()
	_ = reg.RegisterProbe(&ProbeInfo{
		AgentID: "probe_cancel",
		Status:  ProbeStatusOnline,
	})

	svc := NewService(WithRegistry(reg), WithExecutor(mock))

	ch, err := svc.ExecuteStream(cancelCtx, ExecutionRequest{
		AgentID: "probe_cancel",
		Payload: []byte("echo stream"),
	})
	if err != nil {
		t.Fatalf("ExecuteStream failed: %v", err)
	}

	// Read initial frame
	<-ch
	// Cancel context
	cancel()

	// Drain channel
	for range ch {
	}
}

func TestRegisterCLIDriver_Validation(t *testing.T) {
	ctx := context.Background()
	svc := NewService()

	// Empty agent ID
	err := svc.RegisterCLIDriver(ctx, "", CLIDriverManifest{
		DriverName: "test-driver",
		BinPath:    "/usr/bin/echo",
	})
	if err != ErrInvalidAgentID {
		t.Fatalf("expected ErrInvalidAgentID on empty agentID, got: %v", err)
	}

	// Empty DriverName
	err = svc.RegisterCLIDriver(ctx, "probe_driver_val", CLIDriverManifest{
		DriverName: "",
		BinPath:    "/usr/bin/echo",
	})
	if err == nil {
		t.Fatalf("expected error on empty DriverName")
	}

	// Empty BinPath
	err = svc.RegisterCLIDriver(ctx, "probe_driver_val", CLIDriverManifest{
		DriverName: "test-driver",
		BinPath:    "",
	})
	if err == nil {
		t.Fatalf("expected error on empty BinPath")
	}
}

func TestRegisterCLIDriver_OverwriteAndUpdate(t *testing.T) {
	ctx := context.Background()
	reg := NewMeshRegistry()
	svc := NewService(WithRegistry(reg))

	manifest1 := CLIDriverManifest{
		DriverName: "driver-v1",
		BinPath:    "/usr/bin/echo",
		MaxTimeout: 30,
	}
	err := svc.RegisterCLIDriver(ctx, "probe_driver_update", manifest1)
	if err != nil {
		t.Fatalf("initial RegisterCLIDriver failed: %v", err)
	}

	d1, ok := reg.GetCLIDriver("probe_driver_update")
	if !ok || d1.BinPath != "/usr/bin/echo" {
		t.Fatalf("driver 1 registration failed: %+v", d1)
	}

	// Overwrite with updated BinPath
	manifest2 := CLIDriverManifest{
		DriverName: "driver-v2",
		BinPath:    "/usr/local/bin/echo",
		MaxTimeout: 60,
	}
	err = svc.RegisterCLIDriver(ctx, "probe_driver_update", manifest2)
	if err != nil {
		t.Fatalf("update RegisterCLIDriver failed: %v", err)
	}

	d2, ok := reg.GetCLIDriver("probe_driver_update")
	if !ok || d2.BinPath != "/usr/local/bin/echo" || d2.DriverName != "driver-v2" {
		t.Fatalf("driver update failed: %+v", d2)
	}
}

func TestHeartbeat_ExactThresholdEvaluation(t *testing.T) {
	reg := NewMeshRegistry()
	now := time.Now()

	_ = reg.RegisterProbe(&ProbeInfo{AgentID: "probe_28s", Status: ProbeStatusOnline})
	_ = reg.RegisterProbe(&ProbeInfo{AgentID: "probe_29s", Status: ProbeStatusOnline})
	_ = reg.RegisterProbe(&ProbeInfo{AgentID: "probe_31s", Status: ProbeStatusOnline})

	reg.mu.Lock()
	reg.probes["probe_28s"].LastHeartbeat = now.Unix() - 28
	reg.probes["probe_29s"].LastHeartbeat = now.Unix() - 29
	reg.probes["probe_31s"].LastHeartbeat = now.Unix() - 31
	reg.mu.Unlock()

	offline := reg.CheckHeartbeats(now)
	if len(offline) != 1 || offline[0] != "probe_31s" {
		t.Fatalf("expected only probe_31s to be flagged offline, got: %v", offline)
	}
}

func TestHeartbeat_ConcurrentHeartbeats(t *testing.T) {
	reg := NewMeshRegistry()
	_ = reg.RegisterProbe(&ProbeInfo{
		AgentID: "probe_concurrent_hb",
		Status:  ProbeStatusOnline,
	})

	var wg sync.WaitGroup
	const totalRoutines = 20

	for i := 0; i < totalRoutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = reg.RecordHeartbeat("probe_concurrent_hb")
			_ = reg.CheckHeartbeats(time.Now())
		}()
	}
	wg.Wait()

	p, err := reg.GetProbe("probe_concurrent_hb")
	if err != nil || p.Status != ProbeStatusOnline {
		t.Fatalf("probe should remain online: %v, %+v", err, p)
	}
}

func TestDistillation_CompilerErrorFallback(t *testing.T) {
	// Rule compiler_error_only with exit code != 0, but no compiler keywords
	rawStderr := "An unhandled exception occurred in runtime without keywords"
	distilled, _ := DistillOutput("", rawStderr, 1, 500, DistillRuleCompilerErrorOnly)
	if !strings.Contains(distilled, "An unhandled exception occurred") {
		t.Fatalf("expected stderr fallback for compiler error rule, got: %s", distilled)
	}
}

func TestDistillation_MalformedJSONFallback(t *testing.T) {
	// Broken JSON with failure exit code and stderr
	brokenJSON := "Starting server...\n{\ninvalid json payload: \nFinished."
	distilledFail, _ := DistillOutput(brokenJSON, "fatal syntax error", 1, 300, DistillRuleJSONExtract)
	if !strings.Contains(distilledFail, "未提取到 JSON (执行失败)") {
		t.Fatalf("expected JSON failure fallback message, got: %s", distilledFail)
	}

	// Broken JSON with exit code 0 returns cleaned source
	distilledClean, _ := DistillOutput(brokenJSON, "", 0, 300, DistillRuleJSONExtract)
	if !strings.Contains(distilledClean, "invalid json payload") {
		t.Fatalf("expected cleaned source fallback, got: %s", distilledClean)
	}
}

func TestDistillation_DECPrivateModeCursorSequences(t *testing.T) {
	// Terminal spinners and progress indicators emit DEC Private Mode sequences:
	// \x1b[?25l (hide cursor) and \x1b[?25h (show cursor).
	// StripANSI and CleanStageOneNoise must strip them cleanly without leaking '?25l' or '?25h'.
	samples := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "hide cursor spinner",
			input:    "\x1b[?25l[1/3] Step one...\x1b[?25h",
			expected: "[1/3] Step one...",
		},
		{
			name:     "mixed ANSI and DEC cursor sequences",
			input:    "\x1b[?25l\x1b[36mRunning tests\x1b[0m\x1b[?25h\n\x1b[32mPASS\x1b[0m",
			expected: "Running tests\nPASS",
		},
		{
			name:     "alternate screen buffer DEC sequences",
			input:    "\x1b[?1049hHello Alternate Screen\x1b[?1049l",
			expected: "Hello Alternate Screen",
		},
	}

	for _, tc := range samples {
		t.Run(tc.name, func(t *testing.T) {
			stripped := StripANSI(tc.input)
			if strings.Contains(stripped, "\x1b") || strings.Contains(stripped, "?25") || strings.Contains(stripped, "?1049") {
				t.Fatalf("StripANSI leaked escape sequences: %q", stripped)
			}
			cleaned := CleanStageOneNoise(tc.input)
			if cleaned != tc.expected {
				t.Fatalf("CleanStageOneNoise expected %q, got %q", tc.expected, cleaned)
			}
		})
	}
}

func TestProtocolAccessControl_EmptyCapabilitiesDefaultDeny(t *testing.T) {
	ctx := context.Background()
	mock := &mockExecutor{
		result: &CommandExecutionResult{
			Stdout:   "ok",
			ExitCode: 0,
		},
	}

	reg := NewMeshRegistry()
	// Probe registered with completely empty capabilities (nil or empty slice)
	err := reg.RegisterProbe(&ProbeInfo{
		AgentID:      "probe_empty_caps",
		DeviceName:   "Bare-Metal",
		Capabilities: []string{},
		Status:       ProbeStatusOnline,
	})
	if err != nil {
		t.Fatalf("RegisterProbe failed: %v", err)
	}

	err = reg.RegisterProbe(&ProbeInfo{
		AgentID:      "probe_nil_caps",
		DeviceName:   "Bare-Metal-Nil",
		Capabilities: nil,
		Status:       ProbeStatusOnline,
	})
	if err != nil {
		t.Fatalf("RegisterProbe failed: %v", err)
	}

	svc := NewService(WithRegistry(reg), WithExecutor(mock))

	targetProbes := []string{"probe_empty_caps", "probe_nil_caps"}

	for _, probeID := range targetProbes {
		// 1. Non-empty ProtocolID requested on probe with empty capabilities MUST fail with ErrProtocolDenied
		_, err = svc.Execute(ctx, ExecutionRequest{
			AgentID:    probeID,
			ProtocolID: "nexus.cli.claude",
			Payload:    []byte(`{"cmd": "echo test"}`),
		})
		if err != ErrProtocolDenied {
			t.Fatalf("[%s] Execute: expected ErrProtocolDenied for non-empty ProtocolID, got %v", probeID, err)
		}

		// 2. Stream execution with non-empty ProtocolID MUST also fail with ErrProtocolDenied
		_, err = svc.ExecuteStream(ctx, ExecutionRequest{
			AgentID:    probeID,
			ProtocolID: "nexus.cli.claude",
			Payload:    []byte(`{"cmd": "echo test"}`),
		})
		if err != ErrProtocolDenied {
			t.Fatalf("[%s] ExecuteStream: expected ErrProtocolDenied for non-empty ProtocolID, got %v", probeID, err)
		}

		// 3. Empty ProtocolID allows unconstrained commands (no protocol restriction)
		res, err := svc.Execute(ctx, ExecutionRequest{
			AgentID:    probeID,
			ProtocolID: "",
			Payload:    []byte(`{"cmd": "echo test"}`),
		})
		if err != nil {
			t.Fatalf("[%s] Execute: expected success with empty ProtocolID, got %v", probeID, err)
		}
		if res.ExitCode != 0 {
			t.Fatalf("[%s] Execute: expected exit code 0, got %d", probeID, res.ExitCode)
		}
	}
}
