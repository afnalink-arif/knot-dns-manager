package api

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
)

// --- Cluster role management ---

func (s *Server) getRole() string {
	if v, ok := s.clusterRole.Load().(string); ok && v != "" {
		return v
	}
	return "standalone"
}

func (s *Server) initClusterRole() {
	ctx := context.Background()

	// Seed cluster_config if empty
	s.pg.Exec(ctx,
		`INSERT INTO cluster_config (id, node_role, node_name) VALUES (1, $1, $2) ON CONFLICT (id) DO NOTHING`,
		s.cfg.NodeRole, s.cfg.NodeName,
	)

	// Load role from DB
	var role string
	err := s.pg.QueryRow(ctx, "SELECT node_role FROM cluster_config WHERE id = 1").Scan(&role)
	if err != nil {
		role = "standalone"
	}
	s.clusterRole.Store(role)

	// Start poller if controller
	if role == "controller" {
		s.startPoller()
	}
}

func (s *Server) startPoller() {
	if s.pollerCancel != nil {
		s.pollerCancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.pollerCancel = cancel
	go s.runPoller(ctx)
}

func (s *Server) stopPoller() {
	if s.pollerCancel != nil {
		s.pollerCancel()
		s.pollerCancel = nil
	}
}

// --- Cluster token middleware ---

func (s *Server) clusterTokenMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Only active when role is "agent"
		if s.getRole() != "agent" {
			http.Error(w, `{"error":"agent API not active"}`, http.StatusNotFound)
			return
		}

		token := r.Header.Get("X-Cluster-Token")
		if token == "" {
			http.Error(w, `{"error":"cluster token required"}`, http.StatusUnauthorized)
			return
		}

		var storedToken string
		err := s.pg.QueryRow(r.Context(),
			"SELECT controller_token FROM cluster_config WHERE id = 1",
		).Scan(&storedToken)
		if err != nil || storedToken == "" ||
			subtle.ConstantTimeCompare([]byte(token), []byte(storedToken)) != 1 {
			http.Error(w, `{"error":"invalid cluster token"}`, http.StatusForbidden)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// --- Cluster config endpoints ---

type ClusterConfigResponse struct {
	NodeRole         string `json:"node_role"`
	NodeName         string `json:"node_name"`
	NodeDomain       string `json:"node_domain"`
	ControllerDomain string `json:"controller_domain,omitempty"`
	ControllerToken  string `json:"controller_token,omitempty"`
}

func (s *Server) handleClusterConfig(w http.ResponseWriter, r *http.Request) {
	var cfg ClusterConfigResponse
	err := s.pg.QueryRow(r.Context(),
		"SELECT node_role, node_name, node_domain, controller_domain, controller_token FROM cluster_config WHERE id = 1",
	).Scan(&cfg.NodeRole, &cfg.NodeName, &cfg.NodeDomain, &cfg.ControllerDomain, &cfg.ControllerToken)
	if err != nil {
		cfg.NodeRole = "standalone"
	}
	writeJSON(w, cfg)
}

func (s *Server) handleUpdateClusterConfig(w http.ResponseWriter, r *http.Request) {
	var req struct {
		NodeRole         *string `json:"node_role"`
		NodeName         *string `json:"node_name"`
		NodeDomain       *string `json:"node_domain"`
		ControllerDomain *string `json:"controller_domain"`
		ControllerToken  *string `json:"controller_token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid request"}`, http.StatusBadRequest)
		return
	}

	ctx := r.Context()

	if req.NodeRole != nil {
		role := *req.NodeRole
		if role != "standalone" && role != "controller" && role != "agent" {
			http.Error(w, `{"error":"invalid role, must be standalone/controller/agent"}`, http.StatusBadRequest)
			return
		}
		s.pg.Exec(ctx, "UPDATE cluster_config SET node_role = $1, updated_at = NOW() WHERE id = 1", role)
		s.clusterRole.Store(role)

		// Manage poller lifecycle
		if role == "controller" {
			s.startPoller()
		} else {
			s.stopPoller()
		}
	}
	if req.NodeName != nil {
		s.pg.Exec(ctx, "UPDATE cluster_config SET node_name = $1, updated_at = NOW() WHERE id = 1", *req.NodeName)
	}
	if req.NodeDomain != nil {
		s.pg.Exec(ctx, "UPDATE cluster_config SET node_domain = $1, updated_at = NOW() WHERE id = 1", *req.NodeDomain)
	}
	if req.ControllerDomain != nil {
		s.pg.Exec(ctx, "UPDATE cluster_config SET controller_domain = $1, updated_at = NOW() WHERE id = 1", *req.ControllerDomain)
	}
	if req.ControllerToken != nil {
		s.pg.Exec(ctx, "UPDATE cluster_config SET controller_token = $1, updated_at = NOW() WHERE id = 1", *req.ControllerToken)
	}

	writeJSON(w, map[string]string{"message": "cluster config updated"})
}

// --- Agent pairing endpoint ---

func (s *Server) handleClusterPair(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Token            string `json:"token"`
		ControllerDomain string `json:"controller_domain"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Token == "" {
		http.Error(w, `{"error":"token required"}`, http.StatusBadRequest)
		return
	}

	ctx := r.Context()
	s.pg.Exec(ctx, "UPDATE cluster_config SET node_role = 'agent', controller_domain = $1, controller_token = $2, updated_at = NOW() WHERE id = 1",
		req.ControllerDomain, req.Token)
	s.clusterRole.Store("agent")
	s.stopPoller()

	writeJSON(w, map[string]string{"message": "paired successfully"})
}

// --- Node CRUD (controller) ---

type ClusterNode struct {
	ID         int        `json:"id"`
	Name       string     `json:"name"`
	Domain     string     `json:"domain"`
	APIToken   string     `json:"api_token,omitempty"`
	Status     string     `json:"status"`
	Version    string     `json:"version"`
	LastSeenAt *time.Time `json:"last_seen_at"`
	LastError  string     `json:"last_error"`
	CreatedAt  time.Time  `json:"created_at"`
}

func (s *Server) handleListNodes(w http.ResponseWriter, r *http.Request) {
	rows, err := s.pg.Query(r.Context(),
		"SELECT id, name, domain, status, version, last_seen_at, last_error, created_at FROM cluster_nodes ORDER BY id")
	if err != nil {
		http.Error(w, `{"error":"query failed"}`, http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	nodes := []ClusterNode{}
	for rows.Next() {
		var n ClusterNode
		rows.Scan(&n.ID, &n.Name, &n.Domain, &n.Status, &n.Version, &n.LastSeenAt, &n.LastError, &n.CreatedAt)
		nodes = append(nodes, n)
	}
	writeJSON(w, nodes)
}

func (s *Server) handleAddNode(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name   string `json:"name"`
		Domain string `json:"domain"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Domain == "" {
		http.Error(w, `{"error":"name and domain required"}`, http.StatusBadRequest)
		return
	}

	// Generate cluster token
	tokenBytes := make([]byte, 32)
	rand.Read(tokenBytes)
	token := hex.EncodeToString(tokenBytes)

	var id int
	err := s.pg.QueryRow(r.Context(),
		"INSERT INTO cluster_nodes (name, domain, api_token) VALUES ($1, $2, $3) RETURNING id",
		req.Name, req.Domain, token,
	).Scan(&id)
	if err != nil {
		if strings.Contains(err.Error(), "unique") {
			http.Error(w, `{"error":"domain already registered"}`, http.StatusConflict)
		} else {
			http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err.Error()), http.StatusInternalServerError)
		}
		return
	}

	w.WriteHeader(http.StatusCreated)
	writeJSON(w, map[string]interface{}{
		"id":        id,
		"name":      req.Name,
		"domain":    req.Domain,
		"api_token": token,
	})
}

func (s *Server) handleUpdateNode(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.Atoi(chi.URLParam(r, "id"))
	var req struct {
		Name   *string `json:"name"`
		Domain *string `json:"domain"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid request"}`, http.StatusBadRequest)
		return
	}

	if req.Name != nil {
		s.pg.Exec(r.Context(), "UPDATE cluster_nodes SET name = $1, updated_at = NOW() WHERE id = $2", *req.Name, id)
	}
	if req.Domain != nil {
		s.pg.Exec(r.Context(), "UPDATE cluster_nodes SET domain = $1, updated_at = NOW() WHERE id = $2", *req.Domain, id)
	}

	writeJSON(w, map[string]string{"message": "node updated"})
}

func (s *Server) handleDeleteNode(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.Atoi(chi.URLParam(r, "id"))
	s.pg.Exec(r.Context(), "DELETE FROM cluster_nodes WHERE id = $1", id)
	writeJSON(w, map[string]string{"message": "node deleted"})
}

// --- Node metrics proxy (controller fetches from agent) ---

func (s *Server) handleNodeMetrics(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.Atoi(chi.URLParam(r, "id"))

	var domain, token string
	err := s.pg.QueryRow(r.Context(),
		"SELECT domain, api_token FROM cluster_nodes WHERE id = $1", id,
	).Scan(&domain, &token)
	if err != nil {
		http.Error(w, `{"error":"node not found"}`, http.StatusNotFound)
		return
	}

	data, err := s.fetchAgentEndpoint(domain, token, "/metrics/overview")
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err.Error()), http.StatusBadGateway)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Write(data)
}

// --- Cluster overview (aggregated) ---

func (s *Server) handleClusterOverview(w http.ResponseWriter, r *http.Request) {
	type UpdateInfo struct {
		UpdateAvailable bool     `json:"update_available"`
		CommitsBehind   int      `json:"commits_behind"`
		CommitLog       []string `json:"commit_log,omitempty"`
	}
	type NodeOverview struct {
		ID            int              `json:"id"`
		Name          string           `json:"name"`
		Domain        string           `json:"domain"`
		Status        string           `json:"status"`
		Version       string           `json:"version"`
		IsLocal       bool             `json:"is_local"`
		LastSeenAt    *time.Time       `json:"last_seen_at"`
		LastError     string           `json:"last_error"`
		Metrics       *json.RawMessage `json:"metrics"`
		SystemMetrics *json.RawMessage `json:"system_metrics"`
		UpdateInfo    *UpdateInfo      `json:"update_info,omitempty"`
	}

	nodes := []NodeOverview{}

	// Include local/controller node
	var localName, localDomain string
	s.pg.QueryRow(r.Context(),
		"SELECT node_name, node_domain FROM cluster_config WHERE id = 1").Scan(&localName, &localDomain)
	if localName == "" {
		localName = "Controller (local)"
	}

	now := time.Now()
	localNode := NodeOverview{
		ID:         0,
		Name:       localName,
		Domain:     localDomain,
		Status:     "online",
		Version:    s.cfg.Version,
		IsLocal:    true,
		LastSeenAt: &now,
	}

	// Get local metrics from Prometheus
	localMetrics := s.getLocalOverviewMetrics(r.Context())
	if localMetrics != nil {
		localNode.Metrics = localMetrics
	}
	localSystem := s.getLocalSystemMetrics(r.Context())
	if localSystem != nil {
		localNode.SystemMetrics = localSystem
	}

	// Local update check (from in-memory cache)
	if cached := s.localUpdateCache.Load(); cached != nil {
		if data, ok := cached.(json.RawMessage); ok {
			var ui UpdateInfo
			if json.Unmarshal(data, &ui) == nil {
				localNode.UpdateInfo = &ui
			}
		}
	}

	nodes = append(nodes, localNode)

	// Remote agent nodes
	rows, err := s.pg.Query(r.Context(),
		"SELECT id, name, domain, status, version, last_seen_at, last_error FROM cluster_nodes ORDER BY id")
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var n NodeOverview
			rows.Scan(&n.ID, &n.Name, &n.Domain, &n.Status, &n.Version, &n.LastSeenAt, &n.LastError)

			// Get cached metrics
			var data json.RawMessage
			err := s.pg.QueryRow(r.Context(),
				"SELECT data FROM cluster_metrics_cache WHERE node_id = $1 AND metric_type = 'overview'", n.ID,
			).Scan(&data)
			if err == nil {
				n.Metrics = &data
			}
			var sysData json.RawMessage
			err = s.pg.QueryRow(r.Context(),
				"SELECT data FROM cluster_metrics_cache WHERE node_id = $1 AND metric_type = 'system'", n.ID,
			).Scan(&sysData)
			if err == nil {
				n.SystemMetrics = &sysData
			}
			var updateData json.RawMessage
			err = s.pg.QueryRow(r.Context(),
				"SELECT data FROM cluster_metrics_cache WHERE node_id = $1 AND metric_type = 'update_check'", n.ID,
			).Scan(&updateData)
			if err == nil {
				var ui UpdateInfo
				if json.Unmarshal(updateData, &ui) == nil {
					n.UpdateInfo = &ui
				}
			}
			nodes = append(nodes, n)
		}
	}

	writeJSON(w, map[string]interface{}{
		"nodes":      nodes,
		"node_count": len(nodes),
	})
}

// getLocalOverviewMetrics fetches overview metrics from Prometheus for the local node,
// using the same queries as handleMetricsOverview so the format matches agent responses.
func (s *Server) getLocalOverviewMetrics(ctx context.Context) *json.RawMessage {
	queries := map[string]string{
		"qps":              `sum(rate(kresd_query_total[1m]))`,
		"avg_latency_ms":   `histogram_quantile(0.5, rate(kresd_answer_duration_seconds_bucket[5m])) * 1000`,
		"cache_hit_ratio":  `rate(kresd_cache_hit_total[5m]) / (rate(kresd_cache_hit_total[5m]) + rate(kresd_cache_miss_total[5m]))`,
		"dnssec_secure_pct": `rate(kresd_answer_total{dnssec="secure"}[5m]) / rate(kresd_answer_total[5m]) * 100`,
	}

	results := map[string]json.RawMessage{}
	for name, query := range queries {
		data, err := s.promInstantQuery(query)
		if err != nil {
			continue
		}
		results[name] = data
	}

	if len(results) == 0 {
		return nil
	}
	raw, _ := json.Marshal(results)
	msg := json.RawMessage(raw)
	return &msg
}

// --- Remote update proxy ---

func (s *Server) handlePushNodeUpdate(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.Atoi(chi.URLParam(r, "id"))

	var domain, token string
	err := s.pg.QueryRow(r.Context(),
		"SELECT domain, api_token FROM cluster_nodes WHERE id = $1", id,
	).Scan(&domain, &token)
	if err != nil {
		http.Error(w, `{"error":"node not found"}`, http.StatusNotFound)
		return
	}

	s.proxyAgentSSE(w, r, domain, token, "/update/execute")

	// Clear cached update check so overview reflects up-to-date state
	s.pg.Exec(context.Background(),
		"DELETE FROM cluster_metrics_cache WHERE node_id = $1 AND metric_type = 'update_check'", id)
}

func (s *Server) handlePushUpdateAll(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, `{"error":"streaming not supported"}`, http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")

	rows, err := s.pg.Query(r.Context(),
		"SELECT id, name, domain, api_token FROM cluster_nodes ORDER BY id")
	if err != nil {
		fmt.Fprintf(w, "event: error\ndata: query failed\n\n")
		flusher.Flush()
		return
	}
	defer rows.Close()

	type nodeInfo struct {
		id     int
		name   string
		domain string
		token  string
	}
	var nodes []nodeInfo
	for rows.Next() {
		var n nodeInfo
		rows.Scan(&n.id, &n.name, &n.domain, &n.token)
		nodes = append(nodes, n)
	}

	for i, n := range nodes {
		fmt.Fprintf(w, "data: === Updating %s (%s) [%d/%d] ===\n\n", n.name, n.domain, i+1, len(nodes))
		flusher.Flush()

		// Trigger update on agent — stream client, or the 30s ceiling cuts
		// every update mid-flight (see agentStreamClient).
		resp, err := s.doAgentRequestBody(r.Context(), agentStreamClient, "POST", n.domain, n.token, "/update/execute", nil, "")
		if err != nil {
			fmt.Fprintf(w, "data: [ERROR] Failed to connect: %s\n\n", err.Error())
			flusher.Flush()
			continue
		}

		// Stream SSE from agent
		scanner := bufio.NewScanner(resp.Body)
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "data: ") || strings.HasPrefix(line, "event: ") {
				fmt.Fprintf(w, "%s\n", line)
				if strings.HasPrefix(line, "data: ") {
					fmt.Fprintf(w, "\n")
				}
			}
			flusher.Flush()
		}
		resp.Body.Close()

		fmt.Fprintf(w, "data: === Done: %s ===\n\n", n.name)
		flusher.Flush()
	}

	fmt.Fprintf(w, "event: done\ndata: All nodes updated\n\n")
	flusher.Flush()
}

// --- Proxy: node cleanup ---

func (s *Server) handleProxyNodeCleanupInfo(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.Atoi(chi.URLParam(r, "id"))
	var domain, token string
	err := s.pg.QueryRow(r.Context(), "SELECT domain, api_token FROM cluster_nodes WHERE id = $1", id).Scan(&domain, &token)
	if err != nil {
		http.Error(w, `{"error":"node not found"}`, http.StatusNotFound)
		return
	}
	data, err := s.fetchAgentEndpoint(domain, token, "/docker/cleanup")
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err.Error()), http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(data)
}

func (s *Server) handleProxyNodeCleanup(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.Atoi(chi.URLParam(r, "id"))
	var domain, token string
	err := s.pg.QueryRow(r.Context(), "SELECT domain, api_token FROM cluster_nodes WHERE id = $1", id).Scan(&domain, &token)
	if err != nil {
		http.Error(w, `{"error":"node not found"}`, http.StatusNotFound)
		return
	}
	resp, err := s.doAgentRequest("POST", domain, token, "/docker/cleanup")
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err.Error()), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	w.Header().Set("Content-Type", "application/json")
	w.Write(data)
}

// getLocalSystemMetrics fetches CPU/memory/disk metrics from Prometheus for the local node.
func (s *Server) getLocalSystemMetrics(ctx context.Context) *json.RawMessage {
	queries := map[string]string{
		"cpu_usage":      `100 - (avg(rate(node_cpu_seconds_total{mode="idle"}[5m])) * 100)`,
		"memory_used":    `node_memory_MemTotal_bytes - node_memory_MemAvailable_bytes`,
		"memory_total":   `node_memory_MemTotal_bytes`,
		"disk_usage_pct": `100 - ((node_filesystem_avail_bytes{mountpoint="/"} / node_filesystem_size_bytes{mountpoint="/"}) * 100)`,
	}

	results := map[string]json.RawMessage{}
	for name, query := range queries {
		data, err := s.promInstantQuery(query)
		if err != nil {
			continue
		}
		results[name] = data
	}
	if len(results) == 0 {
		return nil
	}
	raw, _ := json.Marshal(results)
	msg := json.RawMessage(raw)
	return &msg
}

// --- Background poller (controller) ---

func (s *Server) runPoller(ctx context.Context) {
	log.Println("Cluster poller started")
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	updateCheckTicker := time.NewTicker(5 * time.Minute)
	defer updateCheckTicker.Stop()

	// Poll immediately on start
	s.pollAllNodes()
	s.pollUpdateChecks()

	for {
		select {
		case <-ctx.Done():
			log.Println("Cluster poller stopped")
			return
		case <-ticker.C:
			s.pollAllNodes()
		case <-updateCheckTicker.C:
			s.pollUpdateChecks()
		}
	}
}

func (s *Server) pollUpdateChecks() {
	// Check local node
	s.pollLocalUpdateCheck()

	// Check remote agent nodes
	ctx := context.Background()
	rows, err := s.pg.Query(ctx, "SELECT id, domain, api_token FROM cluster_nodes")
	if err != nil {
		return
	}
	defer rows.Close()

	type node struct {
		id     int
		domain string
		token  string
	}
	var nodes []node
	for rows.Next() {
		var n node
		rows.Scan(&n.id, &n.domain, &n.token)
		nodes = append(nodes, n)
	}

	for _, n := range nodes {
		if updateData, err := s.fetchAgentEndpoint(n.domain, n.token, "/update/check"); err == nil {
			s.pg.Exec(ctx,
				`INSERT INTO cluster_metrics_cache (node_id, metric_type, data, fetched_at)
				 VALUES ($1, 'update_check', $2, NOW())
				 ON CONFLICT (node_id, metric_type) DO UPDATE SET data = $2, fetched_at = NOW()`,
				n.id, updateData)
		}
	}
}

func (s *Server) pollLocalUpdateCheck() {
	projectDir := s.cfg.ProjectDir

	// git fetch origin (ignore errors for offline)
	fetchCmd := exec.Command("git", "fetch", "origin")
	fetchCmd.Dir = projectDir
	fetchCmd.Run()

	resp := map[string]interface{}{
		"update_available": false,
		"commits_behind":   0,
		"commit_log":       []string{},
	}

	// commits behind
	countCmd := exec.Command("git", "rev-list", "--count", "HEAD..origin/main")
	countCmd.Dir = projectDir
	if out, err := countCmd.Output(); err == nil {
		var count int
		fmt.Sscanf(strings.TrimSpace(string(out)), "%d", &count)
		resp["commits_behind"] = count
		resp["update_available"] = count > 0

		if count > 0 {
			logCmd := exec.Command("git", "log", "--oneline", "HEAD..origin/main")
			logCmd.Dir = projectDir
			if logOut, err := logCmd.Output(); err == nil && len(logOut) > 0 {
				resp["commit_log"] = strings.Split(strings.TrimSpace(string(logOut)), "\n")
			}
		}
	}

	data, _ := json.Marshal(resp)
	s.localUpdateCache.Store(json.RawMessage(data))
}

func (s *Server) pollAllNodes() {
	ctx := context.Background()
	rows, err := s.pg.Query(ctx,
		"SELECT id, domain, api_token FROM cluster_nodes")
	if err != nil {
		return
	}
	defer rows.Close()

	type node struct {
		id     int
		domain string
		token  string
	}
	var nodes []node
	for rows.Next() {
		var n node
		rows.Scan(&n.id, &n.domain, &n.token)
		nodes = append(nodes, n)
	}

	var wg sync.WaitGroup
	for _, n := range nodes {
		wg.Add(1)
		go func(n node) {
			defer wg.Done()
			s.pollNode(ctx, n.id, n.domain, n.token)
		}(n)
	}
	wg.Wait()
}

func (s *Server) pollNode(ctx context.Context, id int, domain, token string) {
	// Fetch health
	healthData, err := s.fetchAgentEndpoint(domain, token, "/health")
	if err != nil {
		s.pg.Exec(ctx,
			"UPDATE cluster_nodes SET status = 'offline', last_error = $1, updated_at = NOW() WHERE id = $2",
			err.Error(), id)
		return
	}

	// Parse health status
	var health struct {
		Status string `json:"status"`
	}
	json.Unmarshal(healthData, &health)

	status := "online"
	if health.Status == "degraded" {
		status = "degraded"
	}

	// Fetch version
	var version string
	if versionData, err := s.fetchAgentEndpoint(domain, token, "/version"); err == nil {
		var v struct {
			Version string `json:"version"`
		}
		json.Unmarshal(versionData, &v)
		version = v.Version
	}

	// Update node status
	s.pg.Exec(ctx,
		"UPDATE cluster_nodes SET status = $1, version = $2, last_seen_at = NOW(), last_error = '', updated_at = NOW() WHERE id = $3",
		status, version, id)

	// Fetch and cache overview metrics
	if overviewData, err := s.fetchAgentEndpoint(domain, token, "/metrics/overview"); err == nil {
		s.pg.Exec(ctx,
			`INSERT INTO cluster_metrics_cache (node_id, metric_type, data, fetched_at)
			 VALUES ($1, 'overview', $2, NOW())
			 ON CONFLICT (node_id, metric_type) DO UPDATE SET data = $2, fetched_at = NOW()`,
			id, overviewData)
	}

	// Fetch and cache system metrics
	if systemData, err := s.fetchAgentEndpoint(domain, token, "/metrics/system"); err == nil {
		s.pg.Exec(ctx,
			`INSERT INTO cluster_metrics_cache (node_id, metric_type, data, fetched_at)
			 VALUES ($1, 'system', $2, NOW())
			 ON CONFLICT (node_id, metric_type) DO UPDATE SET data = $2, fetched_at = NOW()`,
			id, systemData)
	}

}

// --- Helper: fetch from agent ---

// agentClient is for short request/response calls: a hard 30s ceiling is right.
var agentClient = &http.Client{Timeout: 30 * time.Second}

// agentStreamClient is for SSE streams (updates, RPZ syncs) that legitimately
// run for many minutes. http.Client.Timeout covers the ENTIRE exchange
// including reading the body, so the 30s client used to kill every remote
// update stream at the 30-second mark while the update kept running blind on
// the agent. Bound only the connection phases; stream lifetime is bounded by
// the caller's request context instead.
var agentStreamClient = &http.Client{
	Transport: &http.Transport{
		DialContext:           (&net.Dialer{Timeout: 10 * time.Second}).DialContext,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 20 * time.Second,
	},
}

func (s *Server) fetchAgentEndpoint(domain, token, endpoint string) ([]byte, error) {
	resp, err := s.doAgentRequest("GET", domain, token, endpoint)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("agent returned %d", resp.StatusCode)
	}

	return io.ReadAll(resp.Body)
}

func (s *Server) doAgentRequest(method, domain, token, endpoint string) (*http.Response, error) {
	return s.doAgentRequestBody(context.Background(), agentClient, method, domain, token, endpoint, nil, "")
}

func (s *Server) doAgentRequestBody(ctx context.Context, client *http.Client, method, domain, token, endpoint string, body io.Reader, contentType string) (*http.Response, error) {
	url := fmt.Sprintf("https://%s/api/cluster/agent%s", domain, endpoint)
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Cluster-Token", token)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	return client.Do(req)
}

func (s *Server) proxyAgentSSE(w http.ResponseWriter, r *http.Request, domain, token, endpoint string) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, `{"error":"streaming not supported"}`, http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")

	// Stream client + caller context: lives as long as the browser keeps the
	// connection open, dies with it.
	resp, err := s.doAgentRequestBody(r.Context(), agentStreamClient, "POST", domain, token, endpoint, nil, "")
	if err != nil {
		fmt.Fprintf(w, "event: error\ndata: %s\n\n", err.Error())
		flusher.Flush()
		return
	}
	defer resp.Body.Close()

	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		fmt.Fprintf(w, "%s\n", line)
		if strings.HasPrefix(line, "data: ") || line == "" {
			flusher.Flush()
		}
	}
	flusher.Flush()
}

// --- Proxy: operational levers (services / RPZ / fleet probe) ---

// nodeConn looks up the domain and token for a registered agent node.
func (s *Server) nodeConn(r *http.Request) (domain, token string, ok bool) {
	id, _ := strconv.Atoi(chi.URLParam(r, "id"))
	err := s.pg.QueryRow(r.Context(),
		"SELECT domain, api_token FROM cluster_nodes WHERE id = $1", id,
	).Scan(&domain, &token)
	return domain, token, err == nil
}

// proxyAgentGET forwards a simple GET to the agent and relays the JSON reply.
func (s *Server) proxyAgentGET(w http.ResponseWriter, r *http.Request, endpoint string) {
	domain, token, ok := s.nodeConn(r)
	if !ok {
		http.Error(w, `{"error":"node not found"}`, http.StatusNotFound)
		return
	}
	data, err := s.fetchAgentEndpoint(domain, token, endpoint)
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err.Error()), http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(data)
}

func (s *Server) handleProxyNodeServices(w http.ResponseWriter, r *http.Request) {
	s.proxyAgentGET(w, r, "/services")
}

func (s *Server) handleProxyNodeRPZConfig(w http.ResponseWriter, r *http.Request) {
	s.proxyAgentGET(w, r, "/rpz/config")
}

func (s *Server) handleProxyNodeRPZStats(w http.ResponseWriter, r *http.Request) {
	s.proxyAgentGET(w, r, "/rpz/stats")
}

func (s *Server) handleProxyNodeFleetProbe(w http.ResponseWriter, r *http.Request) {
	s.proxyAgentGET(w, r, "/fleet/probe")
}

func (s *Server) handleProxyNodeResolverInfo(w http.ResponseWriter, r *http.Request) {
	s.proxyAgentGET(w, r, "/resolver/info")
}

func (s *Server) handleProxyNodeServiceRestart(w http.ResponseWriter, r *http.Request) {
	domain, token, ok := s.nodeConn(r)
	if !ok {
		http.Error(w, `{"error":"node not found"}`, http.StatusNotFound)
		return
	}
	// Forward the {"service": "..."} body as-is; the agent validates the name
	// against its own allowlist.
	body := http.MaxBytesReader(w, r.Body, 4096)
	resp, err := s.doAgentRequestBody(r.Context(), agentClient, "POST", domain, token, "/services/restart", body, "application/json")
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err.Error()), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	w.Write(data)
}

func (s *Server) handleProxyNodeRPZSync(w http.ResponseWriter, r *http.Request) {
	domain, token, ok := s.nodeConn(r)
	if !ok {
		http.Error(w, `{"error":"node not found"}`, http.StatusNotFound)
		return
	}
	s.proxyAgentSSE(w, r, domain, token, "/rpz/sync")
}
