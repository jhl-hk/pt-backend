package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/jianyuelab/pt-backend/db"
	"github.com/jianyuelab/pt-backend/handler"
	"github.com/jianyuelab/pt-backend/web"
	"github.com/joho/godotenv"
	"github.com/melbahja/goph"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/pgdialect"
	"github.com/uptrace/bun/driver/pgdriver"
	"golang.org/x/crypto/ssh"
)

func main() {
	// Load .env if present; fall back to environment variables silently.
	if err := godotenv.Load(); err != nil && !os.IsNotExist(err) {
		log.Fatalf("load .env: %v", err)
	}

	// --- PostgreSQL (connect first so server can start immediately) ---
	log.Println("connecting to database...")
	sqldb := sql.OpenDB(pgdriver.NewConnector(pgdriver.WithDSN(os.Getenv("DATABASE_URL"))))
	bunDB := bun.NewDB(sqldb, pgdialect.New())
	defer bunDB.Close()

	ctx := context.Background()
	if err := db.CreateTable(ctx, bunDB); err != nil {
		log.Fatalf("create table: %v", err)
	}
	if err := db.CreatePrefixTable(ctx, bunDB); err != nil {
		log.Fatalf("create prefix table: %v", err)
	}
	log.Println("database ready")

	// --- Load initial data from DB ---
	asns := loadASNsFromDB(ctx, bunDB)
	paths := loadPathsFromDB(ctx, bunDB)
	orgMap := loadOrgMapFromDB(ctx, bunDB)

	// --- HTTP server (starts immediately with DB data) ---
	log.Println("building route index...")
	srv := web.NewServer(handler.IndexByASN(paths), bunDB)
	srv.SetOrgMap(orgMap)
	log.Println("route index ready")

	var asnsMu sync.RWMutex

	// syncRipeDB fetches APNIC + ARIN + RIPE DBs, merges them
	// (priority: APNIC < ARIN < RIPE), and updates srv orgMap and DB.
	syncRipeDB := func() {
		asnsMu.RLock()
		a := asns
		asnsMu.RUnlock()

		log.Println("syncing IRR databases (APNIC + ARIN + RIPE)...")

		om := make(map[int]handler.OrgInfo)

		apnicMap, err := handler.LoadAPNICOrgMap(a)
		if err != nil {
			log.Printf("apnic db load failed: %v", err)
		} else {
			for asn, info := range apnicMap {
				om[asn] = info
			}
			log.Printf("APNIC DB loaded: %d entries", len(apnicMap))
		}

		arinMap, err := handler.LoadARINOrgMap(a)
		if err != nil {
			log.Printf("arin db load failed: %v", err)
		} else {
			for asn, info := range arinMap {
				om[asn] = info
			}
			log.Printf("ARIN DB loaded: %d entries", len(arinMap))
		}

		ripeMap, err := handler.LoadRipeOrgMap(a)
		if err != nil {
			log.Printf("ripe db sync failed: %v", err)
		} else {
			for asn, info := range ripeMap {
				om[asn] = info
			}
			log.Printf("RIPE DB loaded: %d entries", len(ripeMap))
		}

		srv.SetOrgMap(om)
		log.Printf("IRR sync done: %d total entries", len(om))

		// Only upsert ASNs that have IRR data — never overwrite with empty values.
		records := make([]db.ASNRecord, 0, len(om))
		for asn, info := range om {
			records = append(records, db.ASNRecord{
				ID:          asn,
				Country:     info.Country,
				Org:         info.Handle,
				OrgName:     info.Name,
				SponsorOrg:  info.SponsorHandle,
				SponsorName: info.SponsorName,
				Whois:       info.Whois,
				UpdatedAt:   time.Now(),
			})
		}
		if err := db.UpsertASNs(ctx, bunDB, records); err != nil {
			log.Printf("upsert asns: %v", err)
			return
		}
		log.Printf("upserted %d ASN records", len(records))
	}

	// Initial IRR DB sync in background.
	go syncRipeDB()

	// --- SSH client (background; server already running with DB data) ---
	go func() {
		client, err := newSSHClient()
		if err != nil {
			log.Printf("ssh connect: %v", err)
			return
		}
		defer client.Close()

		// syncPrefixes re-fetches all feeds and updates the index.
		syncPrefixes := func() {
			asnsMu.RLock()
			a := asns
			asnsMu.RUnlock()

			log.Println("syncing prefix data...")
			feeds, err := fetchFeeds(client)
			if err != nil {
				log.Printf("fetch feeds: %v", err)
				return
			}
			newPaths, err := handler.ParseMultipleFeedsFromBytes(feeds, a)
			if err != nil {
				log.Printf("parse feeds: %v", err)
				return
			}
			srv.SetIndex(handler.IndexByASN(newPaths))
			if err := db.UpsertPrefixes(ctx, bunDB, prefixRecords(newPaths)); err != nil {
				log.Printf("upsert prefixes: %v", err)
			}
			log.Printf("prefix index updated: %d paths", len(newPaths))
		}

		// Initial data fetch from router.
		client.Run("sudo /etc/bird/update-moedove.sh")
		if asnsData, err := client.Run("sudo cat /etc/bird/as_moedove_asns.conf"); err != nil {
			log.Printf("fetch asns from router: %v", err)
		} else if newAsns, err := handler.LoadASNsFromBytes(asnsData); err != nil {
			log.Printf("parse asns: %v", err)
		} else {
			asnsMu.Lock()
			asns = newAsns
			asnsMu.Unlock()
			log.Printf("asns loaded from router: %d entries", len(newAsns))

			// Seed any new ASNs to DB.
			seedRecords := make([]db.ASNRecord, 0, len(newAsns))
			for asn := range newAsns {
				seedRecords = append(seedRecords, db.ASNRecord{ID: asn, UpdatedAt: time.Now()})
			}
			if err := db.SeedASNs(ctx, bunDB, seedRecords); err != nil {
				log.Printf("seed asns: %v", err)
			} else {
				log.Printf("seeded %d ASNs", len(seedRecords))
			}
		}

		log.Printf("fetching %d feed(s)...", len(feedCmds))
		syncPrefixes()

		// Every 10 minutes: refresh prefix data.
		prefixTicker := time.NewTicker(10 * time.Minute)
		defer prefixTicker.Stop()

		// Daily at 00:00: re-sync IRR databases + update BIRD ASN list.
		go func() {
			for {
				now := time.Now()
				next := time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, now.Location())
				time.Sleep(time.Until(next))

				log.Println("daily: running update-moedove.sh on router...")
				if _, err := client.Run("sudo /etc/bird/update-moedove.sh"); err != nil {
					log.Printf("daily: update-moedove.sh failed: %v", err)
				}

				newAsnsData, err := client.Run("sudo cat /etc/bird/as_moedove_asns.conf")
				if err != nil {
					log.Printf("daily: fetch asns failed: %v", err)
				} else if newAsns, err := handler.LoadASNsFromBytes(newAsnsData); err != nil {
					log.Printf("daily: parse asns failed: %v", err)
				} else {
					asnsMu.Lock()
					asns = newAsns
					asnsMu.Unlock()
					log.Printf("daily: asns updated: %d entries", len(newAsns))
				}

				// Delete cached IRR files so they are re-downloaded fresh.
				for _, f := range []string{"temp/ripe.db.gz", "temp/arin.db.gz", "temp/apnic.db.aut-num.gz", "temp/apnic.db.organisation.gz"} {
					os.Remove(f)
				}

				syncRipeDB()
			}
		}()

		for range prefixTicker.C {
			syncPrefixes()
		}
	}()

	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)

	log.Println("listening on :8080")
	log.Fatal(http.ListenAndServe(":8080", corsMiddleware(mux)))
}

// newSSHClient builds and connects the SSH client from environment variables.
func newSSHClient() (*goph.Client, error) {
	rawKey := os.Getenv("private_key")
	lines := strings.Split(rawKey, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimSpace(l)
	}
	pemBytes := []byte(strings.Join(lines, "\n"))

	signer, err := ssh.ParsePrivateKey(pemBytes)
	if err != nil {
		return nil, err
	}
	return goph.New(os.Getenv("user"), os.Getenv("host"), goph.Auth{ssh.PublicKeys(signer)})
}

// loadASNsFromDB returns the set of ASN IDs stored in the database.
// Returns an empty map (never nil) on error.
func loadASNsFromDB(ctx context.Context, bunDB *bun.DB) map[int]bool {
	ids, err := db.LoadAllASNIDs(ctx, bunDB)
	if err != nil {
		log.Printf("load asns from DB: %v", err)
		return make(map[int]bool)
	}
	m := make(map[int]bool, len(ids))
	for _, id := range ids {
		m[id] = true
	}
	log.Printf("loaded %d ASNs from DB", len(m))
	return m
}

// loadPathsFromDB reconstructs []PrefixPath from the prefixes table.
func loadPathsFromDB(ctx context.Context, bunDB *bun.DB) []handler.PrefixPath {
	records, err := db.LoadAllPrefixes(ctx, bunDB)
	if err != nil {
		log.Printf("load prefixes from DB: %v", err)
		return nil
	}
	var paths []handler.PrefixPath
	for _, r := range records {
		var rawPaths [][]int
		if err := json.Unmarshal(r.Paths, &rawPaths); err != nil {
			continue
		}
		for _, p := range rawPaths {
			paths = append(paths, handler.PrefixPath{
				Prefix:    r.Prefix,
				Path:      p,
				OriginASN: r.OriginASN,
			})
		}
	}
	log.Printf("loaded %d prefix paths from DB", len(paths))
	return paths
}

// loadOrgMapFromDB reconstructs the orgMap from the asns table.
func loadOrgMapFromDB(ctx context.Context, bunDB *bun.DB) map[int]handler.OrgInfo {
	var records []db.ASNRecord
	if err := bunDB.NewSelect().Model(&records).
		Where("org != '' OR org_name != '' OR country != ''").
		Scan(ctx); err != nil {
		log.Printf("load org map from DB: %v", err)
		return nil
	}
	m := make(map[int]handler.OrgInfo, len(records))
	for _, r := range records {
		m[r.ID] = handler.OrgInfo{
			Handle:        r.Org,
			Name:          r.OrgName,
			Country:       r.Country,
			SponsorHandle: r.SponsorOrg,
			SponsorName:   r.SponsorName,
			Whois:         r.Whois,
		}
	}
	log.Printf("loaded org info for %d ASNs from DB", len(m))
	return m
}

var collectors = []string{"211575", "215172", "213605", "139317", "51087", "202734", "44324"}

var feedCmds = func() []string {
	cmds := make([]string, len(collectors))
	for i, c := range collectors {
		cmds[i] = "sudo birdc show route protocol as" + c + "_collector all"
	}
	return cmds
}()

func prefixRecords(paths []handler.PrefixPath) []db.PrefixRecord {
	// Group paths by prefix to build the combined paths JSON per prefix.
	type entry struct {
		originASN int
		paths     [][]int
	}
	grouped := make(map[string]*entry, len(paths))
	order := make([]string, 0, len(paths))
	for _, p := range paths {
		if e, ok := grouped[p.Prefix]; ok {
			e.paths = append(e.paths, p.Path)
		} else {
			grouped[p.Prefix] = &entry{originASN: p.OriginASN, paths: [][]int{p.Path}}
			order = append(order, p.Prefix)
		}
	}
	now := time.Now()
	records := make([]db.PrefixRecord, 0, len(grouped))
	for _, prefix := range order {
		e := grouped[prefix]
		raw, _ := json.Marshal(e.paths)
		records = append(records, db.PrefixRecord{
			Prefix:    prefix,
			OriginASN: e.originASN,
			Paths:     raw,
			UpdatedAt: now,
		})
	}
	return records
}

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func fetchFeeds(client *goph.Client) ([][]byte, error) {
	feeds := make([][]byte, len(feedCmds))
	errs := make([]error, len(feedCmds))
	var wg sync.WaitGroup
	for i, cmd := range feedCmds {
		wg.Add(1)
		go func(i int, cmd string) {
			defer wg.Done()
			feeds[i], errs[i] = client.Run(cmd)
		}(i, cmd)
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return nil, err
		}
	}
	return feeds, nil
}
