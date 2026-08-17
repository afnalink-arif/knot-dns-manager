package api

import (
	"context"
	"fmt"
	"net/http"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// ============================================================
// Fleet probe — the "eyes from outside" this fleet lacked.
//
// Every node watches:
//   1. its peers' DNS port 53 (a resolver that stops answering is
//      invisible to its own dashboard — 238's dnsdist died silently)
//   2. its own RPZ actually blocking (216 served an 87-day-old zone
//      for months while reporting "sync success")
//   3. its own zone serial vs Komdigi's (drift = compliance risk)
//
// Findings go through fireSystemAlert/resolveSystemAlert, so they
// land in alert_events and Telegram with dedup + re-notify.
// ============================================================

type probeState struct {
	mu       sync.Mutex
	failures map[string]int       // check key -> consecutive failures
	results  map[string]probeItem // check key -> last observation
}

type probeItem struct {
	Key       string    `json:"key"`
	OK        bool      `json:"ok"`
	Detail    string    `json:"detail"`
	CheckedAt time.Time `json:"checked_at"`
}

var fleetProbe = &probeState{
	failures: map[string]int{},
	results:  map[string]probeItem{},
}

// failsBeforeAlert: nightly RPZ reloads hold kresd down for up to ~3 minutes,
// and dnsdist only covers cached names. Two consecutive failures (checks run
// 5 minutes apart) outlast that window; a real outage still alerts inside ~10m.
const failsBeforeAlert = 2

func (p *probeState) record(key string, ok bool, detail string) (fails int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if ok {
		p.failures[key] = 0
	} else {
		p.failures[key]++
	}
	p.results[key] = probeItem{Key: key, OK: ok, Detail: detail, CheckedAt: time.Now()}
	return p.failures[key]
}

func (p *probeState) snapshot() []probeItem {
	p.mu.Lock()
	defer p.mu.Unlock()
	items := make([]probeItem, 0, len(p.results))
	for _, it := range p.results {
		items = append(items, it)
	}
	return items
}

func (s *Server) getProbePeers() []string {
	var raw string
	s.pg.QueryRow(context.Background(),
		`SELECT probe_peers FROM server_config WHERE id = 1`).Scan(&raw)
	var peers []string
	for _, p := range strings.Split(raw, ",") {
		if p = strings.TrimSpace(p); p != "" {
			peers = append(peers, p)
		}
	}
	return peers
}

func (s *Server) handleFleetProbeStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]interface{}{
		"peers":   s.getProbePeers(),
		"results": fleetProbe.snapshot(),
	})
}

// digAnswers runs one DNS query and reports whether it got a real answer.
func digAnswers(server, name string) (bool, string) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "dig", "@"+server, name,
		"+short", "+time=4", "+tries=2").Output()
	answer := strings.TrimSpace(string(out))
	if err != nil || answer == "" {
		return false, "no answer"
	}
	return true, strings.Split(answer, "\n")[0]
}

// runFleetProbe is the watchdog loop; started from NewRouter.
func (s *Server) runFleetProbe(ctx context.Context) {
	// First pass shortly after boot so a fresh deploy reports quickly,
	// but give the stack a moment to settle.
	peerTicker := time.NewTicker(5 * time.Minute)
	serialTicker := time.NewTicker(1 * time.Hour)
	defer peerTicker.Stop()
	defer serialTicker.Stop()

	boot := time.After(90 * time.Second)
	for {
		select {
		case <-ctx.Done():
			return
		case <-boot:
			s.probePeersAndSelf()
			s.probeSerialDrift()
		case <-peerTicker.C:
			s.probePeersAndSelf()
		case <-serialTicker.C:
			s.probeSerialDrift()
		}
	}
}

func (s *Server) probePeersAndSelf() {
	// --- peers: is port 53 answering at all? ---
	for _, peer := range s.getProbePeers() {
		key := "peer-dns:" + peer
		ok, detail := digAnswers(peer, "google.com")
		fails := fleetProbe.record(key, ok, detail)
		if ok {
			s.resolveSystemAlert(key, fmt.Sprintf("DNS peer %s kembali menjawab di port 53.", peer))
		} else if fails >= failsBeforeAlert {
			s.fireSystemAlert(key, fmt.Sprintf(
				"DNS peer %s TIDAK menjawab di port 53 (%d cek berturut-turut). Cek dnsdist/kresd di node itu.", peer, fails))
		}
	}

	// --- self: does RPZ actually block? ---
	cfg := s.getRPZConfig()
	if !cfg.Enabled {
		return
	}
	zonePath := filepath.Join(s.cfg.ProjectDir, "config", "kresd", "rpz.zone")
	key := "self-rpz"
	serverIP := loadEnvFile(filepath.Join(s.cfg.ProjectDir, ".env"))["SERVER_IP"]

	// Sampled across the whole zone, and re-sampled every round. Probing the
	// first owner (what this did before) is the weakest possible test: a
	// partially loaded ruledb holds the head of the zone, so that one name
	// answers "blocked" while the rest of the list leaks — and after the first
	// round it is served from cache anyway.
	v := verifyRPZFiltering(zonePath, cfg.ZoneName, serverIP, rpzVerifySamples)
	if !v.ResolverUp || v.Total == 0 {
		// Resolver down is the peer check's job; an inconclusive filter probe
		// must not be recorded as either healthy or leaking.
		return
	}
	fails := fleetProbe.record(key, v.Healthy(), v.Detail)
	if v.Healthy() {
		s.resolveSystemAlert(key, "RPZ kembali memblokir dengan benar ("+v.Detail+").")
	} else if fails >= failsBeforeAlert {
		s.fireSystemAlert(key, fmt.Sprintf(
			"RPZ TIDAK memblokir sepenuhnya: %s. Ruledb kresd mungkin termuat sebagian.", v.Detail))
	}
}

// probeSerialDrift compares the local zone serial with Komdigi's. Local behind
// for more than a full sync interval (+slack) means syncs are silently failing
// or not being applied — exactly how 216 drifted 87 days without a alarm.
func (s *Server) probeSerialDrift() {
	cfg := s.getRPZConfig()
	if !cfg.Enabled {
		return
	}
	zonePath := filepath.Join(s.cfg.ProjectDir, "config", "kresd", "rpz.zone")
	local := localZoneSerial(zonePath)
	if local == 0 {
		return
	}
	var remote uint64
	for _, master := range strings.Split(cfg.MasterServers, ",") {
		if master = strings.TrimSpace(master); master == "" {
			continue
		}
		if remote = remoteZoneSerial(master, cfg.ZoneName); remote != 0 {
			break
		}
	}
	key := "serial-drift"
	if remote == 0 {
		// Masters unreachable is its own condition — don't conflate with drift.
		fails := fleetProbe.record("komdigi-master", false, "SOA query gagal ke semua master")
		if fails >= failsBeforeAlert {
			s.fireSystemAlert("komdigi-master",
				"Master RPZ Komdigi tidak bisa dihubungi (SOA query gagal ke semua master).")
		}
		return
	}
	fleetProbe.record("komdigi-master", true, fmt.Sprintf("serial %d", remote))
	s.resolveSystemAlert("komdigi-master", "Master RPZ Komdigi kembali bisa dihubungi.")

	graceHrs := time.Duration(cfg.AutoSyncIntervalHrs+4) * time.Hour
	stale := cfg.LastSync == nil || time.Since(*cfg.LastSync) > graceHrs
	drifting := remote > local && stale
	fleetProbe.record(key, !drifting, fmt.Sprintf("local=%d komdigi=%d", local, remote))
	if drifting {
		s.fireSystemAlert(key, fmt.Sprintf(
			"Zone RPZ tertinggal: lokal %d vs Komdigi %d dan sync terakhir > %dh lalu. Cek RPZ sync di dashboard.",
			local, remote, cfg.AutoSyncIntervalHrs+4))
	} else {
		s.resolveSystemAlert(key, fmt.Sprintf("Zone RPZ kembali sinkron (serial %d).", local))
	}
}
