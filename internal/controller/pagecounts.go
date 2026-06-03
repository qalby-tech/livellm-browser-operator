package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// controllerBrowserInfo mirrors an item from the controller's GET /parser/browsers.
type controllerBrowserInfo struct {
	BrowserID    string `json:"browser_id"`
	WsURL        string `json:"ws_url"`
	SessionCount int    `json:"session_count"`
}

// fetchControllerPageCounts queries a controller's GET /parser/browsers and
// returns {browser_id: session_count}. Best-effort: returns an empty map on any
// error (the controller may not be ready, or no browsers are connected yet).
// This replaces the old Redis-published controller state used for autoscaling.
func fetchControllerPageCounts(ctx context.Context, name, namespace string) map[string]int {
	url := fmt.Sprintf("http://%s.%s.svc.cluster.local:%d/parser/browsers", name, namespace, controllerPort)
	reqCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, url, nil)
	if err != nil {
		return map[string]int{}
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return map[string]int{}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return map[string]int{}
	}

	var items []controllerBrowserInfo
	if err := json.NewDecoder(resp.Body).Decode(&items); err != nil {
		return map[string]int{}
	}
	counts := make(map[string]int, len(items))
	for _, it := range items {
		counts[it.BrowserID] = it.SessionCount
	}
	return counts
}
