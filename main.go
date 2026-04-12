package main

import (
	"context"
	"database/sql"
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
	if err := godotenv.Load(); err != nil {
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

	// --- HTTP server ---
	log.Println("building route index...")
	srv := web.NewServer(handler.IndexByASN(paths), bunDB)
	log.Println("route index ready")

	// syncRipeDB fetches RIPE DB, updates srv orgMap and DB org columns.
	syncRipeDB := func() {
		log.Println("syncing RIPE DB...")
		orgMap, err := handler.LoadRipeOrgMap(asns)
		if err != nil {
			log.Printf("ripe db sync failed: %v", err)
			return
		}
		srv.SetOrgMap(orgMap)
		log.Printf("RIPE DB loaded: %d entries", len(orgMap))

		records := make([]db.ASNRecord, 0, len(asns))
		for asn := range asns {
			rec := db.ASNRecord{ID: asn, UpdatedAt: time.Now()}
			if info, ok := orgMap[asn]; ok {
				rec.Org = info.Handle
				rec.OrgName = info.Name
				rec.SponsorOrg = info.SponsorHandle
				rec.SponsorName = info.SponsorName
				rec.Whois = info.Whois
			}
			records = append(records, rec)
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

	// Daily at 00:00: re-sync RIPE DB.
	go func() {
		for {
			now := time.Now()
			next := time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, now.Location())
			time.Sleep(time.Until(next))
			syncRipeDB()
		}
	}()

	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)

	log.Println("listening on :8080")
	log.Fatal(http.ListenAndServe(":8080", mux))
}

var collectors = []string{"as211575", "as215172", "as213605"}

var feedCmds = func() []string {
	cmds := make([]string, len(collectors))
	for i, c := range collectors {
		cmds[i] = "sudo birdc show route protocol " + c + "_collector all"
	}
	return cmds
}()

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
