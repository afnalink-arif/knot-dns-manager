package api

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseSOASerial(t *testing.T) {
	cases := []struct {
		in   string
		want uint64
	}{
		// dig +short SOA output (what remoteZoneSerial reads)
		{"localhost. aduankonten.mail.kominfo.go.id. 26081205 120 60 2592000 900", 26081205},
		// full record, as stored at the head of rpz.zone
		{"trustpositifkominfo.\t900\tIN\tSOA\tlocalhost. aduankonten.mail.kominfo.go.id. 26051703 120 60 2592000 900", 26051703},
		{"", 0},
		{"some.domain.trustpositifkominfo. 3600 IN CNAME .", 0},
	}
	for _, c := range cases {
		if got := parseSOASerial(c.in); got != c.want {
			t.Errorf("parseSOASerial(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestClassifyAXFRError(t *testing.T) {
	if msg := classifyAXFRError(nil, true); !strings.Contains(msg, "ditolak") {
		t.Errorf("refusal should be reported as refusal, got: %s", msg)
	}
	// dig exit 9 = no reply from server; must NOT be reported as a registration problem
	if msg := classifyAXFRError(&fakeErr{"exit status 9"}, false); strings.Contains(msg, "belum di-approve") {
		t.Errorf("exit 9 misdiagnosed as registration issue: %s", msg)
	}
	if msg := classifyAXFRError(&fakeErr{"exit status 9"}, false); !strings.Contains(msg, "balasan") {
		t.Errorf("exit 9 should be reported as connectivity, got: %s", msg)
	}
}

type fakeErr struct{ s string }

func (e *fakeErr) Error() string { return e.s }

func TestIsValidRPZOwner(t *testing.T) {
	valid := []string{
		"domain.com.trustpositifkominfo.",
		"*.domain.com.trustpositifkominfo.",
		"sub-domain.example.co.id.trustpositifkominfo.",
	}
	invalid := []string{
		"",
		".",
		"single",
		"with_underscore.com.trustpositifkominfo.",
		"-leadinghyphen.com.trustpositifkominfo.",
		"emoji💀.com.trustpositifkominfo.",
	}
	for _, v := range valid {
		if !isValidRPZOwner(v) {
			t.Errorf("isValidRPZOwner(%q) = false, want true", v)
		}
	}
	for _, v := range invalid {
		if isValidRPZOwner(v) {
			t.Errorf("isValidRPZOwner(%q) = true, want false", v)
		}
	}
}

func TestLocalZoneSerial(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rpz.zone")
	content := "trustpositifkominfo.\t900\tIN\tSOA\tlocalhost. aduankonten.mail.kominfo.go.id. 26081205 120 60 2592000 900\n" +
		"blocked.example.trustpositifkominfo.\t3600\tIN\tA\t103.186.204.216\n"
	os.WriteFile(path, []byte(content), 0644)
	if got := localZoneSerial(path); got != 26081205 {
		t.Errorf("localZoneSerial = %d, want 26081205", got)
	}
	if got := localZoneSerial(filepath.Join(dir, "missing")); got != 0 {
		t.Errorf("missing file should give 0, got %d", got)
	}
}

func TestOwnerFromRPZLine(t *testing.T) {
	const zone = "trustpositifkominfo"
	// Not probeable: apex/SOA, NS, wildcards, passthru, single-label owners.
	skip := []string{
		"trustpositifkominfo.\t900\tIN\tSOA\tlocalhost. x. 26081205 120 60 2592000 900",
		"trustpositifkominfo.\t900\tIN\tNS\tlocalhost.",
		"*.wild.example.trustpositifkominfo.\t3600\tIN\tA\t103.186.204.216",
		"good.example.trustpositifkominfo.\t3600\tIN\tCNAME\trpz-passthru.",
		"; a comment",
		"$ORIGIN trustpositifkominfo.",
		"",
		"other.zone.example.\t3600\tIN\tA\t1.2.3.4",
	}
	for _, line := range skip {
		if got := ownerFromRPZLine(line, zone); got != "" {
			t.Errorf("ownerFromRPZLine(%q) = %q, want empty", line, got)
		}
	}
	if got := ownerFromRPZLine("blocked.example.trustpositifkominfo.\t3600\tIN\tA\t103.186.204.216", zone); got != "blocked.example" {
		t.Errorf("ownerFromRPZLine = %q, want blocked.example", got)
	}
}

// A half-loaded ruledb keeps the head of the zone, so sampling has to reach
// past it — this is the property that makes the probe able to see a partial load.
func TestSampleBlockedOwnersSpreadsAcrossFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rpz.zone")
	var b strings.Builder
	b.WriteString("trustpositifkominfo.\t900\tIN\tSOA\tlocalhost. x. 26081205 120 60 2592000 900\n")
	for i := 0; i < 5000; i++ {
		fmt.Fprintf(&b, "d%05d.example.trustpositifkominfo.\t3600\tIN\tA\t103.186.204.216\n", i)
		fmt.Fprintf(&b, "*.d%05d.example.trustpositifkominfo.\t3600\tIN\tA\t103.186.204.216\n", i)
	}
	os.WriteFile(path, []byte(b.String()), 0644)

	owners := sampleBlockedOwners(path, "trustpositifkominfo", 8)
	if len(owners) < 4 {
		t.Fatalf("expected several samples, got %d: %v", len(owners), owners)
	}
	seen := map[string]bool{}
	beyondHead := 0
	for _, o := range owners {
		if seen[o] {
			t.Errorf("duplicate sample %q", o)
		}
		seen[o] = true
		if strings.HasPrefix(o, "*") {
			t.Errorf("wildcard owner sampled: %q", o)
		}
		if !strings.HasSuffix(o, ".example") {
			t.Errorf("malformed owner: %q", o)
		}
		if o > "d00500.example" {
			beyondHead++
		}
	}
	if beyondHead == 0 {
		t.Error("all samples came from the head of the file — a partial ruledb load would look healthy")
	}
	if got := sampleBlockedOwners(filepath.Join(dir, "missing"), "trustpositifkominfo", 4); got != nil {
		t.Errorf("missing file should give no samples, got %v", got)
	}
}

func TestZoneBodyDigestIgnoresSerialButNotContent(t *testing.T) {
	dir := t.TempDir()
	write := func(name, soaSerial string, records ...string) string {
		p := filepath.Join(dir, name)
		content := "trustpositifkominfo.\t900\tIN\tSOA\tlocalhost. x. " + soaSerial + " 120 60 2592000 900\n" +
			strings.Join(records, "\n") + "\n"
		os.WriteFile(p, []byte(content), 0644)
		return p
	}
	a := "a.example.trustpositifkominfo.\t3600\tIN\tA\t103.186.204.216"
	b := "b.example.trustpositifkominfo.\t3600\tIN\tA\t103.186.204.216"

	base := write("base", "26081205", a, b)
	bumped := write("bumped", "26081301", a, b) // serial-only bump: must match
	reordered := write("reordered", "26081301", b, a)
	changed := write("changed", "26081301", a)

	dBase, err := zoneBodyDigest(base)
	if err != nil {
		t.Fatalf("digest failed: %v", err)
	}
	dBumped, _ := zoneBodyDigest(bumped)
	if dBase != dBumped {
		t.Error("serial-only bump changed the digest — every daily sync would reload the ruledb")
	}
	// AXFR does not guarantee record order; a positional hash would never match.
	dReordered, _ := zoneBodyDigest(reordered)
	if dBase != dReordered {
		t.Error("record order changed the digest")
	}
	dChanged, _ := zoneBodyDigest(changed)
	if dBase == dChanged {
		t.Error("a removed domain did not change the digest — a real update would be skipped")
	}
	if _, err := zoneBodyDigest(filepath.Join(dir, "missing")); err == nil {
		t.Error("missing file should error")
	}
}

func TestRPZSanityCheck(t *testing.T) {
	// Truncated transfer: absolute floor.
	if err := rpzSanityCheck(12_000, 9_000_000); err == nil {
		t.Error("a 12k-domain zone must be rejected")
	}
	// The 238 failure shape: plausible size, but a fifth of the list is gone.
	if err := rpzSanityCheck(6_000_000, 9_000_000); err == nil {
		t.Error("a 33% shrink must be rejected")
	}
	// Normal churn passes.
	if err := rpzSanityCheck(8_900_000, 9_000_000); err != nil {
		t.Errorf("normal churn rejected: %v", err)
	}
	// Growth always passes.
	if err := rpzSanityCheck(9_500_000, 9_000_000); err != nil {
		t.Errorf("growth rejected: %v", err)
	}
	// First sync ever (no previous count) only faces the absolute floor.
	if err := rpzSanityCheck(9_000_000, 0); err != nil {
		t.Errorf("first sync rejected: %v", err)
	}
}

func TestRPZVerdictHealthy(t *testing.T) {
	cases := []struct {
		name string
		v    rpzVerdict
		want bool
	}{
		{"all blocked", rpzVerdict{ResolverUp: true, Blocked: 8, Total: 8}, true},
		{"one leak", rpzVerdict{ResolverUp: true, Blocked: 7, Total: 8}, false},
		// A dead resolver answers nothing, which must never read as "blocked".
		{"resolver down", rpzVerdict{ResolverUp: false}, false},
		{"no samples", rpzVerdict{ResolverUp: true, Total: 0}, false},
		{"overblock", rpzVerdict{ResolverUp: true, Blocked: 8, Total: 8, Overblock: true}, false},
	}
	for _, c := range cases {
		if got := c.v.Healthy(); got != c.want {
			t.Errorf("%s: Healthy() = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestLoginLimiter(t *testing.T) {
	l := &loginLimiter{fails: map[string]int{}, lockedTo: map[string]time.Time{}}
	key := "1.2.3.4|admin"

	for i := 0; i < loginMaxFails-1; i++ {
		l.recordFailure(key)
		if l.isLocked(key) {
			t.Fatalf("locked after %d failures, threshold is %d", i+1, loginMaxFails)
		}
	}
	l.recordFailure(key)
	if !l.isLocked(key) {
		t.Fatal("not locked after reaching threshold")
	}
	// Other keys must be unaffected
	if l.isLocked("5.6.7.8|admin") {
		t.Fatal("unrelated key locked")
	}
	// Success clears the state
	l.recordSuccess(key)
	if l.isLocked(key) {
		t.Fatal("still locked after recordSuccess")
	}
	// Expired lockout unlocks automatically
	l.recordFailure(key)
	l.mu.Lock()
	l.lockedTo[key] = time.Now().Add(-time.Second)
	l.mu.Unlock()
	if l.isLocked(key) {
		t.Fatal("expired lockout should unlock")
	}
}

func TestSystemAlertDedup(t *testing.T) {
	a := &systemAlerts{firing: map[string]time.Time{}, renote: map[string]time.Time{}}

	// Simulate the fire bookkeeping without a Server (DB/telegram side effects)
	shouldNotify := func(kind string) bool {
		a.mu.Lock()
		defer a.mu.Unlock()
		_, already := a.firing[kind]
		now := time.Now()
		if !already {
			a.firing[kind] = now
		}
		notify := !already || now.Sub(a.renote[kind]) >= reNotifyInterval
		if notify {
			a.renote[kind] = now
		}
		return notify
	}

	if !shouldNotify("x") {
		t.Fatal("first fire must notify")
	}
	if shouldNotify("x") {
		t.Fatal("immediate repeat must be deduplicated")
	}
	// After reNotifyInterval it must notify again
	a.mu.Lock()
	a.renote["x"] = time.Now().Add(-reNotifyInterval - time.Minute)
	a.mu.Unlock()
	if !shouldNotify("x") {
		t.Fatal("stale firing condition must re-notify")
	}
}
