package envelope

import (
	"strings"
	"testing"
	"time"
)

func rootFixture() *Envelope {
	e := NewRoot("user_bot", ChannelCLI, MsgRequest, "idem-root")
	e.PayloadHash = strings.Repeat("ab", 32)
	e.PayloadRef = "payload://x"
	return e
}

func codeOf(t *testing.T, err error) Code {
	t.Helper()
	ve, ok := err.(*ValidationError)
	if !ok {
		t.Fatalf("期望 *ValidationError，得到 %T: %v", err, err)
	}
	return ve.Code
}

func TestRootValid(t *testing.T) {
	if err := rootFixture().Validate(); err != nil {
		t.Fatalf("根信封应合法: %v", err)
	}
}

func TestDeriveValid(t *testing.T) {
	parent := rootFixture()
	child, err := Derive(parent, "echo_bot", ChannelTransport, MsgReply, "idem-child", parent.EnvelopeID)
	if err != nil {
		t.Fatalf("派生应成功: %v", err)
	}
	if child.CorrelationID != parent.CorrelationID {
		t.Errorf("correlation_id 必须整链不变")
	}
	if child.RootEnvelopeID != parent.RootEnvelopeID {
		t.Errorf("root_envelope_id 必须整链不变")
	}
	if child.CausationID != parent.EnvelopeID {
		t.Errorf("causation_id 应指向父节点")
	}
	if child.Traceparent[3:35] != parent.Traceparent[3:35] {
		t.Errorf("子 span 必须继承同一 trace id")
	}
	if child.Traceparent == parent.Traceparent {
		t.Errorf("每一跳必须生成新 span id")
	}
	if err := child.Validate(); err != nil {
		t.Fatalf("派生信封应合法: %v", err)
	}
}

func TestDeriveRejectsNilParent(t *testing.T) {
	if _, err := Derive(nil, "echo_bot", ChannelCLI, MsgReply, "k", "cause"); codeOf(t, err) != CodeCausalRequired {
		t.Fatalf("期望 CAUSAL_CONTEXT_REQUIRED，得到 %v", err)
	}
}

// 失败注入 1：缺因果头
func TestMissingCausalPath(t *testing.T) {
	e := rootFixture()
	e.CausalPath = nil
	if err := e.Validate(); codeOf(t, err) != CodeCausalRequired {
		t.Fatalf("期望 CAUSAL_CONTEXT_REQUIRED，得到 %v", err)
	}
}

// 失败注入 2：路径断裂（末项 != causation_id）
func TestCausalPathBroken(t *testing.T) {
	e := rootFixture()
	child, err := Derive(e, "echo_bot", ChannelCLI, MsgReply, "k", e.EnvelopeID)
	if err != nil {
		t.Fatal(err)
	}
	child.CausationID = "env_not_in_path"
	if err := child.Validate(); codeOf(t, err) != CodeCausationMismatch {
		t.Fatalf("期望 CAUSATION_MISMATCH，得到 %v", err)
	}
}

func TestCausalPathCycle(t *testing.T) {
	e := rootFixture()
	child, err := Derive(e, "echo_bot", ChannelCLI, MsgReply, "k", e.EnvelopeID)
	if err != nil {
		t.Fatal(err)
	}
	child.CausalPath = append(child.CausalPath, child.CausalPath[0])
	if err := child.Validate(); codeOf(t, err) != CodePathBroken {
		t.Fatalf("期望 CAUSAL_PATH_BROKEN（成环），得到 %v", err)
	}
}

// 失败注入 3：深度超过 32
func TestCausalDepthExceeded(t *testing.T) {
	e := rootFixture()
	e.CausationID = "env_parent"
	e.CausalPath = make([]CausalNode, MaxCausalDepth+1)
	for i := range e.CausalPath {
		id := "env_node_" + string(rune('a'+i%26)) + strings.Repeat("x", i/26)
		e.CausalPath[i] = CausalNode{NodeID: id, NodeType: "job", Actor: "echo_bot", At: time.Now().UTC()}
	}
	e.RootEnvelopeID = e.CausalPath[0].NodeID
	e.CausationID = e.CausalPath[len(e.CausalPath)-1].NodeID
	if err := e.Validate(); codeOf(t, err) != CodeDepthExceeded {
		t.Fatalf("期望 CAUSAL_DEPTH_EXCEEDED，得到 %v", err)
	}
}

func TestCausalDepthAtLimitIsValid(t *testing.T) {
	e := rootFixture()
	e.CausalPath = make([]CausalNode, MaxCausalDepth)
	for i := range e.CausalPath {
		id := "env_n" + strings.Repeat("y", i+1)
		e.CausalPath[i] = CausalNode{NodeID: id, NodeType: "job", Actor: "echo_bot", At: time.Now().UTC()}
	}
	e.RootEnvelopeID = e.CausalPath[0].NodeID
	e.CausationID = e.CausalPath[MaxCausalDepth-1].NodeID
	if err := e.Validate(); err != nil {
		t.Fatalf("深度恰为 %d 应合法: %v", MaxCausalDepth, err)
	}
}

func TestDeriveAtDepthLimitRejected(t *testing.T) {
	e := rootFixture()
	e.CausalPath = make([]CausalNode, MaxCausalDepth)
	for i := range e.CausalPath {
		id := "env_d" + strings.Repeat("z", i+1)
		e.CausalPath[i] = CausalNode{NodeID: id, NodeType: "job", Actor: "echo_bot", At: time.Now().UTC()}
	}
	e.RootEnvelopeID = e.CausalPath[0].NodeID
	e.CausationID = e.CausalPath[MaxCausalDepth-1].NodeID
	// 以父信封自身为直接原因：需追加该节点，路径将达 33 而被拒
	if _, err := Derive(e, "echo_bot", ChannelCLI, MsgReply, "k", e.EnvelopeID); codeOf(t, err) != CodeDepthExceeded {
		t.Fatalf("期望 CAUSAL_DEPTH_EXCEEDED，得到 %v", err)
	}
}

// 失败注入 4：根不一致
func TestRootMismatch(t *testing.T) {
	e := rootFixture()
	e.RootEnvelopeID = "env_other_root"
	if err := e.Validate(); codeOf(t, err) != CodeRootMismatch {
		t.Fatalf("期望 ROOT_ENVELOPE_MISMATCH，得到 %v", err)
	}
}

func TestSchemaVersion(t *testing.T) {
	e := rootFixture()
	e.SchemaVersion = 2
	if err := e.Validate(); codeOf(t, err) != CodeSchemaVersion {
		t.Fatalf("期望 SCHEMA_VERSION_UNSUPPORTED，得到 %v", err)
	}
}

func TestRequiredFields(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Envelope)
		field  string
	}{
		{"envelope_id", func(e *Envelope) { e.EnvelopeID = "" }, "envelope_id"},
		{"msg_type", func(e *Envelope) { e.MsgType = "" }, "msg_type"},
		{"from_bot_id", func(e *Envelope) { e.FromBotID = "" }, "from_bot_id"},
		{"channel", func(e *Envelope) { e.Channel = "" }, "channel"},
		{"correlation_id", func(e *Envelope) { e.CorrelationID = "" }, "correlation_id"},
		{"root_envelope_id", func(e *Envelope) { e.RootEnvelopeID = "" }, "root_envelope_id"},
		{"idempotency_key", func(e *Envelope) { e.IdempotencyKey = "" }, "idempotency_key"},
		{"traceparent", func(e *Envelope) { e.Traceparent = "" }, "traceparent"},
		{"received_at", func(e *Envelope) { e.ReceivedAt = time.Time{} }, "received_at"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := rootFixture()
			tc.mutate(e)
			err := e.Validate()
			if codeOf(t, err) != CodeRequiredField {
				t.Fatalf("期望 REQUIRED_FIELD_MISSING，得到 %v", err)
			}
			if err.(*ValidationError).Field != tc.field {
				t.Fatalf("期望字段 %s，得到 %s", tc.field, err.(*ValidationError).Field)
			}
		})
	}
}

func TestValueDomains(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Envelope)
		code   Code
	}{
		{"msg_type", func(e *Envelope) { e.MsgType = "chat" }, CodeInvalidMsgType},
		{"channel", func(e *Envelope) { e.Channel = "smtp" }, CodeInvalidChannel},
		{"bot_id 大写", func(e *Envelope) { e.FromBotID = "User_Bot" }, CodeInvalidBotID},
		{"bot_id 过短", func(e *Envelope) { e.FromBotID = "abc" }, CodeInvalidBotID},
		{"traceparent", func(e *Envelope) { e.Traceparent = "not-a-trace" }, CodeCausalRequired},
		{"payload_hash", func(e *Envelope) { e.PayloadHash = "zz" }, CodeInvalidHash},
		{"payload_ref 缺 hash", func(e *Envelope) { e.PayloadHash = "" }, CodeRequiredField},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := rootFixture()
			tc.mutate(e)
			if err := e.Validate(); codeOf(t, err) != tc.code {
				t.Fatalf("期望 %s，得到 %v", tc.code, err)
			}
		})
	}
}

// 内核不得替调用方补造历史：根节点路径必须只含自身
func TestRootPathMustBeSelfOnly(t *testing.T) {
	e := rootFixture()
	e.CausalPath = append(e.CausalPath, CausalNode{NodeID: "env_extra", NodeType: "job", Actor: "user_bot", At: time.Now().UTC()})
	if err := e.Validate(); codeOf(t, err) != CodePathBroken {
		t.Fatalf("期望 CAUSAL_PATH_BROKEN，得到 %v", err)
	}
}
