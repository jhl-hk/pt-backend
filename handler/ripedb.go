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

// LoadRipeOrgMap uses ./ripe.db.gz if it exists, otherwise downloads it to a
// temp file (deleted after parsing). Only ASNs present in the filter set are
// returned; pass nil to return all.
func LoadRipeOrgMap(filter map[int]bool) (map[int]OrgInfo, error) {
	path, downloaded, err := resolveRipeDB()
	if err != nil {
		return nil, err
	}
	if downloaded {
		defer os.Remove(path)
	}

	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open ripe db: %w", err)
	}
	defer f.Close()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return nil, fmt.Errorf("open gzip: %w", err)
	}
	defer gz.Close()

	return parseRipeDB(gz, filter)
}

// resolveRipeDB returns the path to a ripe.db.gz file.
// If ./ripe.db.gz already exists it is used directly (downloaded=false).
// Otherwise the file is downloaded to a temp path (downloaded=true).
func resolveRipeDB() (path string, downloaded bool, err error) {
	if _, err := os.Stat(localRipeDB); err == nil {
		log.Printf("using existing %s", localRipeDB)
		return localRipeDB, false, nil
	}
	tmp, err := downloadToTemp()
	if err != nil {
		return "", false, err
	}
	return tmp, true, nil
}

// downloadToTemp streams the RIPE DB gzip to a temp file and returns its path.
func downloadToTemp() (string, error) {
	resp, err := http.Get(RipeDBURL)
	if err != nil {
		return "", fmt.Errorf("download ripe.db.gz: %w", err)
	}
	defer resp.Body.Close()

	total := resp.ContentLength // -1 if unknown

	tmp, err := os.CreateTemp("temp", "ripe.db.*.gz")
	if err != nil {
		return "", fmt.Errorf("create temp file: %w", err)
	}

	pr := &progressReader{r: resp.Body, total: total}
	if _, err := io.Copy(tmp, pr); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return "", fmt.Errorf("write temp file: %w", err)
	}
	log.Printf("ripe.db download complete: %.1f MB", float64(pr.written)/(1024*1024))

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
}

func (p *progressReader) Read(buf []byte) (int, error) {
	n, err := p.r.Read(buf)
	p.written += int64(n)
	if p.written-p.lastLogged >= progressInterval {
		p.lastLogged = p.written
		if p.total > 0 {
			log.Printf("ripe.db downloading... %.1f / %.1f MB (%.0f%%)",
				float64(p.written)/(1024*1024),
				float64(p.total)/(1024*1024),
				float64(p.written)/float64(p.total)*100)
		} else {
			log.Printf("ripe.db downloading... %.1f MB", float64(p.written)/(1024*1024))
		}
	}
	return n, err
}

// blockResult is the output from a single parsed RIPE DB block.
type blockResult struct {
	asn        int    // > 0 for aut-num blocks
	org        string // org handle referenced by this ASN
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
		country    string
		sponsorOrg string
		whois      string
	}
	autNumOrg := make(map[int]asnEntry) // asn -> org/sponsor/whois
	orgNames := make(map[string]string) // org handle -> org-name
	for res := range results {
		if res.asn > 0 && res.org != "" && (filter == nil || filter[res.asn]) {
			autNumOrg[res.asn] = asnEntry{org: res.org, country: res.country, sponsorOrg: res.sponsorOrg, whois: res.whois}
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
		result[asn] = OrgInfo{
			Handle:        entry.org,
			Name:          orgNames[entry.org],
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

	useful := (blockType == "aut-num" && res.asn > 0 && res.org != "") ||
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
