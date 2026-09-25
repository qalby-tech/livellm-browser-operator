package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// controllerBrowserInfo mirrors an item from the controller's GET /parser/browsers
// (controller 2.4.0+). Older controllers send no open_tabs.
type controllerBrowserInfo struct {
	BrowserID    string `json:"browser_id"`
	OpenTabs     int    `json:"open_tabs"`
	SessionCount int    `json:"session_count"`
}

// browserLoad is one browser's load as the controller reports it.
type browserLoad struct {
	OpenTabs int
	Sessions int
}

// pageCountTimeout bounds the call: a controller the operator can't reach
// (a NetworkPolicy without the operator's namespace) must not stall every
// reconcile.
const pageCountTimeout = 3 * time.Second

// fetchControllerLoads queries a controller's GET /parser/browsers and returns
// {browser_id: load}. ok is false when the controller could not be asked
// (not ready, unreachable, or an unexpected answer).
func fetchControllerLoads(ctx context.Context, name, namespace string) (map[string]browserLoad, bool) {
	url := fmt.Sprintf("http://%s.%s.svc.cluster.local:%d/parser/browsers", name, namespace, controllerPort)
	reqCtx, cancel := context.WithTimeout(ctx, pageCountTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, url, nil)
	if err != nil {
		return nil, false
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, false
	}
	return decodeControllerLoads(resp.Body)
}

func decodeControllerLoads(body io.Reader) (map[string]browserLoad, bool) {
	var items []controllerBrowserInfo
	if err := json.NewDecoder(body).Decode(&items); err != nil {
		return nil, false
	}
	loads := make(map[string]browserLoad, len(items))
	for _, it := range items {
		loads[it.BrowserID] = browserLoad{OpenTabs: it.OpenTabs, Sessions: it.SessionCount}
	}
	return loads, true
}
