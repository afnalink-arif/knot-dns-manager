package api

import (
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

func TestFirstBlockedOwner(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rpz.zone")
	content := "trustpositifkominfo.\t900\tIN\tSOA\tlocalhost. x. 26081205 120 60 2592000 900\n" +
		"*.wild.example.trustpositifkominfo.\t3600\tIN\tCNAME\t.\n" +
		"blocked.example.trustpositifkominfo.\t3600\tIN\tA\t103.186.204.216\n"
	os.WriteFile(path, []byte(content), 0644)
	// SOA and wildcard lines must be skipped; the plain owner is returned
	if got := firstBlockedOwner(path, "trustpositifkominfo"); got != "blocked.example" {
		t.Errorf("firstBlockedOwner = %q, want %q", got, "blocked.example")
	}
	if got := firstBlockedOwner(filepath.Join(dir, "missing"), "trustpositifkominfo"); got != "" {
		t.Errorf("missing file should give empty owner, got %q", got)
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
