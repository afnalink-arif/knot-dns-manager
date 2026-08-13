package api

import (
	"bufio"
	"bytes"
	"context"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// CacheUsage reports how much of the resolver cache is actually occupied.
//
// kresd exports hit/miss counters but nothing about cache occupancy, so the
// dashboard could only ever show the configured ceiling — a number that tells
// you nothing about whether the cache is empty, healthy, or thrashing. The
// cache is a plain LMDB environment, so we read the real figures out of it
// with mdb_stat.
//
// LMDB accounting, because the three numbers are easy to confuse:
//   - MapSizeBytes  the ceiling (`size-max`), reserved address space
//   - UsedBytes     high-water mark: pages LMDB has ever touched. It never
//     shrinks, so this stays at the peak even after entries expire.
//   - LiveBytes     used minus the freelist — what is genuinely held right now.
//
// PercentUsed is LiveBytes/MapSizeBytes: "how full is the cache", which is the
// question an operator is actually asking.
type CacheUsage struct {
	MapSizeBytes int64   `json:"map_size_bytes"`
	UsedBytes    int64   `json:"used_bytes"`
	FreeBytes    int64   `json:"free_bytes"`
	LiveBytes    int64   `json:"live_bytes"`
	PercentUsed  float64 `json:"percent_used"`
	Entries      int64   `json:"entries"`
	Available    bool    `json:"available"`
	// HighWaterOnly is set when the per-DB sections could not be read (see
	// readCacheUsage): LiveBytes then equals the high-water mark rather than
	// live occupancy, and Entries is unknown.
	HighWaterOnly bool   `json:"high_water_only"`
	Error         string `json:"error,omitempty"`
}

const cacheMountPath = "/cache"

// readCacheUsage shells out to mdb_stat over the mounted cache volume.
// Returns Available=false (never an error) so a missing tool or unmounted
// volume degrades to "unknown" in the UI instead of failing the whole
// resolver-info response.
func readCacheUsage(ctx context.Context) CacheUsage {
	u := CacheUsage{}

	if _, err := os.Stat(cacheMountPath); err != nil {
		u.Error = "cache volume not mounted at " + cacheMountPath
		return u
	}

	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	// Deliberately ignore the exit status. kresd holds the cache open and
	// writes to it constantly, so mdb_stat's read transaction for the
	// freelist/per-DB sections routinely fails with EAGAIN and it exits 1 —
	// but it has already printed the "Environment Info" block we care about
	// to stdout. Treating that exit code as fatal would throw away good data
	// and report the cache as unmeasurable almost every time.
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "mdb_stat", "-ef", cacheMountPath)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	runErr := cmd.Run()

	u = parseMdbStat(stdout.String())
	if !u.Available && runErr != nil && u.Error != "" {
		u.Error += ": " + strings.TrimSpace(stderr.String())
	}
	return u
}

// parseMdbStat turns mdb_stat -ef output into CacheUsage. Split out from the
// exec so the truncated-output case (see readCacheUsage) is directly testable.
func parseMdbStat(out string) CacheUsage {
	u := CacheUsage{}

	var pageSize, pagesUsed, freePages int64
	sawFreelist := false
	scanner := bufio.NewScanner(strings.NewReader(out))
	inMainDB := false

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "Status of Main DB") {
			inMainDB = true
			continue
		}
		num := func(prefix string) (int64, bool) {
			if !strings.HasPrefix(line, prefix) {
				return 0, false
			}
			v, err := strconv.ParseInt(strings.TrimSpace(strings.TrimPrefix(line, prefix)), 10, 64)
			return v, err == nil
		}
		if v, ok := num("Map size:"); ok {
			u.MapSizeBytes = v
		} else if v, ok := num("Page size:"); ok {
			pageSize = v
		} else if v, ok := num("Number of pages used:"); ok {
			pagesUsed = v
		} else if v, ok := num("Free pages:"); ok {
			freePages = v
			sawFreelist = true
		} else if v, ok := num("Entries:"); ok && inMainDB {
			// Both the freelist and the main DB report "Entries:"; only the
			// one after "Status of Main DB" is the cached-record count.
			u.Entries = v
		}
	}

	if pageSize == 0 || u.MapSizeBytes == 0 {
		u.Error = "could not parse mdb_stat output"
		return u
	}
	u.HighWaterOnly = !sawFreelist

	u.UsedBytes = pagesUsed * pageSize
	u.FreeBytes = freePages * pageSize
	u.LiveBytes = u.UsedBytes - u.FreeBytes
	if u.LiveBytes < 0 {
		u.LiveBytes = 0
	}
	u.PercentUsed = float64(u.LiveBytes) / float64(u.MapSizeBytes) * 100
	u.Available = true
	return u
}
