package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime"
)

// handleResolverInfo returns live resolver configuration and server info
func (s *Server) handleResolverInfo(w http.ResponseWriter, r *http.Request) {
	kresdBase := envOr("KRESD_METRICS_URL", "http://kresd:5000/metrics")
	// Derive management API base from metrics URL
	mgmtBase := kresdBase[:len(kresdBase)-len("/metrics")]
	if mgmtBase == "http://kresd:5000" {
		mgmtBase = "http://kresd:5000"
	}

	info := map[string]interface{}{
		"server": getServerInfo(),
	}

	// Fetch live config from kresd management API
	if configData, err := fetchJSON(s.httpClient, mgmtBase+"/v1/config"); err == nil {
		info["config"] = configData
	}

	// Fetch cache config specifically
	if cacheData, err := fetchJSON(s.httpClient, mgmtBase+"/v1/config/cache"); err == nil {
		info["cache"] = cacheData
	}

	// Actual occupancy, measured from the LMDB itself — kresd does not export it.
	info["cache_usage"] = readCacheUsage(r.Context())

	// Fetch network config
	if netData, err := fetchJSON(s.httpClient, mgmtBase+"/v1/config/network"); err == nil {
		info["network"] = netData
	}

	// Fetch options
	if optData, err := fetchJSON(s.httpClient, mgmtBase+"/v1/config/options"); err == nil {
		info["options"] = optData
	}

	// Fetch monitoring config
	if monData, err := fetchJSON(s.httpClient, mgmtBase+"/v1/config/monitoring"); err == nil {
		info["monitoring"] = monData
	}

	// Fetch workers
	if wrkData, err := fetchJSON(s.httpClient, mgmtBase+"/v1/config/workers"); err == nil {
		info["workers"] = wrkData
	}

	writeJSON(w, info)
}

func fetchJSON(client *http.Client, url string) (interface{}, error) {
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var data interface{}
	if err := json.Unmarshal(body, &data); err != nil {
		// Try as plain string
		return string(body), nil
	}
	return data, nil
}

func getServerInfo() map[string]interface{} {
	hostname, _ := os.Hostname()
	return map[string]interface{}{
		"hostname":  hostname,
		"cpus":      runtime.NumCPU(),
		"goversion": runtime.Version(),
	}
}
