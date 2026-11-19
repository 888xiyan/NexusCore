package httpapi

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// SetMarketplaceUpstream 把商场检索与下载转发到插件。Hub 不保存目录，也不在这里批准安装。
func (a *API) SetMarketplaceUpstream(base string) {
	a.marketUpstream = strings.TrimRight(strings.TrimSpace(base), "/")
	if a.marketHTTP == nil {
		a.marketHTTP = &http.Client{Timeout: 10 * time.Second}
	}
}

func (a *API) relayMarketplace(w http.ResponseWriter, r *http.Request) bool {
	if a.marketUpstream == "" {
		return false
	}
	const prefix = "/v1/marketplace"
	if r.URL.Path != prefix && !strings.HasPrefix(r.URL.Path, prefix+"/") {
		return false
	}
	target := a.marketUpstream + "/api/marketplace" + strings.TrimPrefix(r.URL.Path, prefix)
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}
	client := a.marketHTTP
	if client == nil {
		client = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(r.Context(), r.Method, target, r.Body)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "转发商场请求失败: "+err.Error())
		return true
	}
	if ct := r.Header.Get("Content-Type"); ct != "" {
		req.Header.Set("Content-Type", ct)
	}
	resp, err := client.Do(req)
	if err != nil {
		writeErr(w, http.StatusBadGateway, fmt.Sprintf("商场插件不可达: %v", err))
		return true
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
	return true
}
