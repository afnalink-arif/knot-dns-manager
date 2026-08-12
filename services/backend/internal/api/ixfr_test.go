package api

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const soaLine = "trustpositifkominfo.\t900\tIN\tSOA\tlocalhost. aduankonten.mail.kominfo.go.id. %s 120 60 2592000 900"

func soa(serial string) string {
	return strings.Replace(soaLine, "%s", serial, 1)
}

func TestParseIXFRUnchanged(t *testing.T) {
	in := soa("26081205") + "\n"
	outcome, stats, _, err := parseIXFR(strings.NewReader(in), "")
	if err != nil || outcome != ixfrUnchanged {
		t.Fatalf("outcome=%v err=%v, want unchanged", outcome, err)
	}
	if stats.newSerial != 26081205 {
		t.Errorf("newSerial=%d, want 26081205", stats.newSerial)
	}
}

func TestParseIXFRFullTransferDetected(t *testing.T) {
	in := soa("26081206") + "\n" +
		"blocked.example.trustpositifkominfo.\t3600\tIN\tCNAME\tlamanlabuh.aduankonten.id.\n"
	outcome, _, _, err := parseIXFR(strings.NewReader(in), "")
	if err != nil || outcome != ixfrFullTransfer {
		t.Fatalf("outcome=%v err=%v, want fullTransfer", outcome, err)
	}
}

func TestParseIXFRIncremental(t *testing.T) {
	// header SOA(new) | SOA(old) deletions | SOA(new) additions | SOA(new)
	in := strings.Join([]string{
		soa("26081206"),
		soa("26081205"),
		"gone.example.trustpositifkominfo.\t3600\tIN\tCNAME\tlamanlabuh.aduankonten.id.",
		"*.gone.example.trustpositifkominfo.\t3600\tIN\tCNAME\tlamanlabuh.aduankonten.id.",
		soa("26081206"),
		"fresh.example.trustpositifkominfo.\t3600\tIN\tCNAME\tlamanlabuh.aduankonten.id.",
		"bad_owner.example.trustpositifkominfo.\t3600\tIN\tCNAME\tlamanlabuh.aduankonten.id.",
		soa("26081206"),
	}, "\n") + "\n"

	outcome, stats, ops, err := parseIXFR(strings.NewReader(in), "103.186.204.216")
	if err != nil || outcome != ixfrApplied {
		t.Fatalf("outcome=%v err=%v, want applied", outcome, err)
	}
	if stats.newSerial != 26081206 || stats.deleted != 2 || stats.added != 1 || stats.skipped != 1 {
		t.Errorf("stats=%+v, want serial=26081206 del=2 add=1 skip=1", stats)
	}
	if v, ok := ops["gone.example.trustpositifkominfo."]; !ok || v != "" {
		t.Errorf("deletion op missing/wrong: %q", v)
	}
	added := ops["fresh.example.trustpositifkominfo."]
	// redirect mode: CNAME target must become an A record to SERVER_IP
	if !strings.Contains(added, "\tA\t103.186.204.216") {
		t.Errorf("addition not converted to redirect A record: %q", added)
	}
}

func TestConvertRPZRecord(t *testing.T) {
	fields := strings.Fields("blocked.example.trustpositifkominfo. 3600 IN CNAME lamanlabuh.aduankonten.id.")
	// NXDOMAIN mode
	line, ok := convertRPZRecord(append([]string(nil), fields...), "")
	if !ok || !strings.HasSuffix(line, "\tCNAME\t.") {
		t.Errorf("NXDOMAIN conversion wrong: %q ok=%v", line, ok)
	}
	// redirect mode
	line, ok = convertRPZRecord(append([]string(nil), fields...), "1.2.3.4")
	if !ok || !strings.Contains(line, "\tA\t1.2.3.4") {
		t.Errorf("redirect conversion wrong: %q ok=%v", line, ok)
	}
	// rpz-passthru must be left alone
	pt := strings.Fields("white.example.trustpositifkominfo. 3600 IN CNAME rpz-passthru.")
	line, ok = convertRPZRecord(pt, "1.2.3.4")
	if !ok || !strings.Contains(line, "rpz-passthru.") {
		t.Errorf("passthru mangled: %q ok=%v", line, ok)
	}
	// non-CNAME rejected
	if _, ok := convertRPZRecord(strings.Fields("x.trustpositifkominfo. 3600 IN NS ns1.example."), ""); ok {
		t.Error("NS record must be rejected")
	}
}

func TestApplyIXFROps(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rpz.zone")
	content := strings.Join([]string{
		soa("26081205"),
		"keep.example.trustpositifkominfo.\t3600\tIN\tCNAME\t.",
		"gone.example.trustpositifkominfo.\t3600\tIN\tCNAME\t.",
		"replace.example.trustpositifkominfo.\t3600\tIN\tCNAME\t.",
	}, "\n") + "\n"
	os.WriteFile(path, []byte(content), 0644)

	ops := map[string]string{
		"gone.example.trustpositifkominfo.":    "",
		"replace.example.trustpositifkominfo.": "replace.example.trustpositifkominfo.\t3600\tIN\tA\t1.2.3.4",
		"fresh.example.trustpositifkominfo.":   "fresh.example.trustpositifkominfo.\t3600\tIN\tCNAME\t.",
		"never-there.trustpositifkominfo.":     "", // delete for absent owner is a no-op
	}
	if err := applyIXFROps(path, ops, 26081206); err != nil {
		t.Fatalf("apply: %v", err)
	}

	data, _ := os.ReadFile(path)
	got := string(data)
	for _, want := range []string{"26081206", "keep.example", "fresh.example", "\tA\t1.2.3.4"} {
		if !strings.Contains(got, want) {
			t.Errorf("result missing %q:\n%s", want, got)
		}
	}
	for _, absent := range []string{"26081205", "gone.example", "never-there"} {
		if strings.Contains(got, absent) {
			t.Errorf("result must not contain %q:\n%s", absent, got)
		}
	}
	if strings.Count(got, "replace.example") != 1 {
		t.Errorf("replacement duplicated:\n%s", got)
	}
	if got := localZoneSerial(path); got != 26081206 {
		t.Errorf("serial after apply = %d, want 26081206", got)
	}
}
