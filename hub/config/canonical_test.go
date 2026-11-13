package config

import (
	"encoding/json"
	"strings"
	"testing"
)

// 交叉验证：Go 的 canonical 算法必须与迁移种子中预计算的 checksum 完全一致。
// 种子值由独立实现（Python json.dumps(sort_keys=True, separators=(',',':'), ensure_ascii=False)）算出。
func TestChecksumMatchesMigrationSeeds(t *testing.T) {
	cases := []struct {
		name     string
		specJSON string
		want     string
	}{
		{
			"bot/echo_bot v1",
			`{"activation_mode":"lazy","capabilities":["echo"],"idle_ttl_seconds":300,"model":"stub","prompt_ref":"echo","start_timeout_seconds":30}`,
			"e71009676c0cefd144f872dde5f0474d9c39ee2d29faf9cd36262666cb8e5e9d",
		},
		{
			"global/kernel v1",
			`{"heartbeat_seconds":10,"lease_seconds":60,"max_causal_depth":32,"outbox_batch_size":100,"outbox_scan_interval_ms":250,"poison_pill_attempts":5,"retry_backoff_seconds":5,"snapshot_retention_days":30,"wakeup_poll_seconds":5}`,
			"d115727d74d034f5d3d137173ece6de5e74a71720be182fa0b85e5f8d691a2f7",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var spec any
			if err := json.Unmarshal([]byte(tc.specJSON), &spec); err != nil {
				t.Fatal(err)
			}
			got, err := Checksum(spec)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("checksum 与迁移种子不一致\n got=%s\nwant=%s", got, tc.want)
			}
		})
	}
}

func TestCanonicalKeyOrderAndWhitespace(t *testing.T) {
	var a, b any
	if err := json.Unmarshal([]byte(`{"z":1,"a":{"y":2,"b":3}}`), &a); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(`{ "a" : { "b" : 3, "y" : 2 }, "z" : 1 }`), &b); err != nil {
		t.Fatal(err)
	}
	ca, err := CanonicalJSON(a)
	if err != nil {
		t.Fatal(err)
	}
	cb, err := CanonicalJSON(b)
	if err != nil {
		t.Fatal(err)
	}
	if string(ca) != string(cb) {
		t.Fatalf("键序与空白差异必须被规范化消除:\n%s\n%s", ca, cb)
	}
	if string(ca) != `{"a":{"b":3,"y":2},"z":1}` {
		t.Fatalf("canonical 形式不符: %s", ca)
	}
}

func TestCanonicalNoHTMLEscape(t *testing.T) {
	v, err := DecodeSpec([]byte(`{"url":"a<b>&c"}`))
	if err != nil {
		t.Fatal(err)
	}
	c, err := CanonicalJSON(v)
	if err != nil {
		t.Fatal(err)
	}
	// 精确比对即可覆盖「不做 HTML 转义」：若被转义，输出必然不等于原文
	if string(c) != `{"url":"a<b>&c"}` {
		t.Fatalf("canonical 形式不符（不得做 HTML 转义）: %s", c)
	}
}

func TestCanonicalPreservesIntegerLiteral(t *testing.T) {
	v, err := DecodeSpec([]byte(`{"n":9007199254740993}`))
	if err != nil {
		t.Fatal(err)
	}
	c, err := CanonicalJSON(v)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(c), "9007199254740993") {
		t.Fatalf("大整数不得被 float64 往返改写: %s", c)
	}
}

func TestChecksumChangesOnAnyByteChange(t *testing.T) {
	base := map[string]any{"model": "stub"}
	c1, err := Checksum(base)
	if err != nil {
		t.Fatal(err)
	}
	c2, err := Checksum(map[string]any{"model": "stub "})
	if err != nil {
		t.Fatal(err)
	}
	if c1 == c2 {
		t.Fatal("任何字节变化都必须改变 checksum")
	}
}

func TestScanSecretsRejectsPlaintext(t *testing.T) {
	cases := []string{
		`{"password":"p@ss"}`,
		`{"api_key":"sk-123"}`,
		`{"nested":{"token":"abc"}}`,
		`{"db_password":"x"}`,
		`{"list":[{"private_key":"k"}]}`,
		`{"client_secret":"s"}`,
		`{"TOKEN":"upper"}`,
	}
	for _, raw := range cases {
		t.Run(raw, func(t *testing.T) {
			var v any
			if err := json.Unmarshal([]byte(raw), &v); err != nil {
				t.Fatal(err)
			}
			if err := ScanSecrets(v); err == nil {
				t.Fatalf("明文密钥必须被拒绝: %s", raw)
			}
		})
	}
}

func TestScanSecretsAllowsRefAndEmpty(t *testing.T) {
	cases := []string{
		`{"password":{"$secret_ref":"ref_db"}}`,
		`{"api_key":{"$secret_ref":"ref_llm"}}`,
		`{"token":""}`,
		`{"model":"stub","prompt_ref":"echo"}`,
		`{"password":null}`,
	}
	for _, raw := range cases {
		t.Run(raw, func(t *testing.T) {
			var v any
			if err := json.Unmarshal([]byte(raw), &v); err != nil {
				t.Fatal(err)
			}
			if err := ScanSecrets(v); err != nil {
				t.Fatalf("应被接受: %s (%v)", raw, err)
			}
		})
	}
}

func TestScanSecretsRejectsMalformedRef(t *testing.T) {
	cases := []string{
		`{"password":{"$secret_ref":""}}`,
		`{"password":{"$secret_ref":"r","extra":1}}`,
		`{"password":{"$secret_ref":42}}`,
	}
	for _, raw := range cases {
		t.Run(raw, func(t *testing.T) {
			var v any
			if err := json.Unmarshal([]byte(raw), &v); err != nil {
				t.Fatal(err)
			}
			if err := ScanSecrets(v); err == nil {
				t.Fatalf("非法 secret_ref 必须被拒绝: %s", raw)
			}
		})
	}
}

func TestCollectSecretRefs(t *testing.T) {
	var v any
	if err := json.Unmarshal([]byte(`{"a":{"$secret_ref":"r1"},"b":[{"$secret_ref":"r2"}],"c":"plain"}`), &v); err != nil {
		t.Fatal(err)
	}
	refs, err := CollectSecretRefs(v)
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 2 {
		t.Fatalf("期望 2 个引用，得到 %v", refs)
	}
}
