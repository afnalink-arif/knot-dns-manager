package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
)

type AlertRule struct {
	ID             int       `json:"id"`
	Name           string    `json:"name"`
	Metric         string    `json:"metric"`
	Condition      string    `json:"condition"`
	Threshold      float64   `json:"threshold"`
	DurationSec    int       `json:"duration_sec"`
	Enabled        bool      `json:"enabled"`
	NotifyChannels []string  `json:"notify_channels"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

type AlertEvent struct {
	ID         int        `json:"id"`
	RuleID     int        `json:"rule_id"`
	RuleName   string     `json:"rule_name,omitempty"`
	Status     string     `json:"status"`
	Value      float64    `json:"value"`
	Message    string     `json:"message"`
	FiredAt    time.Time  `json:"fired_at"`
	ResolvedAt *time.Time `json:"resolved_at,omitempty"`
}

func (s *Server) handleListAlerts(w http.ResponseWriter, r *http.Request) {
	rows, err := s.pg.Query(r.Context(),
		`SELECT id, name, metric, condition, threshold, duration_sec, enabled, notify_channels, created_at, updated_at
		 FROM alert_rules ORDER BY created_at DESC`)
	if err != nil {
		http.Error(w, fmt.Sprintf("query error: %v", err), http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	var rules []AlertRule
	for rows.Next() {
		var rule AlertRule
		var channels []byte
		if err := rows.Scan(&rule.ID, &rule.Name, &rule.Metric, &rule.Condition,
			&rule.Threshold, &rule.DurationSec, &rule.Enabled, &channels,
			&rule.CreatedAt, &rule.UpdatedAt); err != nil {
			http.Error(w, fmt.Sprintf("scan error: %v", err), http.StatusInternalServerError)
			return
		}
		json.Unmarshal(channels, &rule.NotifyChannels)
		rules = append(rules, rule)
	}

	if rules == nil {
		rules = []AlertRule{}
	}
	writeJSON(w, rules)
}

func (s *Server) handleCreateAlert(w http.ResponseWriter, r *http.Request) {
	var rule AlertRule
	if err := json.NewDecoder(r.Body).Decode(&rule); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}

	channels, _ := json.Marshal(rule.NotifyChannels)

	err := s.pg.QueryRow(r.Context(),
		`INSERT INTO alert_rules (name, metric, condition, threshold, duration_sec, enabled, notify_channels)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)
		 RETURNING id, created_at, updated_at`,
		rule.Name, rule.Metric, rule.Condition, rule.Threshold,
		rule.DurationSec, rule.Enabled, channels,
	).Scan(&rule.ID, &rule.CreatedAt, &rule.UpdatedAt)

	if err != nil {
		http.Error(w, fmt.Sprintf("insert error: %v", err), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusCreated)
	writeJSON(w, rule)
}

func (s *Server) handleUpdateAlert(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(chi.URLParam(r, "id"))
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}

	var rule AlertRule
	if err := json.NewDecoder(r.Body).Decode(&rule); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}

	channels, _ := json.Marshal(rule.NotifyChannels)

	tag, err := s.pg.Exec(r.Context(),
		`UPDATE alert_rules SET name=$1, metric=$2, condition=$3, threshold=$4,
		 duration_sec=$5, enabled=$6, notify_channels=$7, updated_at=NOW()
		 WHERE id=$8`,
		rule.Name, rule.Metric, rule.Condition, rule.Threshold,
		rule.DurationSec, rule.Enabled, channels, id,
	)

	if err != nil {
		http.Error(w, fmt.Sprintf("update error: %v", err), http.StatusInternalServerError)
		return
	}

	if tag.RowsAffected() == 0 {
		http.Error(w, "alert not found", http.StatusNotFound)
		return
	}

	writeJSON(w, map[string]string{"status": "updated"})
}

func (s *Server) handleDeleteAlert(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(chi.URLParam(r, "id"))
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}

	tag, err := s.pg.Exec(r.Context(), `DELETE FROM alert_rules WHERE id=$1`, id)
	if err != nil {
		http.Error(w, fmt.Sprintf("delete error: %v", err), http.StatusInternalServerError)
		return
	}

	if tag.RowsAffected() == 0 {
		http.Error(w, "alert not found", http.StatusNotFound)
		return
	}

	writeJSON(w, map[string]string{"status": "deleted"})
}

func (s *Server) handleAlertHistory(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(queryOrDefault(r, "limit", "50"))

	rows, err := s.pg.Query(r.Context(),
		`SELECT e.id, COALESCE(e.rule_id, 0), COALESCE(r.name, 'system'), e.status, e.value, e.message, e.fired_at, e.resolved_at
		 FROM alert_events e
		 LEFT JOIN alert_rules r ON r.id = e.rule_id
		 ORDER BY e.fired_at DESC
		 LIMIT $1`, limit)
	if err != nil {
		http.Error(w, fmt.Sprintf("query error: %v", err), http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	var events []AlertEvent
	for rows.Next() {
		var e AlertEvent
		if err := rows.Scan(&e.ID, &e.RuleID, &e.RuleName, &e.Status,
			&e.Value, &e.Message, &e.FiredAt, &e.ResolvedAt); err != nil {
			http.Error(w, fmt.Sprintf("scan error: %v", err), http.StatusInternalServerError)
			return
		}
		events = append(events, e)
	}

	if events == nil {
		events = []AlertEvent{}
	}
	writeJSON(w, events)
}
