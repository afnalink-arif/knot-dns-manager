package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

type ServerConfig struct {
	Timezone              string   `json:"timezone"`
	AllowedSubnets        []string `json:"allowed_subnets"`
	RetentionQueryDays    int      `json:"retention_query_days"`
	RetentionMetricsDays  int      `json:"retention_metrics_days"`
}

func (s *Server) getServerTimezone() string {
	var tz string
	err := s.pg.QueryRow(context.Background(),
		`SELECT timezone FROM server_config WHERE id = 1`).Scan(&tz)
	if err != nil || tz == "" {
		return "Asia/Jakarta"
	}
	return tz
}

// getAllowedSubnets returns subnets from DB, or seeds from kresd config on first use.
func (s *Server) getAllowedSubnets() []string {
	var raw string
	s.pg.QueryRow(context.Background(),
		`SELECT allowed_subnets FROM server_config WHERE id = 1`).Scan(&raw)

	if raw != "" {
		var subnets []string
		for _, sub := range strings.Split(raw, ",") {
			sub = strings.TrimSpace(sub)
			if sub != "" {
				subnets = append(subnets, sub)
			}
		}
		return subnets
	}

	// Seed from kresd config on first use
	subnets := parseSubnetsFromKresdConfig(filepath.Join(s.cfg.ProjectDir, "config/kresd/config.yaml"))
	if len(subnets) > 0 {
		s.pg.Exec(context.Background(),
			"UPDATE server_config SET allowed_subnets = $1 WHERE id = 1",
			strings.Join(subnets, ","))
	}
	return subnets
}

// parseSubnetsFromKresdConfig extracts user-managed subnets from kresd views config.
// Skips internal subnets (localhost, docker, catch-all).
var internalSubnets = map[string]bool{
	"127.0.0.0/8":  true,
	"::1/128":      true,
	"172.16.0.0/12": true,
	"0.0.0.0/0":   true,
	"::/0":         true,
}

func parseSubnetsFromKresdConfig(path string) []string {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}

	var subnets []string
	re := regexp.MustCompile(`subnets:\s*\[([^\]]+)\]`)
	for _, match := range re.FindAllStringSubmatch(string(data), -1) {
		inner := match[1]
		for _, part := range strings.Split(inner, ",") {
			part = strings.TrimSpace(part)
			part = strings.Trim(part, "'\"")
			if part != "" && !internalSubnets[part] {
				subnets = append(subnets, part)
			}
		}
	}
	return subnets
}

func (s *Server) handleGetServerConfig(w http.ResponseWriter, r *http.Request) {
	cfg := ServerConfig{
		Timezone:             "Asia/Jakarta",
		AllowedSubnets:       []string{},
		RetentionQueryDays:   30,
		RetentionMetricsDays: 15,
	}

	s.pg.QueryRow(r.Context(),
		`SELECT timezone, retention_query_days, retention_metrics_days FROM server_config WHERE id = 1`,
	).Scan(&cfg.Timezone, &cfg.RetentionQueryDays, &cfg.RetentionMetricsDays)

	cfg.AllowedSubnets = s.getAllowedSubnets()
	if cfg.AllowedSubnets == nil {
		cfg.AllowedSubnets = []string{}
	}

	writeJSON(w, cfg)
}

func (s *Server) handleUpdateServerConfig(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Timezone             *string  `json:"timezone"`
		AllowedSubnets       []string `json:"allowed_subnets"`
		RetentionQueryDays   *int     `json:"retention_query_days"`
		RetentionMetricsDays *int     `json:"retention_metrics_days"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid request"}`, http.StatusBadRequest)
		return
	}

	ctx := r.Context()
	s.pg.Exec(ctx, `INSERT INTO server_config (id) VALUES (1) ON CONFLICT DO NOTHING`)

	if req.Timezone != nil {
		tz := *req.Timezone
		if !isValidTimezone(tz) {
			http.Error(w, `{"error":"invalid timezone"}`, http.StatusBadRequest)
			return
		}
		s.pg.Exec(ctx, "UPDATE server_config SET timezone = $1, updated_at = NOW() WHERE id = 1", tz)
	}

	if req.AllowedSubnets != nil {
		subnets := strings.Join(req.AllowedSubnets, ",")
		s.pg.Exec(ctx, "UPDATE server_config SET allowed_subnets = $1, updated_at = NOW() WHERE id = 1", subnets)

		// Regenerate kresd config with new subnets and restart
		rpzCfg := s.getRPZConfig()
		s.regenerateKresdConfig(rpzCfg.Enabled)
		s.restartKresdVerified()
		log.Printf("Allowed subnets updated: %s", subnets)
	}

	if req.RetentionQueryDays != nil {
		days := *req.RetentionQueryDays
		if days < 1 { days = 1 }
		if days > 365 { days = 365 }
		s.pg.Exec(ctx, "UPDATE server_config SET retention_query_days = $1, updated_at = NOW() WHERE id = 1", days)

		// Update ClickHouse TTL
		alterTTL := fmt.Sprintf("ALTER TABLE dnsmonitor.dns_queries MODIFY TTL toDate(timestamp) + INTERVAL %d DAY", days)
		s.ch.Exec(ctx, alterTTL)
		log.Printf("ClickHouse query retention updated to %d days", days)
	}

	if req.RetentionMetricsDays != nil {
		days := *req.RetentionMetricsDays
		if days < 1 { days = 1 }
		if days > 365 { days = 365 }
		s.pg.Exec(ctx, "UPDATE server_config SET retention_metrics_days = $1, updated_at = NOW() WHERE id = 1", days)

		// Update Prometheus retention via docker-compose restart with new flag
		// Prometheus reads retention from command-line args, so we update docker-compose.yml
		composeFile := filepath.Join(s.cfg.ProjectDir, "docker-compose.yml")
		updatePrometheusRetention(composeFile, days)
		if name := findContainerName("prometheus"); name != "" {
			exec.Command("docker", "restart", name).Run()
		}
		log.Printf("Prometheus retention updated to %d days", days)
	}

	writeJSON(w, map[string]string{"message": "Server config updated"})
}

// updatePrometheusRetention modifies the retention.time flag in docker-compose.yml
func updatePrometheusRetention(composePath string, days int) {
	data, err := os.ReadFile(composePath)
	if err != nil {
		log.Printf("Failed to read docker-compose.yml: %v", err)
		return
	}
	content := string(data)
	re := regexp.MustCompile(`--storage\.tsdb\.retention\.time=\d+d`)
	content = re.ReplaceAllString(content, fmt.Sprintf("--storage.tsdb.retention.time=%dd", days))
	os.WriteFile(composePath, []byte(content), 0644)
}

var validTimezones = map[string]bool{
	"Asia/Jakarta":         true,
	"Asia/Makassar":        true,
	"Asia/Jayapura":        true,
	"Asia/Singapore":       true,
	"Asia/Tokyo":           true,
	"Asia/Kolkata":         true,
	"Asia/Shanghai":        true,
	"Asia/Dubai":           true,
	"Europe/London":        true,
	"Europe/Berlin":        true,
	"America/New_York":     true,
	"America/Chicago":      true,
	"America/Denver":       true,
	"America/Los_Angeles":  true,
	"Pacific/Auckland":     true,
	"Australia/Sydney":     true,
	"UTC":                  true,
}

func isValidTimezone(tz string) bool {
	return validTimezones[tz]
}
