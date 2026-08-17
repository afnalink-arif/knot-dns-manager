package api

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"math/rand"
	"os"
	"strings"
	"sync"
	"time"
)

// ============================================================
// RPZ activation safety — gates before publishing a zone, and
// proof that the published zone is actually filtering.
//
// Two failure modes drove this, both silent:
//   1. A truncated/partial zone replaces a good one. Nothing in the
//      old path compared the new zone against the previous one, so a
//      short transfer could publish and take the filter down.
//   2. A zone loads only halfway into the ruledb (238, 13 Aug: mapsize
//      too small for a cold load — 5 of 10 test domains passed through).
//      kresd does not crash, so no alarm ever fired.
//
// Sampling is spread across the whole file on purpose: a half-loaded
// ruledb contains the head of the zone, so probing the first owner —
// which is what the fleet probe used to do — is the one sample
// guaranteed to say "healthy" while half the blocklist is missing.
// ============================================================

const (
	// rpzMinDomains is an absolute floor. Komdigi's list has been ~9M+
	// domains (~18.9M records with wildcards) for years; anything under
	// a million means a broken transfer, not a policy change.
	rpzMinDomains = 1_000_000

	// rpzMaxShrinkPercent rejects a zone that lost more than this share
	// of the previous generation's domains.
	rpzMaxShrinkPercent = 20

	// rpzShrinkCheckMinDomains: only compare against a previous count
	// that was itself large enough to be trustworthy.
	rpzShrinkCheckMinDomains = 100_000

	// rpzVerifySamples is how many owners are probed per verification
	// round. With a strict "all must block" rule, 8 samples reduce the
	// chance of passing a half-loaded ruledb to well under 1%.
	rpzVerifySamples = 8

	// rpzActivationGrace is how long a fresh load may take before the
	// missing filter is worth alerting on. 238 needs ~4h for a cold
	// full load (slow disk); 216 needs ~48s. The alert is informational
	// during the load, so a short grace is fine and catches real breakage early.
	rpzActivationGrace = 20 * time.Minute

	// rpzActivationBudget bounds the watcher. Beyond this a load is not
	// "still going", it is broken.
	rpzActivationBudget = 6 * time.Hour

	rpzActivationPoll = 2 * time.Minute
)

// rpzSanityCheck reports why a freshly converted zone must not replace the
// running one. nil means it is safe to publish.
func rpzSanityCheck(newCount, prevCount int) error {
	if newCount < rpzMinDomains {
		return fmt.Errorf("zone hanya berisi %d domain (minimum %d) — transfer kemungkinan terpotong",
			newCount, rpzMinDomains)
	}
	if prevCount >= rpzShrinkCheckMinDomains {
		floor := prevCount * (100 - rpzMaxShrinkPercent) / 100
		if newCount < floor {
			return fmt.Errorf("zone menyusut dari %d ke %d domain (batas bawah %d, -%d%%) — ditolak, zone lama dipertahankan",
				prevCount, newCount, floor, rpzMaxShrinkPercent)
		}
	}
	return nil
}

// zoneBodyDigest fingerprints the blocklist content of a zone file, ignoring
// the SOA line so a serial-only bump does not look like new content.
//
// The digest is order-independent (a sum of per-line hashes) because AXFR does
// not guarantee record order between transfers; a positional hash would differ
// on every sync and the gate would never fire. Duplicates still count, and the
// record count is mixed in, so this is far stronger than the "same size" test
// it replaces. A collision here would only skip a reload of an identical
// blocklist, and the sanity gates run regardless.
func zoneBodyDigest(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	var acc [4]uint64
	var records uint64
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 64*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || line[0] == ';' || line[0] == '$' {
			continue
		}
		if strings.Contains(strings.ToUpper(line), "\tSOA\t") || hasSOAField(line) {
			continue
		}
		sum := sha256.Sum256([]byte(line))
		for i := 0; i < 4; i++ {
			acc[i] += binary.LittleEndian.Uint64(sum[i*8 : i*8+8])
		}
		records++
	}
	if err := scanner.Err(); err != nil {
		return "", err
	}
	if records == 0 {
		return "", fmt.Errorf("zone tidak berisi record")
	}
	return fmt.Sprintf("%016x%016x%016x%016x-%d", acc[0], acc[1], acc[2], acc[3], records), nil
}

func hasSOAField(line string) bool {
	for _, f := range strings.Fields(line) {
		if strings.EqualFold(f, "SOA") {
			return true
		}
	}
	return false
}

// ownerFromRPZLine extracts a queryable domain from one RPZ record line.
// Returns "" for anything that cannot be probed directly: the apex, SOA/NS
// records, wildcards, and passthru entries.
func ownerFromRPZLine(line, zoneName string) string {
	line = strings.TrimSpace(line)
	if line == "" || line[0] == ';' || line[0] == '$' {
		return ""
	}
	fields := strings.Fields(line)
	if len(fields) < 4 {
		return ""
	}
	upper := strings.ToUpper(line)
	if strings.Contains(upper, "RPZ-PASSTHRU") {
		return ""
	}
	for _, f := range fields[1:] {
		if strings.EqualFold(f, "SOA") || strings.EqualFold(f, "NS") {
			return ""
		}
	}
	owner := strings.ToLower(fields[0])
	suffix := "." + strings.ToLower(zoneName) + "."
	if !strings.HasSuffix(owner, suffix) {
		return ""
	}
	name := strings.TrimSuffix(owner, suffix)
	// Wildcards cannot be queried literally, and the plain form of the same
	// domain is always present alongside them in Komdigi's zone.
	if name == "" || strings.HasPrefix(name, "*") || !strings.Contains(name, ".") {
		return ""
	}
	return name
}

// sampleBlockedOwners picks owners spread across the whole zone file by seeking
// to jittered offsets, so it never loads 1.3GB and never keeps returning the
// same head-of-file domain (which kresd would then answer from cache).
func sampleBlockedOwners(zonePath, zoneName string, n int) []string {
	f, err := os.Open(zonePath)
	if err != nil {
		return nil
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil || info.Size() < 1024 {
		return nil
	}
	size := info.Size()

	seen := map[string]bool{}
	owners := make([]string, 0, n)
	for i := 0; i < n; i++ {
		// Spread the slots evenly, then jitter inside each slot so repeat
		// rounds probe names that have never been queried before.
		slot := size / int64(n)
		off := slot*int64(i) + rand.Int63n(max64(slot, 1))
		if owner := ownerAtOffset(f, off, zoneName); owner != "" && !seen[owner] {
			seen[owner] = true
			owners = append(owners, owner)
		}
	}
	return owners
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

// ownerAtOffset reads forward from a byte offset until it finds a usable owner.
func ownerAtOffset(f *os.File, off int64, zoneName string) string {
	if _, err := f.Seek(off, io.SeekStart); err != nil {
		return ""
	}
	r := bufio.NewReaderSize(f, 64*1024)
	if off > 0 {
		if _, err := r.ReadString('\n'); err != nil { // discard the partial line
			return ""
		}
	}
	for tries := 0; tries < 200; tries++ {
		line, err := r.ReadString('\n')
		if owner := ownerFromRPZLine(line, zoneName); owner != "" {
			return owner
		}
		if err != nil {
			return ""
		}
	}
	return ""
}

// rpzVerdict is the outcome of one verification round.
type rpzVerdict struct {
	ResolverUp bool
	Blocked    int
	Total      int
	Overblock  bool
	Detail     string
}

func (v rpzVerdict) Healthy() bool {
	return v.ResolverUp && v.Total > 0 && v.Blocked == v.Total && !v.Overblock
}

// verifyRPZFiltering proves — or disproves — that the running resolver is
// applying the zone on disk.
//
// The control query comes first: without it, a dead resolver answers nothing
// and "no answer" would be read as "blocked", turning a total outage into a
// clean bill of health. When a redirect IP is configured (all fleet nodes have
// one) a blocked answer must equal that IP exactly; accepting NXDOMAIN there
// would also accept a domain that simply failed to resolve upstream.
func verifyRPZFiltering(zonePath, zoneName, redirectIP string, samples int) rpzVerdict {
	v := rpzVerdict{}

	controlOK, controlAnswer := digAnswers("kresd", "google.com")
	if !controlOK {
		v.Detail = "resolver tidak menjawab query kontrol (google.com) — verifikasi tidak konklusif"
		return v
	}
	v.ResolverUp = true
	if redirectIP != "" && controlAnswer == redirectIP {
		v.Overblock = true
		v.Detail = fmt.Sprintf("over-block: google.com dijawab %s (block page)", controlAnswer)
		return v
	}

	owners := sampleBlockedOwners(zonePath, zoneName, samples)
	if len(owners) == 0 {
		v.Detail = "tidak ada owner yang bisa disampel dari zone file"
		return v
	}

	var leaked []string
	for _, owner := range owners {
		answered, answer := digAnswers("kresd", owner)
		blocked := false
		if redirectIP != "" {
			blocked = answered && answer == redirectIP
		} else {
			blocked = !answered // NXDOMAIN is the only block signal without a redirect IP
		}
		if blocked {
			v.Blocked++
		} else {
			leaked = append(leaked, fmt.Sprintf("%s->%s", owner, answer))
		}
	}
	v.Total = len(owners)

	if len(leaked) == 0 {
		v.Detail = fmt.Sprintf("%d/%d sampel diblokir ke %s", v.Blocked, v.Total, redirectIP)
	} else {
		v.Detail = fmt.Sprintf("%d/%d sampel diblokir; lolos: %s",
			v.Blocked, v.Total, strings.Join(leaked, ", "))
	}
	return v
}

// publishOutcome reports what the caller still has to do after a zone is live.
type publishOutcome struct {
	// reloadNeeded is false when the new file carries a new serial but the
	// exact same blocklist — the expensive part is the ruledb reload, not the
	// transfer, so skipping it here is what removes the daily unfiltered window.
	reloadNeeded bool
	// prevLink is the previous generation, held for the verification window.
	prevLink string
	digest   string
}

// publishRPZZone runs the gates, swaps the zone in atomically, and decides
// whether a reload is warranted. On error nothing is swapped and the running
// zone stays exactly as it was.
func (s *Server) publishRPZZone(tmpFile, rpzFile string, newCount int, cfg RPZConfig) (publishOutcome, error) {
	if err := rpzSanityCheck(newCount, cfg.DomainCount); err != nil {
		return publishOutcome{}, err
	}

	out := publishOutcome{reloadNeeded: true}
	digest, err := zoneBodyDigest(tmpFile)
	if err != nil {
		// Not fatal — without a digest we just lose the ability to skip a reload.
		log.Printf("RPZ: gagal menghitung digest zone baru: %v", err)
	} else {
		out.digest = digest
		if cfg.ZoneDigest != "" && digest == cfg.ZoneDigest {
			out.reloadNeeded = false
		}
	}

	out.prevLink = linkPreviousZone(rpzFile)
	if err := os.Rename(tmpFile, rpzFile); err != nil {
		if out.prevLink != "" {
			os.Remove(out.prevLink)
		}
		return publishOutcome{}, fmt.Errorf("gagal memasang zone baru: %w", err)
	}
	if out.digest != "" {
		s.pg.Exec(context.Background(),
			`UPDATE rpz_config SET zone_digest = $1 WHERE id = 1`, out.digest)
	}
	if !out.reloadNeeded && out.prevLink != "" {
		os.Remove(out.prevLink) // no reload, nothing to verify, nothing to restore
		out.prevLink = ""
	}
	return out, nil
}

// rpzWatchRunning keeps a single activation watcher at a time — a manual sync
// during an auto-sync reload would otherwise stack duplicate alert loops.
var rpzWatchRunning sync.Mutex

const rpzActivationAlert = "rpz-activation"

// watchRPZActivation polls until the newly loaded zone is proven to be
// filtering, then clears the previous-generation link.
//
// It deliberately does NOT roll back on failure. Rolling back means another
// full ruledb reload — up to ~4 hours on 238 — so an automatic rollback would
// extend the unfiltered window rather than end it. The previous zone stays
// linked as rpz.zone.previous and the alert carries the restore command, so a
// human decides. Silence, not a wrong zone, was the actual failure here.
func (s *Server) watchRPZActivation(zonePath, zoneName, redirectIP string, prevLink string) {
	if !rpzWatchRunning.TryLock() {
		log.Println("RPZ verify: watcher sudah berjalan — lewati")
		return
	}
	defer rpzWatchRunning.Unlock()

	started := time.Now()
	alerted := false
	for {
		v := verifyRPZFiltering(zonePath, zoneName, redirectIP, rpzVerifySamples)
		elapsed := time.Since(started).Round(time.Second)

		if v.Healthy() {
			log.Printf("RPZ verify: filter aktif setelah %v (%s)", elapsed, v.Detail)
			s.recordRPZVerify("ok", v.Detail)
			s.resolveSystemAlert(rpzActivationAlert,
				fmt.Sprintf("RPZ kembali menapis setelah reload (%s, %v).", v.Detail, elapsed))
			if prevLink != "" {
				os.Remove(prevLink)
			}
			return
		}

		if time.Since(started) > rpzActivationBudget {
			restore := "Zone generasi sebelumnya tidak tersimpan (IXFR menulis di tempat)."
			if prevLink != "" {
				restore = fmt.Sprintf("Untuk mengembalikan zone lama: mv %s %s lalu restart kresd.", prevLink, zonePath)
			}
			msg := fmt.Sprintf("RPZ TIDAK menapis %v setelah reload — %s. %s",
				elapsed, v.Detail, restore)
			log.Printf("RPZ verify: %s", msg)
			s.recordRPZVerify("failed", v.Detail)
			s.fireSystemAlert(rpzActivationAlert, msg)
			return
		}

		if !alerted && time.Since(started) > rpzActivationGrace {
			alerted = true
			s.recordRPZVerify("loading", v.Detail)
			s.fireSystemAlert(rpzActivationAlert, fmt.Sprintf(
				"RPZ belum menapis %v setelah reload — %s. Load ruledb bisa memakan jam pada disk lambat; alert ini otomatis resolve saat filter aktif.",
				elapsed, v.Detail))
		}
		time.Sleep(rpzActivationPoll)
	}
}

func (s *Server) recordRPZVerify(status, detail string) {
	s.pg.Exec(context.Background(),
		`UPDATE rpz_config SET last_verify_at = NOW(), last_verify_status = $1, last_verify_detail = $2
		 WHERE id = 1`, status, detail)
}

// linkPreviousZone keeps the outgoing generation reachable across the atomic
// rename. A hard link costs no disk — it only withholds the old inode from
// being freed — so the previous zone survives for as long as verification runs.
func linkPreviousZone(zonePath string) string {
	prev := zonePath + ".previous"
	os.Remove(prev)
	if err := os.Link(zonePath, prev); err != nil {
		return ""
	}
	return prev
}
