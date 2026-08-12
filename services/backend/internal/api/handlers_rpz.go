package api

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// validRPZLabel matches a single valid DNS label for libknot:
// starts and ends with alphanumeric, contains only alphanumeric and hyphens
var validRPZLabel = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9-]*[a-zA-Z0-9])?$`)

func parseSizeMB(s string) int {
	s = strings.TrimSpace(strings.ToUpper(s))
	if strings.HasSuffix(s, "G") {
		v := 0
		fmt.Sscanf(strings.TrimSuffix(s, "G"), "%d", &v)
		return v * 1024
	}
	if strings.HasSuffix(s, "M") {
		v := 0
		fmt.Sscanf(strings.TrimSuffix(s, "M"), "%d", &v)
		return v
	}
	return 0
}

func formatSizeMB(mb int) string {
	if mb >= 1024 && mb%1024 == 0 {
		return fmt.Sprintf("%dG", mb/1024)
	}
	return fmt.Sprintf("%dM", mb)
}

func getTotalMemMB() int {
	out, err := exec.Command("free", "-m").Output()
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "Mem:") {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				v := 0
				fmt.Sscanf(fields[1], "%d", &v)
				return v
			}
		}
	}
	return 0
}

func resolveCacheSize(envCacheSize, projectDir string, rpzEnabled bool) string {
	cacheSize := envCacheSize
	if cacheSize == "" {
		cacheSize = "8G"
	}

	if cacheSize == "auto" {
		scriptPath := filepath.Join(projectDir, "calculate-cache-size.sh")
		if out, err := exec.Command("bash", "-c",
			fmt.Sprintf("source %s && calculate_cache_size", scriptPath)).Output(); err == nil {
			if v := strings.TrimSpace(string(out)); v != "" {
				cacheSize = v
			}
		} else {
			cacheSize = "4G"
		}
	}

	if !rpzEnabled {
		return cacheSize
	}

	mb := parseSizeMB(cacheSize)
	if mb <= 0 {
		return cacheSize
	}

	totalMemMB := getTotalMemMB()
	if totalMemMB <= 0 {
		return cacheSize
	}

	ruledbBudgetMB := 4096
	reserveMB := 2048
	maxCacheWithRPZ := totalMemMB - ruledbBudgetMB - reserveMB
	if maxCacheWithRPZ < 256 {
		maxCacheWithRPZ = 256
	}
	if maxCacheWithRPZ > 8192 {
		maxCacheWithRPZ = 8192
	}

	if mb > maxCacheWithRPZ {
		log.Printf("RPZ cache adjustment: %s -> %dM (RAM=%dM, ruledb=%dM, reserve=%dM)",
			cacheSize, maxCacheWithRPZ, totalMemMB, ruledbBudgetMB, reserveMB)
		return formatSizeMB(maxCacheWithRPZ)
	}

	return cacheSize
}

func restartKresdProper(projectDir string) {
	composeFile := filepath.Join(projectDir, "docker-compose.yml")

	// The backend runs inside the compose stack with the project mounted at
	// /project, so compose's directory-derived project name here is "project" —
	// not the real stack's. Without -p, stop/up silently target a phantom
	// "project" stack: kresd keeps running with the old ruledb while duplicate
	// project-* containers get created next to the real ones.
	compose := []string{"compose", "-f", composeFile}
	if project := detectProjectName(); project != "" {
		compose = append(compose, "-p", project)
	}

	exec.Command("docker", append(compose, "stop", "kresd", "dnstap-ingester")...).Run()

	exec.Command("docker", append(compose, "up", "-d", "dnstap-ingester")...).Run()

	time.Sleep(2 * time.Second)

	exec.Command("docker", append(compose, "up", "-d", "kresd")...).Run()

	log.Println("kresd restarted with proper sequence (dnstap-ingester first)")
}

// isValidRPZOwner validates an RPZ owner name for strict libknot compatibility.
// Each label must start/end with alphanumeric and contain only alphanumeric + hyphens.
// Underscores, special chars, and labels starting/ending with hyphens are rejected.
func isValidRPZOwner(owner string) bool {
	name := strings.TrimSuffix(owner, ".")
	if name == "" {
		return false
	}
	// Handle wildcard
	name = strings.TrimPrefix(name, "*.")
	labels := strings.Split(name, ".")
	if len(labels) < 2 { // need at least 2 labels (domain + zone suffix)
		return false
	}
	for _, l := range labels {
		if l == "" || !validRPZLabel.MatchString(l) {
			return false
		}
	}
	return true
}

// parseSOASerial extracts the serial from an SOA record line. Handles both the
// full record form ("zone. 900 IN SOA mname rname SERIAL ...") and dig +short
// output ("mname rname SERIAL ..."). Komdigi's serial is YYMMDDNN, e.g. 26081205.
func parseSOASerial(line string) uint64 {
	f := strings.Fields(line)
	idx := -1
	for i, tok := range f {
		if strings.EqualFold(tok, "SOA") {
			idx = i + 3 // skip SOA, mname, rname
			break
		}
	}
	if idx == -1 {
		idx = 2 // +short form: mname rname serial
	}
	if idx >= len(f) {
		return 0
	}
	serial, err := strconv.ParseUint(f[idx], 10, 64)
	if err != nil {
		return 0
	}
	return serial
}

// remoteZoneSerial asks a master for the zone's current serial over UDP — cheap
// (one packet) compared to the ~1.1GB AXFR it may let us skip.
func remoteZoneSerial(master, zone string) uint64 {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "dig", "SOA", zone, "@"+master,
		"+short", "+time=5", "+tries=2", "+noidnout").Output()
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(out), "\n") {
		if serial := parseSOASerial(line); serial != 0 {
			return serial
		}
	}
	return 0
}

// localZoneSerial reads the serial from the SOA at the head of the local zone file.
func localZoneSerial(path string) uint64 {
	head, err := readFileHead(path, 4096)
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(head, "\n") {
		if strings.Contains(strings.ToUpper(line), "SOA") {
			return parseSOASerial(line)
		}
	}
	return 0
}

// classifyAXFRError turns a failed sync into an actionable message.
//
// The two failure modes need opposite responses and must not be conflated:
//   - dig exits 9 ("no reply from server") when the master never answered —
//     unreachable host, blocked TCP/53, or a dead master. Registration is fine.
//   - a master that answers but returns only SOA/NS (a few hundred bytes) is
//     refusing the transfer — that is the "IP not registered/approved" case.
func classifyAXFRError(err error, sawRefusal bool) string {
	if sawRefusal {
		return "Zone transfer ditolak master (hanya SOA/NS yang dikirim) — IP server ini kemungkinan belum di-approve Komdigi. Cek status di s.komdigi.go.id/FormKoneksiRPZ"
	}
	if err != nil && strings.Contains(err.Error(), "exit status 9") {
		return "Tidak ada balasan dari master server (dig exit 9) — host unreachable, TCP/53 diblokir, atau master sudah tidak aktif. Ini bukan masalah registrasi IP; cek konektivitas dan daftar master server."
	}
	if err != nil {
		return fmt.Sprintf("Zone transfer gagal: %v", err)
	}
	return "Semua master server gagal memberikan zone data."
}

type RPZConfig struct {
	Enabled              bool       `json:"enabled"`
	MasterServers        string     `json:"master_servers"`
	ZoneName             string     `json:"zone_name"`
	LastSync             *time.Time `json:"last_sync"`
	LastSyncStatus       string     `json:"last_sync_status"`
	LastSyncError        string     `json:"last_sync_error"`
	DomainCount          int        `json:"domain_count"`
	FileSizeBytes        int64      `json:"file_size_bytes"`
	SyncDurationMs       int        `json:"sync_duration_ms"`
	AutoSyncEnabled      bool       `json:"auto_sync_enabled"`
	AutoSyncIntervalHrs  int        `json:"auto_sync_interval_hours"`
	AutoSyncHour         int        `json:"auto_sync_hour"`
	ZoneSerial           uint64     `json:"zone_serial"`
}

// komdigiMasters is the official RPZ master list from the Komdigi juknis
// (Dokumen Ver.260326). 103.154.123.130 was decommissioned — the juknis states
// "IP 103.154.123.130 sudah tidak digunakan" and it black-holes TCP/53, so keeping
// it in the list only burns the AXFR timeout and masks the real error (see
// classifyAXFRError).
const komdigiMasters = "139.255.196.202,182.23.79.202"

func (s *Server) getRPZConfig() RPZConfig {
	cfg := RPZConfig{
		MasterServers:       komdigiMasters,
		ZoneName:            "trustpositifkominfo",
		AutoSyncIntervalHrs: 24,
		AutoSyncHour:        2,
	}
	ctx := context.Background()
	s.pg.QueryRow(ctx,
		`SELECT enabled, master_servers, zone_name, last_sync, last_sync_status, last_sync_error,
		        domain_count, file_size_bytes, sync_duration_ms, auto_sync_enabled, auto_sync_interval_hours,
		        auto_sync_hour, zone_serial
		 FROM rpz_config WHERE id = 1`,
	).Scan(&cfg.Enabled, &cfg.MasterServers, &cfg.ZoneName, &cfg.LastSync, &cfg.LastSyncStatus,
		&cfg.LastSyncError, &cfg.DomainCount, &cfg.FileSizeBytes, &cfg.SyncDurationMs,
		&cfg.AutoSyncEnabled, &cfg.AutoSyncIntervalHrs, &cfg.AutoSyncHour, &cfg.ZoneSerial)
	return cfg
}

func (s *Server) handleGetRPZConfig(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.getRPZConfig())
}

func (s *Server) handleUpdateRPZConfig(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Enabled              *bool   `json:"enabled"`
		MasterServers        *string `json:"master_servers"`
		ZoneName             *string `json:"zone_name"`
		AutoSyncEnabled      *bool   `json:"auto_sync_enabled"`
		AutoSyncIntervalHrs  *int    `json:"auto_sync_interval_hours"`
		AutoSyncHour         *int    `json:"auto_sync_hour"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid request"}`, http.StatusBadRequest)
		return
	}

	ctx := r.Context()
	s.pg.Exec(ctx, `INSERT INTO rpz_config (id) VALUES (1) ON CONFLICT DO NOTHING`)

	if req.Enabled != nil {
		s.pg.Exec(ctx, "UPDATE rpz_config SET enabled = $1, updated_at = NOW() WHERE id = 1", *req.Enabled)
	}
	if req.MasterServers != nil {
		s.pg.Exec(ctx, "UPDATE rpz_config SET master_servers = $1, updated_at = NOW() WHERE id = 1", *req.MasterServers)
	}
	if req.ZoneName != nil {
		s.pg.Exec(ctx, "UPDATE rpz_config SET zone_name = $1, updated_at = NOW() WHERE id = 1", *req.ZoneName)
	}
	if req.AutoSyncEnabled != nil {
		s.pg.Exec(ctx, "UPDATE rpz_config SET auto_sync_enabled = $1, updated_at = NOW() WHERE id = 1", *req.AutoSyncEnabled)
	}
	if req.AutoSyncIntervalHrs != nil {
		hours := *req.AutoSyncIntervalHrs
		if hours < 1 {
			hours = 1
		}
		if hours > 168 {
			hours = 168
		}
		s.pg.Exec(ctx, "UPDATE rpz_config SET auto_sync_interval_hours = $1, updated_at = NOW() WHERE id = 1", hours)
	}
	if req.AutoSyncHour != nil {
		hour := *req.AutoSyncHour
		if hour < 0 {
			hour = 0
		}
		if hour > 23 {
			hour = 23
		}
		s.pg.Exec(ctx, "UPDATE rpz_config SET auto_sync_hour = $1, updated_at = NOW() WHERE id = 1", hour)
	}

	needRestart := req.Enabled != nil
	if needRestart {
		rpzCfg := s.getRPZConfig()
		s.regenerateKresdConfig(rpzCfg.Enabled)
		restartKresdProper(s.cfg.ProjectDir)
	}

	writeJSON(w, map[string]string{"message": "RPZ config updated"})
}

// handleRPZSync triggers an AXFR sync from the master servers.
// The zone file is saved directly for kresd's native policy.rpz() to load.
// This avoids converting 500K+ domains to local-data YAML records.
func (s *Server) handleRPZSync(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, `{"error":"streaming not supported"}`, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")

	sendEvent := func(msg string) {
		fmt.Fprintf(w, "data: %s\n\n", msg)
		flusher.Flush()
	}

	cfg := s.getRPZConfig()
	masters := strings.Split(cfg.MasterServers, ",")
	if len(masters) == 0 {
		sendEvent("[ERROR] No master servers configured")
		fmt.Fprintf(w, "event: error\ndata: no masters\n\n")
		flusher.Flush()
		return
	}

	sendEvent("[INFO] Starting RPZ zone transfer...")
	startTime := time.Now()

	// Try each master server — stream dig output to file to avoid holding all in memory
	rpzDir := filepath.Join(s.cfg.ProjectDir, "config", "kresd")
	rpzFile := filepath.Join(rpzDir, "rpz.zone")
	tmpFile := rpzFile + ".tmp"

	// Cleanup stale temp files from previous failed syncs + guaranteed cleanup on exit
	os.Remove(tmpFile)
	os.Remove(tmpFile + ".converted")
	defer os.Remove(tmpFile)
	defer os.Remove(tmpFile + ".converted")

	var usedMaster string
	var axfrErr error
	var sawRefusal bool

	for _, master := range masters {
		master = strings.TrimSpace(master)
		if master == "" {
			continue
		}
		sendEvent(fmt.Sprintf("[INFO] Trying AXFR from %s...", master))

		// Stream AXFR directly to file to avoid memory spike
		// Zone file is ~1.3GB so use a long timeout (30 min) and +tcp for large transfer
		axfrCtx, axfrCancel := context.WithTimeout(context.Background(), 30*time.Minute)
		cmd := exec.CommandContext(axfrCtx,
			"sh", "-c", fmt.Sprintf(
				"dig AXFR @%s %s +noidnout +tcp +time=300 +tries=2 +nocomments +nostats +nocmd > %s 2>/dev/null",
				master, cfg.ZoneName, tmpFile))

		// Monitor file size during download (progress updates)
		done := make(chan error, 1)
		go func() { done <- cmd.Run() }()

		progressTicker := time.NewTicker(5 * time.Second)
		defer progressTicker.Stop()
	axfrLoop:
		for {
			select {
			case axfrErr = <-done:
				break axfrLoop
			case <-progressTicker.C:
				if info, err := os.Stat(tmpFile); err == nil {
					sizeMB := float64(info.Size()) / 1024 / 1024
					elapsed := time.Since(startTime).Seconds()
					sendEvent(fmt.Sprintf("[INFO] Downloading... %.1f MB (%.0fs)", sizeMB, elapsed))
				}
			}
		}
		axfrCancel()

		// Verify we got real zone data (not just SOA/NS from unregistered IP)
		if info, err := os.Stat(tmpFile); err == nil && axfrErr == nil {
			fileSize := info.Size()
			if head, err := readFileHead(tmpFile, 1024); err == nil && strings.Contains(head, cfg.ZoneName) {
				// Check if we got meaningful data — just SOA+NS is typically < 1KB
				if fileSize > 10*1024 { // > 10KB = likely real data
					usedMaster = master
					sendEvent(fmt.Sprintf("[OK] Got %.1f MB from %s", float64(fileSize)/1024/1024, master))
					break
				}
				// Small response = master answered but refused the transfer (only SOA/NS)
				sawRefusal = true
				sendEvent(fmt.Sprintf("[WARN] %s returned only %d bytes — transfer ditolak (IP belum di-approve Komdigi?)", master, fileSize))
			} else if fileSize == 0 {
				sendEvent(fmt.Sprintf("[WARN] %s returned empty response", master))
			} else {
				sendEvent(fmt.Sprintf("[WARN] %s returned invalid zone data", master))
			}
		} else {
			sendEvent(fmt.Sprintf("[WARN] Failed from %s: %v", master, axfrErr))
		}
	}

	if usedMaster == "" {
		errMsg := classifyAXFRError(axfrErr, sawRefusal)
		sendEvent(fmt.Sprintf("[ERROR] %s", errMsg))
		os.Remove(tmpFile)
		s.updateRPZSyncStatus("error", errMsg, 0, 0, 0)
		fmt.Fprintf(w, "event: error\ndata: %s\n\n", errMsg)
		flusher.Flush()
		return
	}

	sendEvent(fmt.Sprintf("[OK] Zone transfer from %s successful", usedMaster))

	// Convert CNAME targets to "." for kresd compatibility
	// Komdigi uses "CNAME lamanlabuh.aduankonten.id." but kresd only supports "CNAME ."
	sendEvent("[INFO] Converting zone for kresd compatibility...")
	convertedFile := tmpFile + ".converted"
	redirectIP := loadEnvFile(filepath.Join(s.cfg.ProjectDir, ".env"))["SERVER_IP"]
	result, convertErr := convertRPZForKresd(tmpFile, convertedFile, redirectIP)
	if convertErr != nil {
		// Conversion failure (commonly disk-full mid-write) leaves a partial converted file
		// and a raw AXFR file that contains records kresd's parser will reject (escape
		// octal sequences, duplicate SOA, NS records). Publishing either breaks policy-loader.
		// Abort so the existing rpz.zone — known good from a previous successful sync — stays in place.
		errMsg := fmt.Sprintf("RPZ conversion failed: %v — keeping previous zone file", convertErr)
		sendEvent(fmt.Sprintf("[ERROR] %s", errMsg))
		os.Remove(convertedFile)
		s.updateRPZSyncStatus("error", errMsg, 0, 0, int(time.Since(startTime).Milliseconds()))
		fmt.Fprintf(w, "event: error\ndata: %s\n\n", errMsg)
		flusher.Flush()
		return
	}
	sendEvent(fmt.Sprintf("[OK] Converted %d records (%s)", result.converted, func() string {
		if redirectIP != "" {
			return fmt.Sprintf("A record redirect to %s", redirectIP)
		}
		return "NXDOMAIN"
	}()))
	if result.skipped > 0 {
		sendEvent(fmt.Sprintf("[INFO] Skipped %d entries with invalid domain names (non-ASCII, special chars)", result.skipped))
	}
	os.Remove(tmpFile)
	tmpFile = convertedFile

	// Count domains by scanning the file (streaming, not loading all into memory)
	sendEvent("[INFO] Counting domains in zone file...")
	domainCount := countRPZDomains(tmpFile, cfg.ZoneName)
	sendEvent(fmt.Sprintf("[OK] Found %d blocked domains", domainCount))

	// Safety check: verify converted file has real content before replacing
	tmpInfo, err := os.Stat(tmpFile)
	if err != nil || tmpInfo.Size() < 1024 {
		errMsg := fmt.Sprintf("Converted zone file too small (%d bytes) — aborting to protect existing zone", tmpInfo.Size())
		sendEvent(fmt.Sprintf("[ERROR] %s", errMsg))
		os.Remove(tmpFile)
		s.updateRPZSyncStatus("error", errMsg, 0, 0, int(time.Since(startTime).Milliseconds()))
		fmt.Fprintf(w, "event: error\ndata: %s\n\n", errMsg)
		flusher.Flush()
		return
	}

	// Atomic rename: tmp → final
	if err := os.Rename(tmpFile, rpzFile); err != nil {
		data, _ := os.ReadFile(tmpFile)
		if len(data) > 1024 { // only overwrite if we have real data
			os.WriteFile(rpzFile, data, 0644)
		}
		os.Remove(tmpFile)
	}

	duration := time.Since(startTime)
	fileInfo, _ := os.Stat(rpzFile)
	fileSize := int64(0)
	if fileInfo != nil {
		fileSize = fileInfo.Size()
	}

	s.updateRPZSyncStatus("success", "", domainCount, fileSize, int(duration.Milliseconds()))
	s.updateZoneSerial(rpzFile)

	sendEvent(fmt.Sprintf("[OK] Sync complete: %d domains, %.1f MB, %dms",
		domainCount, float64(fileSize)/1024/1024, duration.Milliseconds()))

	// If RPZ is enabled, regenerate kresd config (adds local-data.rpz YAML) and restart
	if cfg.Enabled {
		sendEvent("[INFO] Applying to DNS resolver via native RPZ (shared ruledb)...")
		s.regenerateKresdConfig(true)
		restartKresdProper(s.cfg.ProjectDir)
		sendEvent("[OK] DNS resolver restarted — policy-loader sedang memuat RPZ zone ke ruledb...")
		sendEvent(fmt.Sprintf("[INFO] Loading %d domain ke shared LMDB database. Proses ini berjalan di background.", domainCount))
	} else {
		sendEvent("[INFO] RPZ is disabled — zone file saved but not applied to resolver")
	}

	fmt.Fprintf(w, "event: done\ndata: sync complete\n\n")
	flusher.Flush()
}

func (s *Server) handleRPZStats(w http.ResponseWriter, r *http.Request) {
	cfg := s.getRPZConfig()

	rpzFile := filepath.Join(s.cfg.ProjectDir, "config", "kresd", "rpz.zone")
	fileExists := false
	var actualFileSize int64 = 0
	if info, err := os.Stat(rpzFile); err == nil {
		fileExists = true
		actualFileSize = info.Size()
	}

	if cfg.DomainCount == 0 && fileExists && actualFileSize > 100 {
		cfg.DomainCount = countRPZDomains(rpzFile, cfg.ZoneName)
		cfg.FileSizeBytes = actualFileSize
		s.pg.Exec(context.Background(),
			`UPDATE rpz_config SET domain_count = $1, file_size_bytes = $2 WHERE id = 1`,
			cfg.DomainCount, cfg.FileSizeBytes)
	}

	kresdMem := getKresdMemoryMB()

	writeJSON(w, map[string]interface{}{
		"config":          cfg,
		"file_exists":     fileExists,
		"actual_file_size": actualFileSize,
		"kresd_memory_mb": kresdMem,
	})
}

// updateZoneSerial records the serial of the zone file now on disk, so the next
// auto-sync can tell whether Komdigi has published anything new.
func (s *Server) updateZoneSerial(path string) {
	serial := localZoneSerial(path)
	if serial == 0 {
		return
	}
	s.pg.Exec(context.Background(), `UPDATE rpz_config SET zone_serial = $1 WHERE id = 1`, serial)
}

func (s *Server) updateRPZSyncStatus(status, errMsg string, domainCount int, fileSize int64, durationMs int) {
	ctx := context.Background()
	s.pg.Exec(ctx, `INSERT INTO rpz_config (id) VALUES (1) ON CONFLICT DO NOTHING`)
	if status == "error" {
		s.pg.Exec(ctx,
			`UPDATE rpz_config SET last_sync = NOW(), last_sync_status = $1, last_sync_error = $2,
			 sync_duration_ms = $3, updated_at = NOW() WHERE id = 1`,
			status, errMsg, durationMs)
	} else {
		s.pg.Exec(ctx,
			`UPDATE rpz_config SET last_sync = NOW(), last_sync_status = $1, last_sync_error = $2,
			 domain_count = $3, file_size_bytes = $4, sync_duration_ms = $5, updated_at = NOW() WHERE id = 1`,
			status, errMsg, domainCount, fileSize, durationMs)
	}
}

// regenerateKresdConfig rebuilds kresd YAML config.
// Custom filter rules use local-data records (small count, inlined).
// RPZ uses native local-data.rpz YAML — loaded once by policy-loader into shared LMDB ruledb,
// then all workers read from the shared DB. No per-worker loading, no 60s timeout issue.
func (s *Server) regenerateKresdConfig(includeRPZ bool) {
	projectDir := s.cfg.ProjectDir
	templatePath := filepath.Join(projectDir, "config/kresd/config.yaml.template")
	configPath := filepath.Join(projectDir, "config/kresd/config.yaml")

	templateData, err := os.ReadFile(templatePath)
	if err != nil {
		log.Printf("Failed to read kresd template: %v", err)
		return
	}

	envVars := loadEnvFile(filepath.Join(projectDir, ".env"))
	serverIP := envVars["SERVER_IP"]
	if serverIP == "" {
		serverIP = "0.0.0.0"
	}
	cacheSize := resolveCacheSize(envVars["CACHE_SIZE"], projectDir, includeRPZ)

	// With native YAML RPZ (local-data.rpz), the zone is loaded once by policy-loader
	// into shared LMDB ruledb. Workers don't load the zone independently, so no OOM risk.
	// We can use auto workers safely now.
	workers := "auto"

	// Build subnet views from DB (with .env fallback)
	var subnetViews strings.Builder
	subnets := s.getAllowedSubnets()
	if len(subnets) == 0 {
		// Fallback to .env if DB is empty
		if envSubnets := envVars["ALLOWED_SUBNETS"]; envSubnets != "" {
			for _, sub := range strings.Split(envSubnets, ",") {
				sub = strings.TrimSpace(sub)
				if sub != "" {
					subnets = append(subnets, sub)
				}
			}
		}
	}
	for _, subnet := range subnets {
		subnetViews.WriteString(fmt.Sprintf("  - subnets: ['%s']\n    answer: allow\n", subnet))
	}

	// Build local-data section with custom filter rules + RPZ
	var localData strings.Builder
	ctx := context.Background()
	rows, err := s.pg.Query(ctx,
		"SELECT domain FROM filter_rules WHERE enabled = true AND action = 'block' ORDER BY domain")
	customCount := 0

	// Check if we need local-data section at all
	hasCustomRules := false
	hasRPZ := false
	var domains []string

	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var d string
			rows.Scan(&d)
			domains = append(domains, d)
		}
		hasCustomRules = len(domains) > 0
		customCount = len(domains)
	}

	// Check RPZ zone file
	rpzFile := "/etc/knot-resolver/rpz.zone"
	localRpzFile := filepath.Join(projectDir, "config/kresd/rpz.zone")
	if includeRPZ {
		if info, err := os.Stat(localRpzFile); err == nil && info.Size() > 100 {
			hasRPZ = true
		}
	}

	if hasCustomRules || hasRPZ {
		localData.WriteString("local-data:\n")

		// Custom filter records (redirect to block page)
		if hasCustomRules {
			localData.WriteString("  records:\n")
			for _, d := range domains {
				localData.WriteString(fmt.Sprintf("    - owner: %s.\n      ttl: 60\n      rdata: '%s'\n", d, serverIP))
				if !strings.HasPrefix(d, "www.") {
					localData.WriteString(fmt.Sprintf("    - owner: www.%s.\n      ttl: 60\n      rdata: '%s'\n", d, serverIP))
				}
			}
		}

		// RPZ zone file — loaded by policy-loader into shared LMDB ruledb
		// All workers read from shared DB, zone is loaded only ONCE
		if hasRPZ {
			localData.WriteString("  rpz:\n")
			localData.WriteString(fmt.Sprintf("    - file: %s\n", rpzFile))
		}
	}

	log.Printf("Regenerating kresd config: %d custom domains, RPZ native=%v", customCount, includeRPZ)

	config := string(templateData)
	config = strings.ReplaceAll(config, "__WORKERS__", workers)
	config = strings.ReplaceAll(config, "__CACHE_SIZE__", cacheSize)
	config = strings.ReplaceAll(config, "__SUBNET_VIEWS__", subnetViews.String())
	config = strings.ReplaceAll(config, "__LOCAL_DATA__", localData.String())

	os.WriteFile(configPath, []byte(config), 0644)
}

// convertRPZForKresd converts custom CNAME targets for kresd compatibility.
// If redirectIP is non-empty, CNAME records are converted to A records pointing to redirectIP
// (block page mode). Otherwise they are converted to "CNAME ." (NXDOMAIN).
// Komdigi RPZ uses "CNAME lamanlabuh.aduankonten.id." which kresd doesn't support.
// Streams line-by-line to handle 1.4GB+ files without memory issues.
// convertResult holds stats from the conversion
type convertResult struct {
	converted int
	skipped   int
}

func convertRPZForKresd(src, dst, redirectIP string) (convertResult, error) {
	in, err := os.Open(src)
	if err != nil {
		return convertResult{}, err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return convertResult{}, err
	}
	defer out.Close()

	writer := bufio.NewWriterSize(out, 256*1024) // 256KB buffer for performance
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 64*1024), 64*1024)

	converted := 0
	skipped := 0
	wroteSOA := false

	for scanner.Scan() {
		line := scanner.Text()

		// Skip empty lines and comments — pass through
		if len(line) == 0 || line[0] == ';' {
			writer.WriteString(line)
			writer.WriteString("\n")
			continue
		}

		// Skip directives like $ORIGIN
		if line[0] == '$' {
			writer.WriteString(line)
			writer.WriteString("\n")
			continue
		}

		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}

		// Detect record type (typically field[3] after owner, TTL, class)
		recType := ""
		for _, f := range fields[1:] {
			upper := strings.ToUpper(f)
			if upper == "SOA" || upper == "NS" || upper == "CNAME" || upper == "A" || upper == "AAAA" || upper == "TXT" {
				recType = upper
				break
			}
		}

		// Write exactly ONE SOA record (required by kresd RPZ parser)
		// Skip NS records (kresd RPZ parser crashes on them)
		// Skip duplicate SOA at end of AXFR
		if recType == "SOA" {
			if !wroteSOA {
				writer.WriteString(line)
				writer.WriteString("\n")
				wroteSOA = true
			}
			continue
		}
		if recType == "NS" {
			continue
		}

		// Only keep CNAME records (the actual RPZ rules)
		if recType != "CNAME" {
			skipped++
			continue
		}

		// Validate domain name strictly for libknot compatibility.
		// libknot crashes on invalid chars, labels starting/ending with hyphens, underscores, etc.
		// Komdigi zone has ~1600 entries that fail strict validation.
		owner := fields[0]
		if !isValidRPZOwner(owner) {
			skipped++
			continue
		}

		// Convert custom CNAME targets for kresd compatibility.
		// If redirectIP is set, produce A records (block page redirect).
		// Otherwise produce "CNAME ." (NXDOMAIN).
		// Skip rpz-passthru (whitelisted domains)
		if !strings.Contains(line, "CNAME\t.") && !strings.Contains(line, "CNAME .") &&
			!strings.Contains(strings.ToUpper(line), "RPZ-PASSTHRU") {
			for i, f := range fields {
				if strings.ToUpper(f) == "CNAME" && i+1 < len(fields) {
					target := fields[i+1]
					if target != "." && !strings.HasPrefix(strings.ToLower(target), "rpz-") {
						if redirectIP != "" {
							fields[i] = "A"
							fields[i+1] = redirectIP
						} else {
							fields[i+1] = "."
						}
						converted++
					}
					break
				}
			}
			line = strings.Join(fields, "\t")
		}

		writer.WriteString(line)
		writer.WriteString("\n")
	}

	if skipped > 0 {
		log.Printf("RPZ conversion: skipped %d entries (invalid names, unsupported record types)", skipped)
	}

	if err := writer.Flush(); err != nil {
		return convertResult{}, fmt.Errorf("flush: %w", err)
	}
	if err := out.Sync(); err != nil {
		return convertResult{}, fmt.Errorf("sync: %w", err)
	}
	if err := scanner.Err(); err != nil {
		return convertResult{}, fmt.Errorf("scan: %w", err)
	}
	return convertResult{converted: converted, skipped: skipped}, nil
}

// countRPZDomains counts unique blocked domains by scanning the zone file line by line.
// Streaming approach — doesn't load entire file into memory.
//
// RPZ zone format: records have the zone name as suffix, e.g.:
//   badsite.com.trustpositifkominfo.  IN  CNAME  .
//   *.badsite.com.trustpositifkominfo.  IN  CNAME  .
// We strip the zone suffix to get the actual domain.
func countRPZDomains(path, zoneName string) int {
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer f.Close()

	count := 0
	zoneSuffix := "." + strings.ToLower(zoneName)
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 64*1024)

	for scanner.Scan() {
		line := scanner.Text()
		if len(line) == 0 || line[0] == ';' || line[0] == '$' {
			continue
		}

		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}

		owner := strings.ToLower(fields[0])
		owner = strings.TrimSuffix(owner, ".")

		// Skip rpz-passthru (whitelisted)
		lineUpper := strings.ToUpper(line)
		if strings.Contains(lineUpper, "RPZ-PASSTHRU") {
			continue
		}

		// Extract domain by stripping RPZ zone suffix
		// e.g. "badsite.com.trustpositifkominfo" → "badsite.com"
		domain := owner
		if idx := strings.Index(owner, zoneSuffix); idx > 0 {
			domain = owner[:idx]
		} else if owner == strings.ToLower(zoneName) || owner == "@" {
			// Skip SOA/NS records for the zone apex itself
			continue
		}

		// Skip wildcard prefix for counting (*.domain counts as same domain)
		domain = strings.TrimPrefix(domain, "*.")

		if domain != "" && strings.Contains(domain, ".") {
			count++
		}
	}

	return count
}

// runRPZAutoSync is a background goroutine that periodically syncs RPZ if auto-sync is enabled.
// Checks every minute. Sync triggers when:
// 1. auto_sync_enabled is true
// 2. Current hour (in server timezone) matches auto_sync_hour
// 3. Enough time has passed since last sync (interval_hours)
func (s *Server) runRPZAutoSync(ctx context.Context) {
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()

	log.Println("RPZ auto-sync scheduler started")
	for {
		select {
		case <-ctx.Done():
			log.Println("RPZ auto-sync scheduler stopped")
			return
		case <-ticker.C:
			cfg := s.getRPZConfig()
			if !cfg.AutoSyncEnabled {
				continue
			}

			// Check if interval has elapsed since last sync
			interval := time.Duration(cfg.AutoSyncIntervalHrs) * time.Hour
			if cfg.LastSync != nil && time.Since(*cfg.LastSync) < interval {
				continue
			}

			// Check if current hour (in server timezone) matches preferred hour
			tz := s.getServerTimezone()
			loc, err := time.LoadLocation(tz)
			if err != nil {
				loc = time.FixedZone("WIB", 7*60*60)
			}
			nowLocal := time.Now().In(loc)
			if nowLocal.Hour() != cfg.AutoSyncHour {
				continue
			}

			// Acquire maintenance lock to prevent conflict with auto-update
			if !s.maintenanceMu.TryLock() {
				log.Println("RPZ auto-sync: skipped, maintenance lock held (update in progress?)")
				continue
			}
			log.Printf("RPZ auto-sync triggered (%02d:00 %s, interval: %dh)", cfg.AutoSyncHour, tz, cfg.AutoSyncIntervalHrs)
			s.doRPZSyncBackground()
			s.maintenanceMu.Unlock()
		}
	}
}

// doRPZSyncBackground performs an RPZ sync without SSE streaming (for auto-sync).
func (s *Server) doRPZSyncBackground() {
	cfg := s.getRPZConfig()
	masters := strings.Split(cfg.MasterServers, ",")
	if len(masters) == 0 {
		log.Println("RPZ auto-sync: no master servers configured")
		return
	}

	startTime := time.Now()
	rpzDir := filepath.Join(s.cfg.ProjectDir, "config", "kresd")
	rpzFile := filepath.Join(rpzDir, "rpz.zone")
	tmpFile := rpzFile + ".tmp"

	// Serial pre-check: a full AXFR is ~1.1GB and ends in a kresd restart that
	// reloads the whole ruledb. Komdigi only bumps the serial when the blocklist
	// changes, so when ours already matches there is nothing to fetch. Skipped
	// only when we have a real zone file on disk to compare against.
	if info, err := os.Stat(rpzFile); err == nil && info.Size() > 1024 {
		if local := localZoneSerial(rpzFile); local != 0 {
			for _, master := range masters {
				master = strings.TrimSpace(master)
				if master == "" {
					continue
				}
				remote := remoteZoneSerial(master, cfg.ZoneName)
				if remote == 0 {
					continue // master unreachable — fall through to the AXFR attempt
				}
				if remote == local {
					log.Printf("RPZ auto-sync: serial %d unchanged at %s — skipping AXFR", local, master)
					s.pg.Exec(context.Background(),
						`UPDATE rpz_config SET last_sync = NOW(), last_sync_status = 'success',
						 last_sync_error = '', sync_duration_ms = $1, updated_at = NOW() WHERE id = 1`,
						int(time.Since(startTime).Milliseconds()))
					return
				}
				log.Printf("RPZ auto-sync: serial %d -> %d, fetching zone", local, remote)
				break
			}
		}
	}

	// Cleanup temp files on start and guaranteed cleanup on exit
	os.Remove(tmpFile)
	os.Remove(tmpFile + ".converted")
	defer os.Remove(tmpFile)
	defer os.Remove(tmpFile + ".converted")

	var usedMaster string
	var axfrErr error
	var sawRefusal bool

	for _, master := range masters {
		master = strings.TrimSpace(master)
		if master == "" {
			continue
		}
		log.Printf("RPZ auto-sync: trying AXFR from %s...", master)

		axfrCtx, axfrCancel := context.WithTimeout(context.Background(), 30*time.Minute)
		cmd := exec.CommandContext(axfrCtx,
			"sh", "-c", fmt.Sprintf(
				"dig AXFR @%s %s +noidnout +tcp +time=300 +tries=2 +nocomments +nostats +nocmd > %s 2>/dev/null",
				master, cfg.ZoneName, tmpFile))
		axfrErr = cmd.Run()
		axfrCancel()

		if info, err := os.Stat(tmpFile); err == nil && axfrErr == nil {
			if head, herr := readFileHead(tmpFile, 1024); herr == nil && strings.Contains(head, cfg.ZoneName) {
				if info.Size() > 10*1024 {
					usedMaster = master
					log.Printf("RPZ auto-sync: got %.1f MB from %s", float64(info.Size())/1024/1024, master)
					break
				}
				sawRefusal = true
			}
		}
	}

	if usedMaster == "" {
		errMsg := "auto-sync: " + classifyAXFRError(axfrErr, sawRefusal)
		log.Printf("RPZ %s", errMsg)
		os.Remove(tmpFile)
		s.updateRPZSyncStatus("error", errMsg, 0, 0, int(time.Since(startTime).Milliseconds()))
		return
	}

	convertedFile := tmpFile + ".converted"
	redirectIP := loadEnvFile(filepath.Join(s.cfg.ProjectDir, ".env"))["SERVER_IP"]
	result, convertErr := convertRPZForKresd(tmpFile, convertedFile, redirectIP)
	if convertErr != nil {
		// See handleRPZSync: a partial conversion (typically disk-full) must not fall back
		// to the raw AXFR — kresd's parser rejects escape sequences, duplicate SOA, and NS
		// records present in the raw zone. Keep the previous good rpz.zone instead.
		errMsg := fmt.Sprintf("auto-sync: conversion failed: %v", convertErr)
		log.Printf("RPZ %s — keeping previous zone file", errMsg)
		s.updateRPZSyncStatus("error", errMsg, 0, 0, int(time.Since(startTime).Milliseconds()))
		return
	}
	log.Printf("RPZ auto-sync: converted %d records (skipped %d invalid), redirect=%s", result.converted, result.skipped, func() string {
		if redirectIP != "" {
			return redirectIP
		}
		return "NXDOMAIN"
	}())
	os.Remove(tmpFile)
	tmpFile = convertedFile

	domainCount := countRPZDomains(tmpFile, cfg.ZoneName)

	tmpInfo, err := os.Stat(tmpFile)
	if err != nil || tmpInfo.Size() < 1024 {
		log.Printf("RPZ auto-sync: converted file too small, aborting")
		os.Remove(tmpFile)
		s.updateRPZSyncStatus("error", "auto-sync: zone file too small", 0, 0, int(time.Since(startTime).Milliseconds()))
		return
	}

	if err := os.Rename(tmpFile, rpzFile); err != nil {
		data, _ := os.ReadFile(tmpFile)
		if len(data) > 1024 {
			os.WriteFile(rpzFile, data, 0644)
		}
		os.Remove(tmpFile)
	}

	duration := time.Since(startTime)
	fileInfo, _ := os.Stat(rpzFile)
	fileSize := int64(0)
	if fileInfo != nil {
		fileSize = fileInfo.Size()
	}

	s.updateRPZSyncStatus("success", "", domainCount, fileSize, int(duration.Milliseconds()))
	s.updateZoneSerial(rpzFile)
	log.Printf("RPZ auto-sync complete: %d domains, %.1f MB, %v", domainCount, float64(fileSize)/1024/1024, duration.Round(time.Second))

	if cfg.Enabled {
		s.regenerateKresdConfig(true)
		restartKresdProper(s.cfg.ProjectDir)
		log.Println("RPZ auto-sync: kresd restarted with updated zone")
	}
}

// readFileHead reads first n bytes of a file
func readFileHead(path string, n int) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	buf := make([]byte, n)
	nr, err := f.Read(buf)
	if nr > 0 {
		return string(buf[:nr]), nil
	}
	return "", err
}

// getKresdMemoryMB returns kresd container memory usage in MB
func getKresdMemoryMB() float64 {
	name := findContainerName("kresd")
	if name == "" {
		return 0
	}
	out, err := exec.Command("docker", "stats", name, "--no-stream", "--format", "{{.MemUsage}}").Output()
	if err != nil {
		return 0
	}
	// Format: "123.4MiB / 1.5GiB"
	s := strings.TrimSpace(string(out))
	parts := strings.Split(s, "/")
	if len(parts) == 0 {
		return 0
	}
	mem := strings.TrimSpace(parts[0])
	mem = strings.ReplaceAll(mem, " ", "")
	if strings.HasSuffix(mem, "GiB") {
		mem = strings.TrimSuffix(mem, "GiB")
		var v float64
		fmt.Sscanf(mem, "%f", &v)
		return v * 1024
	}
	if strings.HasSuffix(mem, "MiB") {
		mem = strings.TrimSuffix(mem, "MiB")
		var v float64
		fmt.Sscanf(mem, "%f", &v)
		return v
	}
	return 0
}
