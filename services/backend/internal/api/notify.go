package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"
)

// ============================================================
// Notification config (Telegram) + system alerts
//
// alert_rules/alert_events existed as pure CRUD — nothing ever
// evaluated or delivered anything. This wires the two real needs:
//   - system alerts (fleet probe, serial drift, restart failure)
//     recorded in alert_events with rule_id NULL
//   - delivery via Telegram (helpdesk Komdigi also lives there)
// ============================================================

type NotifyConfig struct {
	TelegramEnabled  bool   `json:"telegram_enabled"`
	TelegramBotToken string `json:"telegram_bot_token"`
	TelegramChatID   string `json:"telegram_chat_id"`
}

func (s *Server) getNotifyConfig() NotifyConfig {
	var cfg NotifyConfig
	s.pg.QueryRow(context.Background(),
		`SELECT telegram_enabled, telegram_bot_token, telegram_chat_id
		 FROM notify_config WHERE id = 1`,
	).Scan(&cfg.TelegramEnabled, &cfg.TelegramBotToken, &cfg.TelegramChatID)
	return cfg
}

func (s *Server) handleGetNotifyConfig(w http.ResponseWriter, r *http.Request) {
	cfg := s.getNotifyConfig()
	// Never echo the full token back to the browser
	if len(cfg.TelegramBotToken) > 8 {
		cfg.TelegramBotToken = cfg.TelegramBotToken[:8] + "..."
	}
	writeJSON(w, cfg)
}

func (s *Server) handleUpdateNotifyConfig(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TelegramEnabled  *bool   `json:"telegram_enabled"`
		TelegramBotToken *string `json:"telegram_bot_token"`
		TelegramChatID   *string `json:"telegram_chat_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid request"}`, http.StatusBadRequest)
		return
	}
	ctx := r.Context()
	s.pg.Exec(ctx, `INSERT INTO notify_config (id) VALUES (1) ON CONFLICT DO NOTHING`)
	if req.TelegramEnabled != nil {
		s.pg.Exec(ctx, `UPDATE notify_config SET telegram_enabled=$1, updated_at=NOW() WHERE id=1`, *req.TelegramEnabled)
	}
	// A masked token ("12345678...") coming back from the UI must not overwrite the real one
	if req.TelegramBotToken != nil && !strings.HasSuffix(*req.TelegramBotToken, "...") {
		s.pg.Exec(ctx, `UPDATE notify_config SET telegram_bot_token=$1, updated_at=NOW() WHERE id=1`, strings.TrimSpace(*req.TelegramBotToken))
	}
	if req.TelegramChatID != nil {
		s.pg.Exec(ctx, `UPDATE notify_config SET telegram_chat_id=$1, updated_at=NOW() WHERE id=1`, strings.TrimSpace(*req.TelegramChatID))
	}
	writeJSON(w, map[string]string{"message": "notify config updated"})
}

func (s *Server) handleTestNotify(w http.ResponseWriter, r *http.Request) {
	host := s.serverLabel()
	if err := s.notifyTelegram(fmt.Sprintf("✅ Test notifikasi dari %s — konfigurasi Telegram bekerja.", host)); err != nil {
		writeJSON(w, map[string]string{"status": "error", "error": err.Error()})
		return
	}
	writeJSON(w, map[string]string{"status": "sent"})
}

// serverLabel identifies this node in outgoing notifications.
func (s *Server) serverLabel() string {
	env := loadEnvFile(s.cfg.ProjectDir + "/.env")
	if d := env["DOMAIN"]; d != "" {
		return d
	}
	return "kresd-manager"
}

// notifyTelegram sends msg via the Bot API. Returns an error the caller may
// log or surface; delivery failure must never break the calling flow.
func (s *Server) notifyTelegram(msg string) error {
	cfg := s.getNotifyConfig()
	if !cfg.TelegramEnabled || cfg.TelegramBotToken == "" || cfg.TelegramChatID == "" {
		return fmt.Errorf("telegram not configured/enabled")
	}
	body, _ := json.Marshal(map[string]string{
		"chat_id": cfg.TelegramChatID,
		"text":    msg,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://api.telegram.org/bot"+cfg.TelegramBotToken+"/sendMessage",
		bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("telegram API returned HTTP %d", resp.StatusCode)
	}
	return nil
}

// ------------------------------------------------------------
// System alerts: stateful fire/resolve keyed by alert kind.
// Debounce/dedup lives here so callers just report observations.
// ------------------------------------------------------------

type systemAlerts struct {
	mu     sync.Mutex
	firing map[string]time.Time // kind -> first fired
	renote map[string]time.Time // kind -> last notification
}

var sysAlerts = &systemAlerts{
	firing: map[string]time.Time{},
	renote: map[string]time.Time{},
}

// reNotifyInterval: while a condition stays firing, repeat the notification
// this often so a single missed message doesn't hide an ongoing outage.
const reNotifyInterval = 6 * time.Hour

// fireSystemAlert records + notifies a firing condition. Repeated calls for
// the same kind are deduplicated (re-notified every reNotifyInterval).
func (s *Server) fireSystemAlert(kind, msg string) {
	sysAlerts.mu.Lock()
	_, already := sysAlerts.firing[kind]
	lastNote := sysAlerts.renote[kind]
	now := time.Now()
	if !already {
		sysAlerts.firing[kind] = now
	}
	shouldNotify := !already || now.Sub(lastNote) >= reNotifyInterval
	if shouldNotify {
		sysAlerts.renote[kind] = now
	}
	sysAlerts.mu.Unlock()

	if !shouldNotify {
		return
	}
	log.Printf("ALERT [%s] %s", kind, msg)
	s.pg.Exec(context.Background(),
		`INSERT INTO alert_events (rule_id, status, value, message) VALUES (NULL, 'firing', 0, $1)`,
		fmt.Sprintf("[%s] %s", kind, msg))
	if err := s.notifyTelegram("🔴 " + s.serverLabel() + " — " + msg); err != nil {
		log.Printf("ALERT [%s] telegram delivery failed: %v", kind, err)
	}
}

// resolveSystemAlert clears a previously firing condition (no-op otherwise).
func (s *Server) resolveSystemAlert(kind, msg string) {
	sysAlerts.mu.Lock()
	_, wasFiring := sysAlerts.firing[kind]
	delete(sysAlerts.firing, kind)
	delete(sysAlerts.renote, kind)
	sysAlerts.mu.Unlock()

	if !wasFiring {
		return
	}
	log.Printf("ALERT RESOLVED [%s] %s", kind, msg)
	s.pg.Exec(context.Background(),
		`INSERT INTO alert_events (rule_id, status, value, message, resolved_at) VALUES (NULL, 'resolved', 0, $1, NOW())`,
		fmt.Sprintf("[%s] %s", kind, msg))
	if err := s.notifyTelegram("🟢 " + s.serverLabel() + " — " + msg); err != nil {
		log.Printf("ALERT [%s] telegram delivery failed: %v", kind, err)
	}
}
