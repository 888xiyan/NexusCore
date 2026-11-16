package botfactory

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
)

func generateLines(n int) string {
	var sb strings.Builder
	for i := 1; i <= n; i++ {
		sb.WriteString(fmt.Sprintf("Line %d: memory note content\n", i))
	}
	return sb.String()
}

func TestValidateBlueprint(t *testing.T) {
	// 1. Valid Blueprint
	validBP := BotBlueprint{
		BotID:         "user_qa_bot",
		DisplayName:   "QA Assistant",
		Role:          "qa",
		SystemPrompt:  "You are a QA testing specialist.",
		Persona:       "Meticulous, structured, analytical.",
		InitialMemory: "# Initial QA Memory\n- Track bug repro steps\n",
		AllowedAgents: []string{"loopback_agent"},
	}

	val := ValidateBlueprint(validBP)
	if !val.Valid || len(val.Violations) != 0 {
		t.Fatalf("expected valid blueprint, got violations: %v", val.Violations)
	}

	// 2. Reserved bot names
	reserved := []string{"user_bot", "create_bot", "monitor_bot", "security_bot"}
	for _, r := range reserved {
		bp := validBP
		bp.BotID = r
		res := ValidateBlueprint(bp)
		if res.Valid {
			t.Fatalf("expected reserved name %q to fail validation", r)
		}
		if len(res.Violations) == 0 || !strings.Contains(res.Violations[0], "内核保留名") {
			t.Fatalf("expected reserved name violation for %q, got: %v", r, res.Violations)
		}
	}

	// 3. Invalid bot_id regex
	invalidIDs := []string{
		"",               // empty
		"bot",            // too short (< 5)
		"Bot_Capital",    // contains uppercase
		"bot-dash",       // contains hyphen
		"bot.dot",        // contains dot
		strings.Repeat("a", 65), // too long (> 64)
	}
	for _, id := range invalidIDs {
		bp := validBP
		bp.BotID = id
		res := ValidateBlueprint(bp)
		if res.Valid {
			t.Fatalf("expected invalid bot_id %q to fail validation", id)
		}
	}

	// 4. Missing required fields
	bpNoName := validBP
	bpNoName.DisplayName = "   "
	if res := ValidateBlueprint(bpNoName); res.Valid {
		t.Fatalf("expected empty display_name to fail")
	}

	bpNoPrompt := validBP
	bpNoPrompt.SystemPrompt = "   "
	if res := ValidateBlueprint(bpNoPrompt); res.Valid {
		t.Fatalf("expected empty system_prompt to fail")
	}

	// 5. Memory <= 200 lines constraint (Edge Cases E17 & E18)
	// E17: Exactly 200 lines
	bp200 := validBP
	bp200.InitialMemory = generateLines(200)
	res200 := ValidateBlueprint(bp200)
	if !res200.Valid || res200.MemoryLines != 200 {
		t.Fatalf("expected 200 lines memory to be valid, got valid=%v, lines=%d, violations=%v", res200.Valid, res200.MemoryLines, res200.Violations)
	}

	// E18: 201 lines (must fail)
	bp201 := validBP
	bp201.InitialMemory = generateLines(201)
	res201 := ValidateBlueprint(bp201)
	if res201.Valid {
		t.Fatalf("expected 201 lines memory to fail validation")
	}
	if res201.MemoryLines != 201 || len(res201.Violations) == 0 || !strings.Contains(res201.Violations[0], "200") {
		t.Fatalf("expected memory limit violation, got: %+v", res201)
	}

	// 6. ActivationMode validation
	bpInvalidMode := validBP
	bpInvalidMode.ActivationMode = "unknown_mode"
	if res := ValidateBlueprint(bpInvalidMode); res.Valid {
		t.Fatalf("expected invalid activation_mode to fail")
	}

	bpResident := validBP
	bpResident.ActivationMode = "resident"
	if res := ValidateBlueprint(bpResident); !res.Valid {
		t.Fatalf("expected resident activation_mode to pass: %v", res.Violations)
	}
}

func TestBuiltinTemplates(t *testing.T) {
	templates := BuiltinTemplates()
	if len(templates) != 3 {
		t.Fatalf("expected 3 builtin templates, got %d", len(templates))
	}

	expectedIDs := map[string]bool{"reviewer": true, "coder": true, "ops": true}
	for _, tmpl := range templates {
		if !expectedIDs[tmpl.ID] {
			t.Errorf("unexpected template ID: %s", tmpl.ID)
		}
		val := ValidateBlueprint(tmpl.Blueprint)
		if !val.Valid {
			t.Errorf("builtin template %s failed validation: %v", tmpl.ID, val.Violations)
		}
		if val.MemoryLines > 200 {
			t.Errorf("builtin template %s has %d memory lines (> 200)", tmpl.ID, val.MemoryLines)
		}
	}
}

func TestAssemblyAndSingleAPIKey(t *testing.T) {
	bp := BotBlueprint{
		BotID:         "user_test_bot",
		DisplayName:   "Test Bot",
		Role:          "tester",
		SystemPrompt:  "Test prompt",
		InitialMemory: "note",
		AllowedAgents: []string{"custom_agent_1"},
		BoundAgentCLI: "claude",
		Metadata: map[string]string{
			"env": "staging",
		},
	}

	assembled := AssembleBotConfig(bp)

	// Verify single global API key injected
	if assembled.APIKeyRef["$secret_ref"] != "global.llm.api_key" {
		t.Fatalf("expected APIKeyRef global.llm.api_key, got %+v", assembled.APIKeyRef)
	}

	// Verify default activation_mode
	if assembled.ActivationMode != "lazy" {
		t.Fatalf("expected default activation_mode 'lazy', got %s", assembled.ActivationMode)
	}

	// Verify default bound agent
	if assembled.BoundAgentID != "loopback_agent" {
		t.Fatalf("expected default bound agent loopback_agent, got %s", assembled.BoundAgentID)
	}

	// Verify allowed agents includes both custom and default bound agent
	if len(assembled.AllowedAgents) != 2 {
		t.Fatalf("expected 2 allowed agents, got %v", assembled.AllowedAgents)
	}
}

func TestProposalGenerationAndCanonicalChecksum(t *testing.T) {
	bp := BotBlueprint{
		BotID:        "user_prop_bot",
		DisplayName:  "Proposal Test Bot",
		SystemPrompt: "Test System Prompt",
	}

	// 1. Missing proposer
	_, err := CreateBotProposal(bp, "")
	if err != ErrEmptyProposer {
		t.Fatalf("expected ErrEmptyProposer, got %v", err)
	}

	// 2. Successful proposal creation
	prop, err := CreateBotProposal(bp, "user_bot")
	if err != nil {
		t.Fatalf("CreateBotProposal failed: %v", err)
	}
	if prop.ScopeType != "bot" {
		t.Errorf("expected ScopeType bot, got %s", prop.ScopeType)
	}
	if prop.ScopeID != "user_prop_bot" {
		t.Errorf("expected ScopeID user_prop_bot, got %s", prop.ScopeID)
	}
	if len(prop.Checksum) != 64 {
		t.Errorf("expected 64-char sha256 checksum, got %s", prop.Checksum)
	}

	// Canonical JSON stability
	data1 := map[string]any{"z": 1, "a": 2, "m": "test"}
	data2 := map[string]any{"a": 2, "m": "test", "z": 1}
	sum1, err1 := ComputeChecksum(data1)
	sum2, err2 := ComputeChecksum(data2)
	if err1 != nil || err2 != nil || sum1 != sum2 {
		t.Fatalf("canonical checksum non-deterministic: %s != %s", sum1, sum2)
	}
}

func TestServiceLifecycle(t *testing.T) {
	ctx := context.Background()
	restartCalled := false
	restartedBotID := ""

	svc := NewService(WithRestartCoordinator(func(ctx context.Context, botID string) error {
		restartCalled = true
		restartedBotID = botID
		return nil
	}))

	bp := BotBlueprint{
		BotID:         "user_lifecycle_bot",
		DisplayName:   "Lifecycle Bot",
		Role:          "reviewer",
		SystemPrompt:  "Review prompt",
		Persona:       "Strict",
		InitialMemory: "# Notes\n",
	}

	// 1. ValidateBlueprint
	val, err := svc.ValidateBlueprint(ctx, bp)
	if err != nil || !val.Valid {
		t.Fatalf("ValidateBlueprint failed: %v, %v", err, val)
	}

	// 2. ProposeCreateBot
	proposalID, err := svc.ProposeCreateBot(ctx, bp, "user_admin_bot")
	if err != nil {
		t.Fatalf("ProposeCreateBot failed: %v", err)
	}
	if !strings.HasPrefix(proposalID, "prop_bot_") {
		t.Fatalf("expected proposalID prefix prop_bot_, got %s", proposalID)
	}

	// Duplicate create fails
	_, err = svc.ProposeCreateBot(ctx, bp, "user_admin_bot")
	if err != ErrBotAlreadyExists {
		t.Fatalf("expected ErrBotAlreadyExists, got %v", err)
	}

	// Invalid blueprint create fails
	badBP := bp
	badBP.BotID = "invalid!id"
	_, err = svc.ProposeCreateBot(ctx, badBP, "user_admin_bot")
	if err == nil || !strings.Contains(err.Error(), "校验未通过") {
		t.Fatalf("expected blueprint invalid error, got %v", err)
	}

	// 3. GetBlueprint
	retrieved, err := svc.GetBlueprint(ctx, "user_lifecycle_bot")
	if err != nil || retrieved.DisplayName != "Lifecycle Bot" {
		t.Fatalf("GetBlueprint failed: %v", err)
	}

	_, err = svc.GetBlueprint(ctx, "unknown_bot")
	if err != ErrBotNotFound {
		t.Fatalf("expected ErrBotNotFound, got %v", err)
	}

	// 4. ProposeUpdateBot
	updates := bp
	updates.Persona = "Supportive and friendly"
	updatePropID, err := svc.ProposeUpdateBot(ctx, "user_lifecycle_bot", updates, "user_admin_bot")
	if err != nil {
		t.Fatalf("ProposeUpdateBot failed: %v", err)
	}
	if !strings.HasPrefix(updatePropID, "prop_bot_") {
		t.Fatalf("expected update proposal ID prefix prop_bot_, got %s", updatePropID)
	}

	// Verify updated blueprint
	updatedBP, _ := svc.GetBlueprint(ctx, "user_lifecycle_bot")
	if updatedBP.Persona != "Supportive and friendly" {
		t.Fatalf("expected updated persona, got %s", updatedBP.Persona)
	}

	// Update non-existent bot fails
	_, err = svc.ProposeUpdateBot(ctx, "non_existent", updates, "user_admin_bot")
	if err != ErrBotNotFound {
		t.Fatalf("expected ErrBotNotFound for update, got %v", err)
	}

	// Update with invalid memory fails
	invalidUpdate := updates
	invalidUpdate.InitialMemory = generateLines(205)
	_, err = svc.ProposeUpdateBot(ctx, "user_lifecycle_bot", invalidUpdate, "user_admin_bot")
	if err == nil {
		t.Fatalf("expected error on 205-line memory update")
	}

	// 5. RestartInstance
	err = svc.RestartInstance(ctx, "user_lifecycle_bot")
	if err != nil {
		t.Fatalf("RestartInstance failed: %v", err)
	}
	if !restartCalled || restartedBotID != "user_lifecycle_bot" {
		t.Fatalf("RestartCoordinator hook was not triggered properly")
	}

	err = svc.RestartInstance(ctx, "non_existent")
	if err != ErrBotNotFound {
		t.Fatalf("expected ErrBotNotFound on restart, got %v", err)
	}

	// Inspect helper functions
	prop, ok := svc.(*botFactoryService).GetProposal(proposalID)
	if !ok || prop.ScopeID != "user_lifecycle_bot" {
		t.Fatalf("GetProposal failed: %+v", prop)
	}

	bps := svc.(*botFactoryService).ListBlueprints()
	if len(bps) != 1 {
		t.Fatalf("expected 1 blueprint in list, got %d", len(bps))
	}
}

func TestConcurrentBotFactory(t *testing.T) {
	ctx := context.Background()
	svc := NewService()

	var wg sync.WaitGroup
	workers := 10

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			bp := BotBlueprint{
				BotID:        fmt.Sprintf("user_worker_%03d", workerID),
				DisplayName:  fmt.Sprintf("Worker Bot %d", workerID),
				Role:         "worker",
				SystemPrompt: "Do work",
			}
			propID, err := svc.ProposeCreateBot(ctx, bp, "user_admin")
			if err != nil {
				t.Errorf("worker %d failed to create bot: %v", workerID, err)
				return
			}
			if propID == "" {
				t.Errorf("worker %d got empty propID", workerID)
			}
			_, _ = svc.GetBlueprint(ctx, bp.BotID)
		}(i)
	}

	wg.Wait()

	bps := svc.(*botFactoryService).ListBlueprints()
	if len(bps) != workers {
		t.Fatalf("expected %d blueprints, got %d", workers, len(bps))
	}
}

func TestValidateBlueprint_MemoryLineBoundaries(t *testing.T) {
	baseBP := BotBlueprint{
		BotID:        "user_mem_bounds",
		DisplayName:  "Memory Bounds Bot",
		Role:         "reviewer",
		SystemPrompt: "Check memory bounds",
	}

	// 0 lines (empty)
	bp0 := baseBP
	bp0.InitialMemory = ""
	val0 := ValidateBlueprint(bp0)
	if !val0.Valid || val0.MemoryLines != 0 {
		t.Fatalf("expected 0 lines to be valid, got %+v", val0)
	}

	// 1 line
	bp1 := baseBP
	bp1.InitialMemory = "single line memory note\n"
	val1 := ValidateBlueprint(bp1)
	if !val1.Valid || val1.MemoryLines != 1 {
		t.Fatalf("expected 1 line to be valid, got %+v", val1)
	}

	// 199 lines
	bp199 := baseBP
	bp199.InitialMemory = generateLines(199)
	val199 := ValidateBlueprint(bp199)
	if !val199.Valid || val199.MemoryLines != 199 {
		t.Fatalf("expected 199 lines to be valid, got %+v", val199)
	}

	// 200 lines CRLF
	var sbCRLF strings.Builder
	for i := 1; i <= 200; i++ {
		sbCRLF.WriteString(fmt.Sprintf("Line %d: CRLF memory\r\n", i))
	}
	bp200CRLF := baseBP
	bp200CRLF.InitialMemory = sbCRLF.String()
	val200CRLF := ValidateBlueprint(bp200CRLF)
	if !val200CRLF.Valid || val200CRLF.MemoryLines != 200 {
		t.Fatalf("expected 200 lines CRLF to be valid, got %+v", val200CRLF)
	}

	// 201 lines CRLF
	sbCRLF.WriteString("Line 201: CRLF overflow\r\n")
	bp201CRLF := baseBP
	bp201CRLF.InitialMemory = sbCRLF.String()
	val201CRLF := ValidateBlueprint(bp201CRLF)
	if val201CRLF.Valid || val201CRLF.MemoryLines != 201 {
		t.Fatalf("expected 201 lines CRLF to fail validation, got %+v", val201CRLF)
	}
}

func TestValidateBlueprint_IDLengthBoundaries(t *testing.T) {
	baseBP := BotBlueprint{
		DisplayName:  "ID Test",
		Role:         "tester",
		SystemPrompt: "test",
	}

	// 4 chars: invalid
	bp4 := baseBP
	bp4.BotID = "bot4"
	if res := ValidateBlueprint(bp4); res.Valid {
		t.Fatalf("expected 4 chars bot_id to fail")
	}

	// 5 chars: valid boundary
	bp5 := baseBP
	bp5.BotID = "bot05"
	if res := ValidateBlueprint(bp5); !res.Valid {
		t.Fatalf("expected 5 chars bot_id to pass, got: %v", res.Violations)
	}

	// 64 chars: valid boundary
	bp64 := baseBP
	bp64.BotID = "b" + strings.Repeat("a", 63)
	if res := ValidateBlueprint(bp64); !res.Valid {
		t.Fatalf("expected 64 chars bot_id to pass, got: %v", res.Violations)
	}

	// 65 chars: invalid boundary
	bp65 := baseBP
	bp65.BotID = "b" + strings.Repeat("a", 64)
	if res := ValidateBlueprint(bp65); res.Valid {
		t.Fatalf("expected 65 chars bot_id to fail")
	}
}

func TestProposeCreateBot_SingleAPIKeyInjectionSpec(t *testing.T) {
	ctx := context.Background()
	svc := NewService()

	bp := BotBlueprint{
		BotID:        "user_api_key_bot",
		DisplayName:  "API Key Bot",
		Role:         "reviewer",
		SystemPrompt: "Prompt",
	}

	propID, err := svc.ProposeCreateBot(ctx, bp, "user_admin")
	if err != nil {
		t.Fatalf("ProposeCreateBot failed: %v", err)
	}

	prop, ok := svc.(*botFactoryService).GetProposal(propID)
	if !ok {
		t.Fatalf("GetProposal failed for %s", propID)
	}

	var assembled AssembledBotConfig
	if err := json.Unmarshal(prop.Spec, &assembled); err != nil {
		t.Fatalf("unmarshal prop.Spec failed: %v", err)
	}
	if assembled.APIKeyRef["$secret_ref"] != "global.llm.api_key" {
		t.Fatalf("expected global.llm.api_key, got %+v", assembled.APIKeyRef)
	}
}

func TestProposeCreateBot_DefaultLoopbackAgentBinding(t *testing.T) {
	ctx := context.Background()
	svc := NewService()

	bp := BotBlueprint{
		BotID:        "user_loopback_bot",
		DisplayName:  "Loopback Bot",
		Role:         "reviewer",
		SystemPrompt: "Prompt",
		BoundAgentID: "", // empty -> should default to loopback_agent
	}

	propID, err := svc.ProposeCreateBot(ctx, bp, "user_admin")
	if err != nil {
		t.Fatalf("ProposeCreateBot failed: %v", err)
	}

	prop, ok := svc.(*botFactoryService).GetProposal(propID)
	if !ok {
		t.Fatalf("GetProposal failed")
	}

	var assembled AssembledBotConfig
	if err := json.Unmarshal(prop.Spec, &assembled); err != nil {
		t.Fatalf("unmarshal prop.Spec failed: %v", err)
	}

	if assembled.BoundAgentID != DefaultBoundAgentID {
		t.Fatalf("expected BoundAgentID %s, got %s", DefaultBoundAgentID, assembled.BoundAgentID)
	}
	foundLoopback := false
	for _, a := range assembled.AllowedAgents {
		if a == DefaultBoundAgentID {
			foundLoopback = true
			break
		}
	}
	if !foundLoopback {
		t.Fatalf("expected AllowedAgents to contain %s, got %v", DefaultBoundAgentID, assembled.AllowedAgents)
	}
}

func TestProposeUpdateBot_EmptyBotIDAndProposer(t *testing.T) {
	ctx := context.Background()
	svc := NewService()

	updates := BotBlueprint{
		DisplayName:  "Updated",
		Role:         "reviewer",
		SystemPrompt: "Prompt",
	}

	// Empty botID
	_, err := svc.ProposeUpdateBot(ctx, "", updates, "user_admin")
	if err != ErrBotNotFound {
		t.Fatalf("expected ErrBotNotFound for empty botID, got: %v", err)
	}

	// Empty proposer
	bp := BotBlueprint{
		BotID:        "user_prop_test",
		DisplayName:  "Prop Bot",
		Role:         "reviewer",
		SystemPrompt: "Prompt",
	}
	_, err = svc.ProposeCreateBot(ctx, bp, "user_admin")
	if err != nil {
		t.Fatalf("ProposeCreateBot failed: %v", err)
	}

	_, err = svc.ProposeUpdateBot(ctx, "user_prop_test", updates, "   ")
	if err != ErrEmptyProposer {
		t.Fatalf("expected ErrEmptyProposer, got: %v", err)
	}
}

func TestProposeUpdateBot_PartialUpdatePreservation(t *testing.T) {
	ctx := context.Background()
	svc := NewService()

	initialBP := BotBlueprint{
		BotID:         "user_partial_bot",
		DisplayName:   "Original Name",
		Role:          "original_role",
		SystemPrompt:  "Original System Prompt",
		Persona:       "Original Persona",
		InitialMemory: "# Original Memory\n",
	}

	_, err := svc.ProposeCreateBot(ctx, initialBP, "user_admin")
	if err != nil {
		t.Fatalf("ProposeCreateBot failed: %v", err)
	}

	// Update only Persona; leave DisplayName, Role, SystemPrompt empty
	partialUpdate := BotBlueprint{
		Persona: "Newly Updated Persona",
	}

	_, err = svc.ProposeUpdateBot(ctx, "user_partial_bot", partialUpdate, "user_admin")
	if err != nil {
		t.Fatalf("ProposeUpdateBot partial update failed: %v", err)
	}

	updated, err := svc.GetBlueprint(ctx, "user_partial_bot")
	if err != nil {
		t.Fatalf("GetBlueprint failed: %v", err)
	}
	if updated.DisplayName != "Original Name" {
		t.Fatalf("DisplayName not preserved: %s", updated.DisplayName)
	}
	if updated.Role != "original_role" {
		t.Fatalf("Role not preserved: %s", updated.Role)
	}
	if updated.SystemPrompt != "Original System Prompt" {
		t.Fatalf("SystemPrompt not preserved: %s", updated.SystemPrompt)
	}
	if updated.Persona != "Newly Updated Persona" {
		t.Fatalf("Persona was not updated: %s", updated.Persona)
	}
}

func TestProposeUpdateBot_MemoryThresholdTransition(t *testing.T) {
	ctx := context.Background()
	svc := NewService()

	bp := BotBlueprint{
		BotID:         "user_mem_trans",
		DisplayName:   "Mem Trans Bot",
		Role:          "reviewer",
		SystemPrompt:  "Prompt",
		InitialMemory: generateLines(100),
	}

	_, err := svc.ProposeCreateBot(ctx, bp, "user_admin")
	if err != nil {
		t.Fatalf("ProposeCreateBot failed: %v", err)
	}

	// Transition to 200 lines: pass
	up200 := bp
	up200.InitialMemory = generateLines(200)
	_, err = svc.ProposeUpdateBot(ctx, "user_mem_trans", up200, "user_admin")
	if err != nil {
		t.Fatalf("update to 200 lines failed: %v", err)
	}

	// Transition to 201 lines: fail
	up201 := bp
	up201.InitialMemory = generateLines(201)
	_, err = svc.ProposeUpdateBot(ctx, "user_mem_trans", up201, "user_admin")
	if err == nil {
		t.Fatalf("expected error on transition to 201 lines")
	}
}

func TestRestartInstance_EmptyIDAndNilHook(t *testing.T) {
	ctx := context.Background()
	// Service without restart hook
	svc := NewService()

	// Empty ID
	err := svc.RestartInstance(ctx, "   ")
	if err != ErrBotNotFound {
		t.Fatalf("expected ErrBotNotFound for empty ID, got: %v", err)
	}

	bp := BotBlueprint{
		BotID:        "user_nil_hook_bot",
		DisplayName:  "Nil Hook Bot",
		Role:         "reviewer",
		SystemPrompt: "Prompt",
	}
	_, err = svc.ProposeCreateBot(ctx, bp, "user_admin")
	if err != nil {
		t.Fatalf("ProposeCreateBot failed: %v", err)
	}

	// Nil hook executes safely and returns nil
	err = svc.RestartInstance(ctx, "user_nil_hook_bot")
	if err != nil {
		t.Fatalf("RestartInstance with nil hook failed: %v", err)
	}
}

func TestRestartInstance_CoordinatorErrorPropagation(t *testing.T) {
	ctx := context.Background()
	expectedErr := fmt.Errorf("supervisor communication timeout")

	svc := NewService(WithRestartCoordinator(func(ctx context.Context, botID string) error {
		return expectedErr
	}))

	bp := BotBlueprint{
		BotID:        "user_coord_err_bot",
		DisplayName:  "Coord Error Bot",
		Role:         "reviewer",
		SystemPrompt: "Prompt",
	}
	_, _ = svc.ProposeCreateBot(ctx, bp, "user_admin")

	err := svc.RestartInstance(ctx, "user_coord_err_bot")
	if err != expectedErr {
		t.Fatalf("expected coordinator error propagated, got: %v", err)
	}
}

func TestRestartInstance_ConcurrentRestarts(t *testing.T) {
	ctx := context.Background()
	var restartCount int
	var mu sync.Mutex

	svc := NewService(WithRestartCoordinator(func(ctx context.Context, botID string) error {
		mu.Lock()
		restartCount++
		mu.Unlock()
		return nil
	}))

	bp := BotBlueprint{
		BotID:        "user_conc_restart_bot",
		DisplayName:  "Conc Restart Bot",
		Role:         "reviewer",
		SystemPrompt: "Prompt",
	}
	_, _ = svc.ProposeCreateBot(ctx, bp, "user_admin")

	var wg sync.WaitGroup
	const totalRoutines = 10
	for i := 0; i < totalRoutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = svc.RestartInstance(ctx, "user_conc_restart_bot")
		}()
	}
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	if restartCount != totalRoutines {
		t.Fatalf("expected %d restarts, got %d", totalRoutines, restartCount)
	}
}

func TestGetBlueprint_EmptyIDAndDeepCopyImmutability(t *testing.T) {
	ctx := context.Background()
	svc := NewService()

	// Empty ID
	_, err := svc.GetBlueprint(ctx, "   ")
	if err != ErrBotNotFound {
		t.Fatalf("expected ErrBotNotFound on empty botID, got: %v", err)
	}

	bp := BotBlueprint{
		BotID:         "user_immut_bot",
		DisplayName:   "Immutability Bot",
		Role:          "reviewer",
		SystemPrompt:  "Prompt",
		AllowedAgents: []string{"agent_1"},
	}
	_, err = svc.ProposeCreateBot(ctx, bp, "user_admin")
	if err != nil {
		t.Fatalf("ProposeCreateBot failed: %v", err)
	}

	retrieved, err := svc.GetBlueprint(ctx, "user_immut_bot")
	if err != nil {
		t.Fatalf("GetBlueprint failed: %v", err)
	}

	// Mutate retrieved struct field
	retrieved.DisplayName = "Hacked Name"

	// Re-query
	original, err := svc.GetBlueprint(ctx, "user_immut_bot")
	if err != nil {
		t.Fatalf("second GetBlueprint failed: %v", err)
	}
	if original.DisplayName != "Immutability Bot" {
		t.Fatalf("internal blueprint was mutated: %s", original.DisplayName)
	}
}

func TestGetBlueprint_ReflectsPostUpdateState(t *testing.T) {
	ctx := context.Background()
	svc := NewService()

	bp := BotBlueprint{
		BotID:        "user_reflect_bot",
		DisplayName:  "Reflect Bot",
		Role:         "reviewer",
		SystemPrompt: "Prompt",
		Persona:      "Original Persona",
	}
	_, err := svc.ProposeCreateBot(ctx, bp, "user_admin")
	if err != nil {
		t.Fatalf("ProposeCreateBot failed: %v", err)
	}

	updates := bp
	updates.Persona = "Updated Persona"
	_, err = svc.ProposeUpdateBot(ctx, "user_reflect_bot", updates, "user_admin")
	if err != nil {
		t.Fatalf("ProposeUpdateBot failed: %v", err)
	}

	current, err := svc.GetBlueprint(ctx, "user_reflect_bot")
	if err != nil {
		t.Fatalf("GetBlueprint failed: %v", err)
	}
	if current.Persona != "Updated Persona" {
		t.Fatalf("expected updated persona 'Updated Persona', got: %s", current.Persona)
	}
}
