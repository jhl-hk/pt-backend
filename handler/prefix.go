package handler

import (
	"bufio"
	"bytes"
	"io"
	"os"
	"strconv"
	"strings"
)

const RootASN = 215172

var Tier1ASNs = map[int]bool{
	215172: true,
	44324:  true,
	139317: true,
	199310: true,
	213605: true,
}

type PrefixPath struct {
	Prefix    string
	Path      []int
	OriginASN int
}

// LoadASNs parses a BIRD-style ASN filter file into a set.
// Expected format:
//
//	define as_moedove_asns = [
//	    112, 1405, ...
//	];
func LoadASNs(path string) (map[int]bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return loadASNs(f)
}

// LoadASNsFromBytes parses a BIRD-style ASN filter from raw bytes.
func LoadASNsFromBytes(data []byte) (map[int]bool, error) {
	return loadASNs(bytes.NewReader(data))
}

func loadASNs(r io.Reader) (map[int]bool, error) {
	asns := make(map[int]bool)
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := scanner.Text()
		// Skip header and closing lines.
		if strings.HasPrefix(line, "define") || strings.TrimSpace(line) == "];" {
			continue
		}
		for _, field := range strings.Split(line, ",") {
			field = strings.TrimSpace(field)
			if field == "" {
				continue
			}
			asn, err := strconv.Atoi(field)
			if err != nil {
				continue
			}
			asns[asn] = true
		}
	}
	return asns, scanner.Err()
}

// ParsePrefixPaths parses a BIRD routing table dump and returns each prefix
// with its filtered AS path.
//
// Path rules:
//   - Consecutive duplicate ASNs (BGP prepending) are collapsed to one.
//   - Only ASNs present in the known asns set are kept; unknown transit ASNs
//     (e.g. 3491, 2914) are skipped and the walk continues to the next known ASN.
//   - Traversal stops if RootASN (215172) is encountered a second time.
func ParsePrefixPaths(outputPath string, asns map[int]bool) ([]PrefixPath, error) {
	f, err := os.Open(outputPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return parsePrefixPaths(f, asns)
}

// ParsePrefixPathsFromBytes parses a BIRD routing table dump from raw bytes.
func ParsePrefixPathsFromBytes(data []byte, asns map[int]bool) ([]PrefixPath, error) {
	return parsePrefixPaths(bytes.NewReader(data), asns)
}

func parsePrefixPaths(r io.Reader, asns map[int]bool) ([]PrefixPath, error) {
	var results []PrefixPath
	var currentPrefix string

	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)

		// Prefix line: not indented, contains "unicast"
		if !strings.HasPrefix(line, "\t") && !strings.HasPrefix(line, " ") &&
			strings.Contains(line, "unicast") {
			if fields := strings.Fields(line); len(fields) > 0 {
				currentPrefix = fields[0]
			}
			continue
		}

		if strings.HasPrefix(trimmed, "BGP.as_path:") && currentPrefix != "" {
			raw := strings.TrimSpace(strings.TrimPrefix(trimmed, "BGP.as_path:"))
			path := buildPath(raw, asns)
			results = append(results, PrefixPath{
				Prefix:    currentPrefix,
				Path:      path,
				OriginASN: originASN(raw),
			})
			currentPrefix = ""
		}
	}
	return results, scanner.Err()
}

// originASN returns the rightmost ASN in the raw AS path string.
func originASN(pathStr string) int {
	fields := strings.Fields(pathStr)
	for i := len(fields) - 1; i >= 0; i-- {
		asn, err := strconv.Atoi(fields[i])
		if err == nil {
			return asn
		}
	}
	return -1
}

// IndexByASN builds a map from origin ASN to the prefixes it originates.
func IndexByASN(paths []PrefixPath) map[int][]PrefixPath {
	idx := make(map[int][]PrefixPath)
	for _, p := range paths {
		idx[p.OriginASN] = append(idx[p.OriginASN], p)
	}
	return idx
}

// buildPath deduplicates consecutive ASNs in pathStr, then applies path rules:
//
//   - If every ASN in the deduped path is in the known asns set, return the
//     full deduped path (e.g. [215172, 153376] or [215172, 44324, 214575]).
//
//   - If any unknown transit ASN (not in asns) is present, return
//     [0, lastKnown] where lastKnown is the rightmost asns-list member in the
//     path and 0 represents the unresolved transit cloud (AS0).
//
// Traversal halts if RootASN (215172) reappears after position 0 (loop guard).
func buildPath(pathStr string, asns map[int]bool) []int {
	fields := strings.Fields(pathStr)

	// Step 1: collapse consecutive duplicates.
	deduped := make([]int, 0, len(fields))
	prev := -1
	for _, f := range fields {
		asn, err := strconv.Atoi(f)
		if err != nil {
			continue
		}
		if asn == RootASN && len(deduped) > 0 {
			break
		}
		if asn != prev {
			deduped = append(deduped, asn)
			prev = asn
		}
	}

	// Step 2: check whether any unknown transit ASN exists.
	hasUnknown := false
	lastKnown := -1
	for _, asn := range deduped {
		if asns[asn] {
			lastKnown = asn
		} else {
			hasUnknown = true
		}
	}

	// If the origin (last ASN) is itself a Tier1, the path is just that Tier1.
	if lastKnown != -1 && Tier1ASNs[lastKnown] {
		return []int{lastKnown}
	}

	if hasUnknown {
		// Unknown transit present: collapse to [0, lastKnown].
		if lastKnown == -1 {
			return []int{0}
		}
		return []int{0, lastKnown}
	}

	// If another Tier1 follows 215172, that Tier1 is the relevant entry point;
	// drop 215172 from the front.
	if len(deduped) >= 2 && deduped[0] == RootASN {
		for _, asn := range deduped[1:] {
			if Tier1ASNs[asn] {
				return deduped[1:]
			}
		}
	}

	return deduped
}
