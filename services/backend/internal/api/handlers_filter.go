package api

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
)

type FilterRule struct {
	ID        int       `json:"id"`
	Domain    string    `json:"domain"`
	Action    string    `json:"action"`
	Category  string    `json:"category"`
	Enabled   bool      `json:"enabled"`
	CreatedAt time.Time `json:"created_at"`
}

type FilterList struct {
	ID          int        `json:"id"`
	Name        string     `json:"name"`
	URL         string     `json:"url"`
	Category    string     `json:"category"`
	Enabled     bool       `json:"enabled"`
	DomainCount int        `json:"domain_count"`
	LastUpdated *time.Time `json:"last_updated"`
	CreatedAt   time.Time  `json:"created_at"`
}

// handleListFilters returns all filter rules and lists
func (s *Server) handleListFilters(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Get rules
	rules := []FilterRule{}
	rows, err := s.pg.Query(ctx, "SELECT id, domain, action, category, enabled, created_at FROM filter_rules ORDER BY category, domain")
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var fr FilterRule
			rows.Scan(&fr.ID, &fr.Domain, &fr.Action, &fr.Category, &fr.Enabled, &fr.CreatedAt)
			rules = append(rules, fr)
		}
	}

	// Get lists
	lists := []FilterList{}
	lrows, err := s.pg.Query(ctx, "SELECT id, name, url, category, enabled, domain_count, last_updated, created_at FROM filter_lists ORDER BY name")
	if err == nil {
		defer lrows.Close()
		for lrows.Next() {
			var fl FilterList
			lrows.Scan(&fl.ID, &fl.Name, &fl.URL, &fl.Category, &fl.Enabled, &fl.DomainCount, &fl.LastUpdated, &fl.CreatedAt)
			lists = append(lists, fl)
		}
	}

	// Count stats
	var totalEnabled int
	s.pg.QueryRow(ctx, "SELECT count(*) FROM filter_rules WHERE enabled = true").Scan(&totalEnabled)

	writeJSON(w, map[string]interface{}{
		"rules":         rules,
		"lists":         lists,
		"total_rules":   len(rules),
		"total_enabled": totalEnabled,
	})
}

// handleAddFilter adds a single domain filter
func (s *Server) handleAddFilter(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Domain   string `json:"domain"`
		Action   string `json:"action"`
		Category string `json:"category"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Domain == "" {
		http.Error(w, `{"error":"domain required"}`, http.StatusBadRequest)
		return
	}

	// Clean domain
	req.Domain = strings.ToLower(strings.TrimSpace(req.Domain))
	req.Domain = strings.TrimPrefix(req.Domain, "http://")
	req.Domain = strings.TrimPrefix(req.Domain, "https://")
	req.Domain = strings.TrimPrefix(req.Domain, "www.")
	req.Domain = strings.TrimSuffix(req.Domain, "/")

	if req.Action == "" {
		req.Action = "block"
	}
	if req.Category == "" {
		req.Category = "custom"
	}

	var id int
	err := s.pg.QueryRow(r.Context(),
		"INSERT INTO filter_rules (domain, action, category) VALUES ($1, $2, $3) RETURNING id",
		req.Domain, req.Action, req.Category,
	).Scan(&id)
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err.Error()), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusCreated)
	writeJSON(w, map[string]interface{}{"id": id, "domain": req.Domain, "message": "rule added"})
}

// handleDeleteFilter removes a filter rule
func (s *Server) handleDeleteFilter(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.Atoi(chi.URLParam(r, "id"))
	s.pg.Exec(r.Context(), "DELETE FROM filter_rules WHERE id = $1", id)
	writeJSON(w, map[string]string{"message": "rule deleted"})
}

// handleToggleFilter enables/disables a filter rule
func (s *Server) handleToggleFilter(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.Atoi(chi.URLParam(r, "id"))
	s.pg.Exec(r.Context(), "UPDATE filter_rules SET enabled = NOT enabled WHERE id = $1", id)
	writeJSON(w, map[string]string{"message": "rule toggled"})
}

// handleImportList imports domains from a hosts-format URL or bulk text
func (s *Server) handleImportList(w http.ResponseWriter, r *http.Request) {
	var req struct {
		URL      string `json:"url"`
		Domains  string `json:"domains"`
		Category string `json:"category"`
		Name     string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid request"}`, http.StatusBadRequest)
		return
	}
	if req.Category == "" {
		req.Category = "imported"
	}

	var domains []string

	if req.URL != "" {
		// Fetch external blocklist
		resp, err := s.httpClient.Get(req.URL)
		if err != nil {
			http.Error(w, fmt.Sprintf(`{"error":"fetch failed: %s"}`, err.Error()), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		domains = parseHostsList(resp.Body)

		// Save list reference
		if req.Name == "" {
			req.Name = req.URL
		}
		s.pg.Exec(r.Context(),
			`INSERT INTO filter_lists (name, url, category, domain_count, last_updated)
			 VALUES ($1, $2, $3, $4, NOW())
			 ON CONFLICT DO NOTHING`,
			req.Name, req.URL, req.Category, len(domains))
	} else if req.Domains != "" {
		// Parse bulk text input (one domain per line)
		scanner := bufio.NewScanner(strings.NewReader(req.Domains))
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line != "" && !strings.HasPrefix(line, "#") {
				d := cleanDomain(line)
				if d != "" {
					domains = append(domains, d)
				}
			}
		}
	} else {
		http.Error(w, `{"error":"url or domains required"}`, http.StatusBadRequest)
		return
	}

	// Bulk insert
	imported := 0
	for _, d := range domains {
		_, err := s.pg.Exec(r.Context(),
			"INSERT INTO filter_rules (domain, action, category) VALUES ($1, 'block', $2) ON CONFLICT DO NOTHING",
			d, req.Category)
		if err == nil {
			imported++
		}
	}

	writeJSON(w, map[string]interface{}{
		"imported": imported,
		"total":    len(domains),
		"message":  fmt.Sprintf("%d domains imported", imported),
	})
}

// handleFilterStats returns filtering statistics
func (s *Server) handleFilterStats(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	stats := map[string]interface{}{}

	var totalRules, enabledRules int
	s.pg.QueryRow(ctx, "SELECT count(*) FROM filter_rules").Scan(&totalRules)
	s.pg.QueryRow(ctx, "SELECT count(*) FROM filter_rules WHERE enabled = true").Scan(&enabledRules)

	// Category breakdown
	catRows, _ := s.pg.Query(ctx, "SELECT category, count(*) FROM filter_rules WHERE enabled = true GROUP BY category ORDER BY count(*) DESC")
	categories := []map[string]interface{}{}
	if catRows != nil {
		defer catRows.Close()
		for catRows.Next() {
			var cat string
			var cnt int
			catRows.Scan(&cat, &cnt)
			categories = append(categories, map[string]interface{}{"category": cat, "count": cnt})
		}
	}

	stats["total_rules"] = totalRules
	stats["enabled_rules"] = enabledRules
	stats["categories"] = categories

	writeJSON(w, stats)
}

// handleApplyFilters regenerates kresd config with blocklist and restarts
func (s *Server) handleApplyFilters(w http.ResponseWriter, r *http.Request) {
	rpzCfg := s.getRPZConfig()
	s.regenerateKresdConfig(rpzCfg.Enabled)
	s.restartKresdVerified()

	// Count custom domains
	var count int
	s.pg.QueryRow(r.Context(), "SELECT count(*) FROM filter_rules WHERE enabled = true AND action = 'block'").Scan(&count)

	log.Printf("Filter applied: %d custom domains + RPZ(%v), kresd restarted", count, rpzCfg.Enabled)
	writeJSON(w, map[string]interface{}{
		"message":         "filters applied",
		"domains_blocked": count,
		"rpz_enabled":     rpzCfg.Enabled,
		"rpz_domains":     rpzCfg.DomainCount,
	})
}

// loadEnvFile reads a .env file into a map
func loadEnvFile(path string) map[string]string {
	result := map[string]string{}
	f, err := os.Open(path)
	if err != nil {
		return result
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) == 2 {
			key := strings.TrimSpace(parts[0])
			val := strings.TrimSpace(parts[1])
			val = strings.Trim(val, `"'`)
			result[key] = val
		}
	}
	return result
}

// parseHostsList parses a hosts-format blocklist
func parseHostsList(r io.Reader) []string {
	var domains []string
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		d := cleanDomain(line)
		if d != "" {
			domains = append(domains, d)
		}
	}
	return domains
}

// cleanDomain extracts domain from hosts-format line (e.g., "0.0.0.0 ads.example.com")
func cleanDomain(line string) string {
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "#") {
		return ""
	}

	// Hosts format: "0.0.0.0 domain" or "127.0.0.1 domain"
	fields := strings.Fields(line)
	var domain string
	if len(fields) >= 2 && (fields[0] == "0.0.0.0" || fields[0] == "127.0.0.1") {
		domain = fields[1]
	} else if len(fields) == 1 {
		domain = fields[0]
	} else {
		return ""
	}

	domain = strings.ToLower(strings.TrimSpace(domain))
	domain = strings.TrimSuffix(domain, ".")

	// Skip invalid/meta entries
	if domain == "" || domain == "localhost" || domain == "broadcasthost" ||
		domain == "local" || strings.HasPrefix(domain, "#") ||
		!strings.Contains(domain, ".") {
		return ""
	}

	return domain
}
