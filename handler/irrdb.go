package handler

import (
	"bufio"
	"compress/gzip"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
)

const RipeDBURL = "https://ftp.ripe.net/ripe/dbase/ripe.db.gz"
const ARINDbURL = "https://ftp.arin.net/pub/rr/arin.db.gz"
const APNICAutNumURL = "https://ftp.apnic.net/apnic/whois/apnic.db.aut-num.gz"
const APNICOrgURL = "https://ftp.apnic.net/apnic/whois/apnic.db.organisation.gz"

// OrgInfo holds the org handle and name for an ASN.
type OrgInfo struct {
	Handle        string // e.g. ORG-ACME-RIPE
	Name          string // e.g. ACME Corp
	Country       string // ISO 3166-1 alpha-2 from aut-num country: field
	SponsorHandle string // sponsoring-org handle (empty if none)
	SponsorName   string // resolved name of the sponsoring org
	Whois         string // raw aut-num block text from RIPE DB
}

const localRipeDB = "temp/ripe.db.gz"
const localARINDB = "temp/arin.db.gz"
const localAPNICAutNum = "temp/apnic.db.aut-num.gz"
const localAPNICOrg = "temp/apnic.db.organisation.gz"

// LoadRipeOrgMap uses temp/ripe.db.gz if it exists, otherwise downloads it.
// Only ASNs present in the filter set are returned; pass nil to return all.
func LoadRipeOrgMap(filter map[int]bool) (map[int]OrgInfo, error) {
	return loadIRROrgMap(localRipeDB, RipeDBURL, "ripe.db", filter)
}

// LoadARINOrgMap uses temp/arin.db.gz if it exists, otherwise downloads it.
// Only ASNs present in the filter set are returned; pass nil to return all.
func LoadARINOrgMap(filter map[int]bool) (map[int]OrgInfo, error) {
	return loadIRROrgMap(localARINDB, ARINDbURL, "arin.db", filter)
}

// LoadAPNICOrgMap loads APNIC aut-num and organisation files, combines them,
// and returns an ASN→OrgInfo map. APNIC splits data by object type, so both
// files are needed: aut-num provides ASN→org mappings, organisation provides
// org handle→name/country. Uses local temp files if present, downloads otherwise.
func LoadAPNICOrgMap(filter map[int]bool) (map[int]OrgInfo, error) {
	autNumPath, autNumDownloaded, err := resolveDB(localAPNICAutNum, APNICAutNumURL, "apnic.db.aut-num")
	if err != nil {
		return nil, err
	}
	if autNumDownloaded {
		defer os.Remove(autNumPath)
	}

	orgPath, orgDownloaded, err := resolveDB(localAPNICOrg, APNICOrgURL, "apnic.db.organisation")
	if err != nil {
		return nil, err
	}
	if orgDownloaded {
		defer os.Remove(orgPath)
	}

	readers, closers, err := openGzipFiles(autNumPath, orgPath)
	if err != nil {
		return nil, fmt.Errorf("open apnic files: %w", err)
	}
	defer func() {
		for _, c := range closers {
			c()
		}
	}()

	// Parse both files as one combined RPSL stream.
	return parseRipeDB(io.MultiReader(readers...), filter)
}

// openGzipFiles opens multiple gzip files and returns their readers and
// closer functions (call each closer when done).
func openGzipFiles(paths ...string) ([]io.Reader, []func(), error) {
	readers := make([]io.Reader, 0, len(paths))
	closers := make([]func(), 0, len(paths))
	for _, p := range paths {
		f, err := os.Open(p)
		if err != nil {
			for _, c := range closers {
				c()
			}
			return nil, nil, err
		}
		gz, err := gzip.NewReader(f)
		if err != nil {
			f.Close()
			for _, c := range closers {
				c()
			}
			return nil, nil, err
		}
		readers = append(readers, gz)
		closers = append(closers, func() { gz.Close(); f.Close() })
	}
	return readers, closers, nil
}

// loadIRROrgMap is the generic loader for any gzip RPSL-format IRR database.
func loadIRROrgMap(localPath, downloadURL, label string, filter map[int]bool) (map[int]OrgInfo, error) {
	path, downloaded, err := resolveDB(localPath, downloadURL, label)
	if err != nil {
		return nil, err
	}
	if downloaded {
		defer os.Remove(path)
	}

	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", label, err)
	}
	defer f.Close()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return nil, fmt.Errorf("open gzip %s: %w", label, err)
	}
	defer gz.Close()

	return parseRipeDB(gz, filter)
}

// resolveDB returns the path to an IRR DB gzip file.
// If localPath already exists it is used directly; otherwise the file is
// downloaded from downloadURL to a temp path.
func resolveDB(localPath, downloadURL, label string) (path string, downloaded bool, err error) {
	if _, err := os.Stat(localPath); err == nil {
		log.Printf("using existing %s", localPath)
		return localPath, false, nil
	}
	tmp, err := downloadToTemp(downloadURL, label)
	if err != nil {
		return "", false, err
	}
	return tmp, true, nil
}

// downloadToTemp streams an IRR DB gzip from url to a temp file.
func downloadToTemp(url, label string) (string, error) {
	resp, err := http.Get(url)
	if err != nil {
		return "", fmt.Errorf("download %s: %w", label, err)
	}
	defer resp.Body.Close()

	total := resp.ContentLength

	tmp, err := os.CreateTemp("temp", label+".*.gz")
	if err != nil {
		return "", fmt.Errorf("create temp file: %w", err)
	}

	pr := &progressReader{r: resp.Body, total: total, label: label}
	if _, err := io.Copy(tmp, pr); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return "", fmt.Errorf("write temp file: %w", err)
	}
	log.Printf("%s download complete: %.1f MB", label, float64(pr.written)/(1024*1024))

	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return "", fmt.Errorf("close temp file: %w", err)
	}

	return tmp.Name(), nil
}

const progressInterval = 50 * 1024 * 1024 // log every 50 MB

type progressReader struct {
	r          io.Reader
	total      int64
	written    int64
	lastLogged int64
	label      string
}

func (p *progressReader) Read(buf []byte) (int, error) {
	n, err := p.r.Read(buf)
	p.written += int64(n)
	if p.written-p.lastLogged >= progressInterval {
		p.lastLogged = p.written
		if p.total > 0 {
			log.Printf("%s downloading... %.1f / %.1f MB (%.0f%%)", p.label,
				float64(p.written)/(1024*1024),
				float64(p.total)/(1024*1024),
				float64(p.written)/float64(p.total)*100)
		} else {
			log.Printf("%s downloading... %.1f MB", p.label, float64(p.written)/(1024*1024))
		}
	}
	return n, err
}

// blockResult is the output from a single parsed RIPE DB block.
type blockResult struct {
	asn        int    // > 0 for aut-num blocks
	org        string // org handle referenced by this ASN (RIPE/APNIC style)
	mntBy      string // mnt-by handle (ARIN: derive org by replacing MNT- → ARIN-)
	asName     string // as-name field (ARIN style fallback)
	descr      string // descr field (ARIN style fallback for org name)
	country    string // country code from aut-num block
	sponsorOrg string // sponsoring-org handle (aut-num blocks)
	whois      string // raw block text (aut-num blocks)
	handle     string // org handle defined by this organisation block
	name       string // org-name for this organisation block
}

// parseRipeDB reads the RIPE DB text using a producer→workers pipeline:
//   - 1 goroutine reads + decompresses lines, groups them into blank-line-separated
//     blocks, and sends each block to a channel.
//   - N worker goroutines (runtime.NumCPU) parse blocks concurrently.
//   - Results are collected and joined: ASN → OrgInfo.
//
// filter: if non-nil, only aut-num blocks whose ASN is in the set are kept.
func parseRipeDB(r io.Reader, filter map[int]bool) (map[int]OrgInfo, error) {
	numWorkers := runtime.NumCPU()

	blocks := make(chan []string, numWorkers*4)
	results := make(chan blockResult, numWorkers*4)

	// --- Producer: read lines, emit complete blocks ---
	var scanErr error
	go func() {
		defer close(blocks)
		scanner := bufio.NewScanner(r)
		scanner.Buffer(make([]byte, 0, 1*1024*1024), 10*1024*1024)

		var block []string
		for scanner.Scan() {
			line := scanner.Text()
			if strings.TrimSpace(line) == "" {
				if len(block) > 0 {
					blocks <- block
					block = nil
				}
				continue
			}
			if strings.HasPrefix(line, "%") || strings.HasPrefix(line, "#") {
				continue
			}
			block = append(block, line)
		}
		if len(block) > 0 {
			blocks <- block
		}
		scanErr = scanner.Err()
	}()

	// --- Workers: parse blocks concurrently ---
	var wg sync.WaitGroup
	for i := 0; i < numWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for block := range blocks {
				if r, ok := parseBlock(block); ok {
					results <- r
				}
			}
		}()
	}

	// Close results once all workers finish.
	go func() {
		wg.Wait()
		close(results)
	}()

	// --- Collector ---
	type asnEntry struct {
		org        string
		asName     string // fallback when no org: (ARIN style)
		descr      string // fallback name when no org: (ARIN style)
		country    string
		sponsorOrg string
		whois      string
	}
	autNumOrg := make(map[int]asnEntry) // asn -> org/sponsor/whois
	orgNames := make(map[string]string) // org handle -> org-name
	for res := range results {
		if res.asn > 0 && (res.org != "" || res.asName != "") && (filter == nil || filter[res.asn]) {
			org := res.org
			// ARIN: derive org handle from mnt-by (MNT-XXX → ARIN-XXX).
			if org == "" && strings.HasPrefix(res.mntBy, "MNT-") {
				org = "ARIN-" + res.mntBy[len("MNT-"):]
			}
			autNumOrg[res.asn] = asnEntry{
				org: org, asName: res.asName, descr: res.descr,
				country: res.country, sponsorOrg: res.sponsorOrg, whois: res.whois,
			}
		}
		if res.handle != "" && res.name != "" {
			orgNames[res.handle] = res.name
		}
	}

	if scanErr != nil {
		return nil, fmt.Errorf("scan ripe.db: %w", scanErr)
	}

	// Join: ASN -> OrgInfo
	result := make(map[int]OrgInfo, len(autNumOrg))
	for asn, entry := range autNumOrg {
		// Prefer org-resolved name; fall back to descr or as-name for ARIN entries.
		name := orgNames[entry.org]
		if name == "" {
			name = entry.descr
		}
		if name == "" {
			name = entry.asName
		}
		result[asn] = OrgInfo{
			Handle:        entry.org,
			Name:          name,
			Country:       entry.country,
			SponsorHandle: entry.sponsorOrg,
			SponsorName:   orgNames[entry.sponsorOrg],
			Whois:         entry.whois,
		}
	}
	return result, nil
}

// parseBlock parses a single blank-line-delimited RIPE DB block.
// It handles two block types:
//   - aut-num: extracts ASN + org reference + raw whois text
//   - organisation: extracts handle + org-name
func parseBlock(lines []string) (blockResult, bool) {
	var res blockResult
	var blockType string // "aut-num" | "organisation" | ""

	for _, line := range lines {
		key, val, ok := cutField(line)
		if !ok {
			continue
		}
		switch key {
		case "aut-num":
			blockType = "aut-num"
			asnStr := strings.TrimPrefix(strings.ToUpper(val), "AS")
			asn, err := strconv.Atoi(asnStr)
			if err == nil {
				res.asn = asn
			}
		case "organisation":
			if blockType == "" {
				blockType = "organisation"
				res.handle = val
			}
		case "org":
			if blockType == "aut-num" {
				res.org = val
			}
		case "mnt-by":
			if blockType == "aut-num" && res.mntBy == "" {
				res.mntBy = val
			}
		case "as-name":
			if blockType == "aut-num" {
				res.asName = val
			}
		case "descr":
			if blockType == "aut-num" && res.descr == "" {
				res.descr = val
			}
		case "country":
			if blockType == "aut-num" && res.country == "" {
				res.country = strings.ToUpper(val)
			}
		case "sponsoring-org":
			if blockType == "aut-num" {
				res.sponsorOrg = val
			}
		case "org-name":
			if blockType == "organisation" {
				res.name = val
			}
		}
	}

	if blockType == "aut-num" && res.asn > 0 {
		res.whois = strings.Join(lines, "\n")
	}

	// Accept aut-num if it has either an org reference (RIPE/APNIC) or a
	// direct as-name (ARIN style without org: field).
	useful := (blockType == "aut-num" && res.asn > 0 && (res.org != "" || res.asName != "")) ||
		(blockType == "organisation" && res.handle != "" && res.name != "")
	return res, useful
}

// cutField splits "key:   value" into ("key", "value", true).
func cutField(line string) (key, val string, ok bool) {
	idx := strings.IndexByte(line, ':')
	if idx < 0 {
		return "", "", false
	}
	key = strings.ToLower(strings.TrimSpace(line[:idx]))
	val = strings.TrimSpace(line[idx+1:])
	return key, val, true
}
