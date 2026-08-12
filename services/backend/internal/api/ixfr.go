package api

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// ============================================================
// IXFR — incremental zone transfer (juknis Komdigi §3).
//
// A serial bump used to mean re-downloading the full ~1.5GB zone
// even when the actual change was a few thousand entries. IXFR asks
// the master for just the diff since our local serial and applies it
// to the stored (already converted) zone file in one streaming pass.
//
// dig IXFR=<serial> output comes in three shapes:
//   unchanged:    single SOA record (serial equal) — nothing to do
//   incremental:  SOA(new) | SOA(old) del... | SOA(x) add... | ... | SOA(new)
//   full zone:    SOA(new) followed directly by ordinary records — the
//                 master chose AXFR-style fallback; we hand the already
//                 downloaded file to the normal AXFR conversion path.
// ============================================================

type ixfrOutcome int

const (
	ixfrFailed ixfrOutcome = iota
	ixfrUnchanged
	ixfrApplied
	ixfrFullTransfer
)

type ixfrStats struct {
	newSerial uint64
	deleted   int
	added     int
	skipped   int
}

// convertRPZRecord converts one raw Komdigi CNAME record (as fields) into the
// stored-file form: strict owner validation, "CNAME ." for NXDOMAIN or an A
// record when redirectIP is set. Mirrors convertRPZForKresd's per-record rules.
func convertRPZRecord(fields []string, redirectIP string) (string, bool) {
	if len(fields) < 4 {
		return "", false
	}
	cnameIdx := -1
	for i, f := range fields[1:] {
		if strings.ToUpper(f) == "CNAME" {
			cnameIdx = i + 1
			break
		}
	}
	if cnameIdx == -1 || cnameIdx+1 >= len(fields) {
		return "", false // only CNAME records are RPZ rules we keep
	}
	if !isValidRPZOwner(fields[0]) {
		return "", false
	}
	target := fields[cnameIdx+1]
	if target != "." && !strings.HasPrefix(strings.ToLower(target), "rpz-") {
		if redirectIP != "" {
			fields[cnameIdx] = "A"
			fields[cnameIdx+1] = redirectIP
		} else {
			fields[cnameIdx+1] = "."
		}
	}
	return strings.Join(fields, "\t"), true
}

// parseIXFR reads a dig IXFR response. For incremental responses it returns
// ops: owner -> "" (delete) or converted replacement/addition line. Detection
// of a full-zone fallback happens at the second record, before any heavy work.
func parseIXFR(r io.Reader, redirectIP string) (outcome ixfrOutcome, stats ixfrStats, ops map[string]string, err error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 64*1024)
	ops = map[string]string{}

	recordNo := 0
	soaSeen := 0
	deleting := false
	var headerSerial uint64

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || line[0] == ';' || line[0] == '$' {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		isSOA := false
		for _, f := range fields[1:] {
			if strings.EqualFold(f, "SOA") {
				isSOA = true
				break
			}
		}
		recordNo++

		if recordNo == 1 {
			if !isSOA {
				return ixfrFailed, stats, nil, fmt.Errorf("respons tidak diawali SOA")
			}
			headerSerial = parseSOASerial(line)
			stats.newSerial = headerSerial
			soaSeen = 1
			continue
		}

		if recordNo == 2 {
			if !isSOA {
				// Ordinary record right after the header = AXFR-style full zone.
				return ixfrFullTransfer, stats, nil, nil
			}
			// SOA #2 with the OLD serial starts the first deletion block.
			deleting = true
			soaSeen++
			continue
		}

		if isSOA {
			soaSeen++
			// SOA toggles between deletion and addition blocks; the final SOA
			// (serial == header) closes the stream.
			deleting = !deleting
			continue
		}

		owner := fields[0]
		if deleting {
			ops[owner] = ""
			stats.deleted++
		} else {
			if converted, ok := convertRPZRecord(fields, redirectIP); ok {
				ops[owner] = converted
				stats.added++
			} else {
				stats.skipped++
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return ixfrFailed, stats, nil, err
	}
	if recordNo == 0 {
		return ixfrFailed, stats, nil, fmt.Errorf("respons kosong")
	}
	if recordNo == 1 {
		// Single SOA = our serial is current.
		return ixfrUnchanged, stats, nil, nil
	}
	if soaSeen < 3 {
		return ixfrFailed, stats, nil, fmt.Errorf("struktur IXFR tidak lengkap (%d SOA)", soaSeen)
	}
	return ixfrApplied, stats, ops, nil
}

// rewriteSOASerial replaces the serial field in a stored SOA line.
func rewriteSOASerial(line string, serial uint64) string {
	fields := strings.Fields(line)
	for i, f := range fields {
		if strings.EqualFold(f, "SOA") && i+3 < len(fields) {
			fields[i+3] = strconv.FormatUint(serial, 10)
			return strings.Join(fields, "\t")
		}
	}
	return line
}

// applyIXFROps streams the stored zone file, drops/replaces owners named in
// ops, appends pure additions, stamps the new serial, and atomically renames
// the result into place. The original file is untouched until the rename.
func applyIXFROps(rpzFile string, ops map[string]string, newSerial uint64) error {
	in, err := os.Open(rpzFile)
	if err != nil {
		return err
	}
	defer in.Close()

	tmpPath := rpzFile + ".ixfr-apply"
	out, err := os.Create(tmpPath)
	if err != nil {
		return err
	}
	defer func() {
		out.Close()
		os.Remove(tmpPath) // no-op after successful rename
	}()

	writer := bufio.NewWriterSize(out, 256*1024)
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 64*1024), 64*1024)

	pending := make(map[string]string, len(ops))
	for k, v := range ops {
		pending[k] = v
	}

	for scanner.Scan() {
		line := scanner.Text()
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		// Stamp the new serial on the (single) SOA line.
		upper := strings.ToUpper(line)
		if strings.Contains(upper, "\tSOA\t") || strings.Contains(upper, " SOA ") {
			writer.WriteString(rewriteSOASerial(line, newSerial))
			writer.WriteString("\n")
			continue
		}
		if replacement, hit := pending[fields[0]]; hit {
			delete(pending, fields[0])
			if replacement == "" {
				continue // deletion
			}
			writer.WriteString(replacement) // in-place replacement
			writer.WriteString("\n")
			continue
		}
		writer.WriteString(line)
		writer.WriteString("\n")
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	// Whatever remains in pending are pure additions (deletes for owners we
	// never had are safely ignored).
	for _, replacement := range pending {
		if replacement == "" {
			continue
		}
		writer.WriteString(replacement)
		writer.WriteString("\n")
	}
	if err := writer.Flush(); err != nil {
		return err
	}
	if err := out.Sync(); err != nil {
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, rpzFile)
}

// tryIXFR downloads and applies an incremental update from master. On
// ixfrFullTransfer the downloaded raw zone is left at fullPath for the caller
// to feed into the normal AXFR conversion path (no second download).
func (s *Server) tryIXFR(master, zoneName, rpzFile, redirectIP string, localSerial uint64) (ixfrOutcome, ixfrStats, string) {
	tmpPath := rpzFile + ".ixfr"
	os.Remove(tmpPath)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", "-c", fmt.Sprintf(
		"dig IXFR=%d %s @%s +noidnout +tcp +time=300 +tries=2 +nocomments +nostats +nocmd > %s 2>/dev/null",
		localSerial, zoneName, master, tmpPath))
	if err := cmd.Run(); err != nil {
		os.Remove(tmpPath)
		log.Printf("RPZ IXFR: dig gagal dari %s: %v", master, err)
		return ixfrFailed, ixfrStats{}, ""
	}

	f, err := os.Open(tmpPath)
	if err != nil {
		return ixfrFailed, ixfrStats{}, ""
	}
	outcome, stats, ops, perr := parseIXFR(f, redirectIP)
	f.Close()

	switch outcome {
	case ixfrFullTransfer:
		// Caller reuses the already-downloaded file as an AXFR result.
		log.Printf("RPZ IXFR: %s menjawab full zone (fallback AXFR-style) — file dipakai ulang", master)
		return ixfrFullTransfer, stats, tmpPath
	case ixfrUnchanged:
		os.Remove(tmpPath)
		return ixfrUnchanged, stats, ""
	case ixfrApplied:
		os.Remove(tmpPath)
		if err := applyIXFROps(rpzFile, ops, stats.newSerial); err != nil {
			log.Printf("RPZ IXFR: gagal menerapkan diff: %v — fallback ke AXFR", err)
			return ixfrFailed, stats, ""
		}
		log.Printf("RPZ IXFR: serial %d -> %d diterapkan (%d hapus, %d tambah, %d skip)",
			localSerial, stats.newSerial, stats.deleted, stats.added, stats.skipped)
		return ixfrApplied, stats, ""
	default:
		os.Remove(tmpPath)
		if perr != nil {
			log.Printf("RPZ IXFR: parse gagal dari %s: %v — fallback ke AXFR", master, perr)
		}
		return ixfrFailed, stats, ""
	}
}
