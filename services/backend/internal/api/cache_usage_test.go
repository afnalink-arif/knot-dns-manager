package api

import "testing"

// The output mdb_stat actually produces against a live kresd cache: kresd
// holds the environment open, the read transaction for the freelist/per-DB
// sections fails with EAGAIN, and the tool exits 1 after printing only the
// environment block. This is the common case, not an edge case.
const truncatedStat = `Environment Info
  Map address: 0
  Map size: 251654144
  Page size: 4096
  Max pages: 61439
  Number of pages used: 4990
  Last transaction ID: 1064220
  Max readers: 126
  Number of readers used: 10
`

const fullStat = `Environment Info
  Map address: 0
  Map size: 12884901888
  Page size: 4096
  Max pages: 3145728
  Number of pages used: 2962992
  Last transaction ID: 5
  Max readers: 126
  Number of readers used: 8
Freelist Status
  Tree depth: 1
  Branch pages: 0
  Leaf pages: 1
  Overflow pages: 3856
  Entries: 2
  Free pages: 1973398
Status of Main DB
  Tree depth: 5
  Branch pages: 9779
  Leaf pages: 975956
  Overflow pages: 0
  Entries: 37865648
`

func TestParseMdbStatTruncated(t *testing.T) {
	u := parseMdbStat(truncatedStat)

	if !u.Available {
		t.Fatalf("expected usable result from environment block alone, got error %q", u.Error)
	}
	if !u.HighWaterOnly {
		t.Error("freelist absent, so HighWaterOnly must be set")
	}
	if u.MapSizeBytes != 251654144 {
		t.Errorf("MapSizeBytes = %d, want 251654144", u.MapSizeBytes)
	}
	if want := int64(4990 * 4096); u.UsedBytes != want {
		t.Errorf("UsedBytes = %d, want %d", u.UsedBytes, want)
	}
	// With no freelist, live collapses to the high-water mark.
	if u.LiveBytes != u.UsedBytes {
		t.Errorf("LiveBytes = %d, want %d", u.LiveBytes, u.UsedBytes)
	}
	if u.Entries != 0 {
		t.Errorf("Entries = %d, want 0 when the main DB section is missing", u.Entries)
	}
	if u.PercentUsed < 8.0 || u.PercentUsed > 8.2 {
		t.Errorf("PercentUsed = %v, want ~8.1", u.PercentUsed)
	}
}

func TestParseMdbStatFull(t *testing.T) {
	u := parseMdbStat(fullStat)

	if !u.Available || u.HighWaterOnly {
		t.Fatalf("full output should be exact: available=%v highWaterOnly=%v", u.Available, u.HighWaterOnly)
	}
	// Entries must come from "Status of Main DB", not the freelist's own
	// "Entries: 2" line that appears earlier.
	if u.Entries != 37865648 {
		t.Errorf("Entries = %d, want 37865648 (freelist Entries must not win)", u.Entries)
	}
	wantLive := int64(2962992-1973398) * 4096
	if u.LiveBytes != wantLive {
		t.Errorf("LiveBytes = %d, want %d", u.LiveBytes, wantLive)
	}
	// ~3.8 GiB live inside a 12 GiB map.
	if u.PercentUsed < 31 || u.PercentUsed > 32 {
		t.Errorf("PercentUsed = %v, want ~31.5", u.PercentUsed)
	}
}

func TestParseMdbStatGarbage(t *testing.T) {
	u := parseMdbStat("mdb_stat: No such file or directory\n")
	if u.Available {
		t.Error("garbage input must not report Available")
	}
	if u.Error == "" {
		t.Error("expected an error message")
	}
}
