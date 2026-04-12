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

	// --- SSH client ---
	rawKey := os.Getenv("private_key")
	lines := strings.Split(rawKey, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimSpace(l)
	}
	pemBytes := []byte(strings.Join(lines, "\n"))

	signer, err := ssh.ParsePrivateKey(pemBytes)
	if err != nil {
		log.Fatalf("ssh key: %v", err)
	}
	auth := goph.Auth{ssh.PublicKeys(signer)}

	client, err := goph.New(os.Getenv("user"), os.Getenv("host"), auth)
	if err != nil {
		log.Fatalf("ssh connect: %v", err)
	}
	defer client.Close()

	// --- Initial data fetch ---
	client.Run("sudo /etc/bird/update-moedove.sh")
	asnsData, err := client.Run("sudo cat /etc/bird/as_moedove_asns.conf")
	if err != nil {
		log.Println("Failed to get asns data")
	}

	asns, err := handler.LoadASNsFromBytes(asnsData)
	if err != nil {
		log.Fatalf("load asns: %v", err)
	}

	log.Printf("fetching %d feed(s)...", len(feedCmds))
	feeds, err := fetchFeeds(client)
	if err != nil {
		log.Printf("fetch feeds: %v", err)
	}
	log.Printf("feeds fetched: %d", len(feeds))

	paths, err := handler.ParseMultipleFeedsFromBytes(feeds, asns)
	if err != nil {
		log.Fatalf("parse feeds: %v", err)
	}
	log.Printf("parsed %d prefix paths from %d ASNs", len(paths), len(asns))

	// --- PostgreSQL ---
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

	// Seed all known ASNs immediately (skips existing rows).
	seedRecords := make([]db.ASNRecord, 0, len(asns))
	for asn := range asns {
		seedRecords = append(seedRecords, db.ASNRecord{ID: asn, UpdatedAt: time.Now()})
	}
	if err := db.SeedASNs(ctx, bunDB, seedRecords); err != nil {
		log.Printf("seed asns: %v", err)
	} else {
		log.Printf("seeded %d ASNs", len(seedRecords))
	}

	// Sync initial prefix data to DB.
	go func() {
		if err := db.UpsertPrefixes(ctx, bunDB, prefixRecords(paths)); err != nil {
			log.Printf("upsert prefixes: %v", err)
		} else {
			log.Printf("upserted %d prefix records", len(paths))
		}
	}()

	// --- HTTP server ---
	log.Println("building route index...")
	srv := web.NewServer(handler.IndexByASN(paths), bunDB)
	log.Println("route index ready")

	// syncRipeDB fetches APNIC + ARIN + RIPE DBs, merges them
	// (priority: APNIC < ARIN < RIPE), and updates srv orgMap and DB.
	syncRipeDB := func() {
		log.Println("syncing IRR databases (APNIC + ARIN + RIPE)...")

		orgMap := make(map[int]handler.OrgInfo)

		apnicMap, err := handler.LoadAPNICOrgMap(asns)
		if err != nil {
			log.Printf("apnic db load failed: %v", err)
		} else {
			for asn, info := range apnicMap {
				orgMap[asn] = info
			}
			log.Printf("APNIC DB loaded: %d entries", len(apnicMap))
		}

		arinMap, err := handler.LoadARINOrgMap(asns)
		if err != nil {
			log.Printf("arin db load failed: %v", err)
		} else {
			for asn, info := range arinMap {
				orgMap[asn] = info
			}
			log.Printf("ARIN DB loaded: %d entries", len(arinMap))
		}

		ripeMap, err := handler.LoadRipeOrgMap(asns)
		if err != nil {
			log.Printf("ripe db sync failed: %v", err)
		} else {
			for asn, info := range ripeMap {
				orgMap[asn] = info
			}
			log.Printf("RIPE DB loaded: %d entries", len(ripeMap))
		}

		srv.SetOrgMap(orgMap)
		log.Printf("IRR sync done: %d total entries", len(orgMap))

		// Only upsert ASNs that have IRR data — never overwrite with empty values.
		records := make([]db.ASNRecord, 0, len(orgMap))
		for asn, info := range orgMap {
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

	// syncPrefixes re-fetches all feeds and updates the index.
	syncPrefixes := func() {
		log.Println("syncing prefix data...")
		feeds, err := fetchFeeds(client)
		if err != nil {
			log.Printf("fetch feeds: %v", err)
			return
		}
		newPaths, err := handler.ParseMultipleFeedsFromBytes(feeds, asns)
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

	// Initial RIPE DB sync in background.
	go syncRipeDB()

	// Every 10 minutes: refresh prefix data.
	go func() {
		ticker := time.NewTicker(10 * time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			syncPrefixes()
		}
	}()

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

			// Re-fetch updated ASN list.
			newAsnsData, err := client.Run("sudo cat /etc/bird/as_moedove_asns.conf")
			if err != nil {
				log.Printf("daily: fetch asns failed: %v", err)
			} else {
				if newAsns, err := handler.LoadASNsFromBytes(newAsnsData); err != nil {
					log.Printf("daily: parse asns failed: %v", err)
				} else {
					asns = newAsns
					log.Printf("daily: asns updated: %d entries", len(asns))
				}
			}

			// Delete cached IRR files so they are re-downloaded fresh.
			for _, f := range []string{"temp/ripe.db.gz", "temp/arin.db.gz", "temp/apnic.db.aut-num.gz", "temp/apnic.db.organisation.gz"} {
				os.Remove(f)
			}

			syncRipeDB()
		}
	}()

	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)

	log.Println("listening on :8080")
	log.Fatal(http.ListenAndServe(":8080", corsMiddleware(mux)))
}

var collectors = []string{"as211575", "as215172", "as213605", "as139317", "as51087"}

var feedCmds = func() []string {
	cmds := make([]string, len(collectors))
	for i, c := range collectors {
		cmds[i] = "sudo birdc show route protocol " + c + "_collector all"
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
