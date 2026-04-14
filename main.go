package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/jianyuelab/pt-backend/bgp"
	"github.com/jianyuelab/pt-backend/config"
	"github.com/jianyuelab/pt-backend/db"
	"github.com/jianyuelab/pt-backend/handler"
	"github.com/jianyuelab/pt-backend/web"
)

func main() {
	configPath := "config.yaml"

	// 1. Load Initial Config
	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	// 2. Initialize DB
	_ = db.NewDatabase()

	// 3. Initialize BGP Collector
	collector := bgp.NewCollector()

	// 4. Initialize BGP Handler
	bgpHandler := handler.NewBGPHandler(collector)

	// 5. Initialize Web Server
	webServer := web.NewServer(8080, bgpHandler)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		log.Println("Shutting down...")
		cancel()
	}()

	// Start BGP collector
	log.Printf("Starting BGP Collector (AS%d, RouterID: %s, Port: %d)...",
		cfg.Global.ASN, cfg.Global.RouterID, cfg.Global.Port)
	if err := collector.Start(ctx, cfg.Global.ASN, cfg.Global.RouterID); err != nil {
		log.Fatalf("Failed to start collector: %v", err)
	}

	// Initial neighbor sync
	syncNeighbors(ctx, collector, cfg.Neighbors)

	bgpHandler.StartMonitoring(ctx)

	// Watch config file for changes
	go watchConfig(ctx, configPath, collector)

	// Start Web server in background
	go func() {
		if err := webServer.Start(); err != nil {
			log.Fatalf("Web server failed: %v", err)
		}
	}()

	log.Println("BGP Collector and Web server are running. Hot-reload enabled for config.yaml.")
	<-ctx.Done()
}

func syncNeighbors(ctx context.Context, c *bgp.Collector, target []config.Neighbor) {
	current, err := c.ListNeighbors(ctx)
	if err != nil {
		log.Printf("Sync error (listing): %v", err)
		return
	}

	targetMap := make(map[string]config.Neighbor)
	for _, n := range target {
		targetMap[n.Address] = n
	}

	// 1. Add or Update
	for addr, n := range targetMap {
		if curASN, exists := current[addr]; !exists || curASN != n.ASN {
			if exists {
				log.Printf("Updating neighbor %s (AS%d -> AS%d)", addr, curASN, n.ASN)
				c.DeleteNeighbor(ctx, addr)
			} else {
				log.Printf("Adding new neighbor %s (AS%d, multihop=%v)", addr, n.ASN, n.Multihop)
			}
			if err := c.AddNeighbor(ctx, addr, n.ASN, n.Multihop); err != nil {
				log.Printf("Failed to add/update neighbor %s: %v", addr, err)
			}
		}
	}

	// 2. Delete
	for addr := range current {
		if _, exists := targetMap[addr]; !exists {
			log.Printf("Removing neighbor %s", addr)
			if err := c.DeleteNeighbor(ctx, addr); err != nil {
				log.Printf("Failed to remove neighbor %s: %v", addr, err)
			}
		}
	}
}

func watchConfig(ctx context.Context, path string, c *bgp.Collector) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		log.Fatal(err)
	}
	defer watcher.Close()

	if err := watcher.Add(path); err != nil {
		log.Fatal(err)
	}

	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-watcher.Events:
			if !ok {
				return
			}
			// Write or Create event
			if event.Op&fsnotify.Write == fsnotify.Write {
				// Wait a brief moment to ensure file is completely written
				time.Sleep(100 * time.Millisecond)
				log.Println("Config file changed, reloading...")
				cfg, err := config.LoadConfig(path)
				if err != nil {
					log.Printf("Reload error: %v", err)
					continue
				}
				syncNeighbors(ctx, c, cfg.Neighbors)
			}
		case err, ok := <-watcher.Errors:
			if !ok {
				return
			}
			log.Printf("Watcher error: %v", err)
		}
	}
}
