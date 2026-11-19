package toolcenter

import (
	"context"
	"crypto/x509"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestIsolateBlocksURLOnOrdinaryTool(t *testing.T) {
	dialed := false
	ran := false
	_, code, networked, _, err := Isolate(context.Background(), Isolation{
		ToolID:           "sys.git.status",
		SearchAuthorized: true,
		Params:           map[string]interface{}{"cmd": "curl https://example.com/secret"},
		Dial: func(context.Context, string) (string, error) {
			dialed = true
			return "example.com", nil
		},
		Run: func() (string, int, error) {
			ran = true
			return "should not run", 0, nil
		},
	})
	if err == nil || code != 126 || networked || dialed || ran {
		t.Fatalf("普通工具写了 URL 仍发生外联: code=%d networked=%v dialed=%v ran=%v err=%v", code, networked, dialed, ran, err)
	}
}

func TestSearchDialNotifiesOnlyAfterHTTPS(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(strings.Repeat("raw-body-", 80)))
	}))
	defer srv.Close()

	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())

	svc := NewToolCenterService()
	botID := "search_bot"
	if err := svc.SetNamedBots(searchToolID, []string{botID}); err != nil {
		t.Fatal(err)
	}
	if err := svc.GrantTool(BotToolGrant{BotID: botID, ToolID: searchToolID, Permission: PermissionExecute}); err != nil {
		t.Fatal(err)
	}
	svc.SetExtraAuditRecipients([]string{"assistant_bot"})
	svc.SetEgressDialer(func(ctx context.Context, rawURL string) (string, error) {
		return DialHTTPS(ctx, rawURL, pool)
	})

	res, err := svc.ExecuteTool(botID, searchToolID, map[string]interface{}{
		"query": srv.URL + "/q",
	}, func() (string, int, error) {
		return strings.Repeat("X", 500), 0, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.NetworkTriggered || res.EgressTarget != "127.0.0.1" {
		t.Fatalf("搜索未完成 DNS/HTTPS: %+v", res)
	}
	if !strings.Contains(res.Output, "已蒸馏") || len(res.Output) > 300 {
		t.Fatalf("原始大输出进入了结果: %q", res.Output)
	}
	user := svc.ListAudit("user_bot")
	assistant := svc.ListAudit("assistant_bot")
	if len(user) != 1 || len(assistant) != 1 {
		t.Fatalf("审计接收者不符 user=%d assistant=%d", len(user), len(assistant))
	}
	if !strings.Contains(user[0].Text, botID) || !strings.Contains(user[0].Text, searchToolID) || user[0].Domain != "127.0.0.1" {
		t.Fatalf("审计正文不完整: %+v", user[0])
	}
	traces := svc.ListTraces(botID)
	if len(traces) == 0 || !traces[len(traces)-1].Networked || traces[len(traces)-1].Distilled == "" {
		t.Fatalf("执行轨迹缺少联网与蒸馏字段: %+v", traces)
	}
}

func TestHTTPURLDoesNotOpenSearch(t *testing.T) {
	dialed := false
	_, _, networked, _, err := Isolate(context.Background(), Isolation{
		ToolID:           searchToolID,
		SearchAuthorized: true,
		Params:           map[string]interface{}{"query": "http://example.com/q"},
		Dial: func(context.Context, string) (string, error) {
			dialed = true
			return "example.com", nil
		},
	})
	if err == nil || networked || dialed {
		t.Fatalf("明文 HTTP 不应拨号: networked=%v dialed=%v err=%v", networked, dialed, err)
	}
}
