package toolcenter

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const searchToolID = "sys.search.web"

// Isolation 是一次沙箱执行。Dial 为空时使用系统信任根做 DNS 与 HTTPS。
type Isolation struct {
	ToolID           string
	SearchAuthorized bool
	Params           map[string]interface{}
	Dial             func(ctx context.Context, rawURL string) (string, error)
	Run              func() (string, int, error)
}

// Isolate 默认拒绝出站。只有已授权的互联网搜索工具能对参数中的 HTTPS 地址做 DNS 解析并请求。
// 其他工具的参数里即使写了 URL，也不会拨号。
func Isolate(ctx context.Context, iso Isolation) (output string, exitCode int, networked bool, host string, err error) {
	if ctx == nil {
		ctx = context.Background()
	}
	rawURL := firstURL(iso.Params)
	search := iso.SearchAuthorized && iso.ToolID == searchToolID
	if rawURL != "" && !search {
		return "", 126, false, "", ErrNetworkDenied
	}
	if search && rawURL != "" {
		parsed, perr := url.Parse(rawURL)
		if perr != nil || parsed.Scheme != "https" || parsed.Hostname() == "" {
			return "", 126, false, "", fmt.Errorf("搜索外联只接受带主机名的 HTTPS URL")
		}
		dial := iso.Dial
		if dial == nil {
			dial = func(ctx context.Context, raw string) (string, error) {
				return DialHTTPS(ctx, raw, nil)
			}
		}
		host, err = dial(ctx, rawURL)
		if err != nil {
			if host == "" {
				host = parsed.Hostname()
			}
			return "", 1, false, host, err
		}
		if host == "" {
			host = parsed.Hostname()
		}
		networked = true
	}
	if iso.Run == nil {
		return "", 0, networked, host, nil
	}
	output, exitCode, err = iso.Run()
	return output, exitCode, networked, host, err
}

// DialHTTPS 先解析主机名，再发起 HTTPS GET。完成响应即视为外联成功。
func DialHTTPS(ctx context.Context, rawURL string, roots *x509.CertPool) (string, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" {
		return "", fmt.Errorf("搜索外联只接受带主机名的 HTTPS URL")
	}
	host := parsed.Hostname()
	lookupCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	ips, err := net.DefaultResolver.LookupHost(lookupCtx, host)
	if err != nil {
		return host, fmt.Errorf("DNS 解析 %s 失败: %w", host, err)
	}
	if len(ips) == 0 {
		return host, fmt.Errorf("DNS 解析 %s 没有地址", host)
	}
	client := &http.Client{Timeout: 5 * time.Second}
	if roots != nil {
		client.Transport = &http.Transport{
			TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12},
		}
	}
	req, err := http.NewRequestWithContext(lookupCtx, http.MethodGet, rawURL, nil)
	if err != nil {
		return host, fmt.Errorf("构造 HTTPS 请求失败: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return host, fmt.Errorf("HTTPS 访问 %s 失败: %w", host, err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	return host, nil
}

// DialHTTPSWithCAFile 使用 PEM 证书作为信任根。
func DialHTTPSWithCAFile(path string) (func(context.Context, string) (string, error), error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取沙箱 CA %s: %w", path, err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(body) {
		return nil, fmt.Errorf("沙箱 CA %s 不是合法 PEM", path)
	}
	return func(ctx context.Context, rawURL string) (string, error) {
		return DialHTTPS(ctx, rawURL, pool)
	}, nil
}

// Distill 压缩工具输出。超长部分不进入 Bot 上下文。
func Distill(raw string) string {
	raw = strings.TrimSpace(raw)
	const max = 240
	if len(raw) <= max {
		return raw
	}
	return raw[:max] + "…(已蒸馏，原文不进入 Bot 上下文)"
}

func firstURL(params map[string]interface{}) string {
	var found string
	var walk func(any)
	walk = func(v any) {
		if found != "" || v == nil {
			return
		}
		switch t := v.(type) {
		case string:
			if u := extractURL(t); u != "" {
				found = u
			}
		case map[string]interface{}:
			for _, child := range t {
				walk(child)
			}
		case []any:
			for _, child := range t {
				walk(child)
			}
		}
	}
	walk(params)
	return found
}

func extractURL(s string) string {
	httpsAt := strings.Index(s, "https://")
	httpAt := strings.Index(s, "http://")
	at := -1
	switch {
	case httpsAt >= 0 && (httpAt < 0 || httpsAt <= httpAt):
		at = httpsAt
	case httpAt >= 0:
		at = httpAt
	default:
		return ""
	}
	rest := s[at:]
	end := len(rest)
	for i, r := range rest {
		if r == ' ' || r == '"' || r == '\'' || r == ',' || r == ')' || r == '\n' {
			end = i
			break
		}
	}
	if end <= len("http://") {
		return ""
	}
	return rest[:end]
}
