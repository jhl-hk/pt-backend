package main

import (
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/jianyuelab/pt-backend/handler"
	"github.com/jianyuelab/pt-backend/web"
	"github.com/joho/godotenv"
	"github.com/melbahja/goph"
	"golang.org/x/crypto/ssh"
)

func main() {
	if err := godotenv.Load(); err != nil {
		log.Fatalf("load .env: %v", err)
	}

	// The key in .env has indented lines; strip leading/trailing whitespace per line.
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

	// Update the list and prefix
	client.Run("sudo /etc/bird/update-moedove.sh")
	asnsData, err := client.Run("sudo cat /etc/bird/as_moedove_asns.conf")
	if err != nil {
		log.Println("Failed to get asns data")
	}

	prefixData, err := client.Run("sudo birdc s r all")
	if err != nil {
		log.Println("Failed to get prefix data")
	}

	asns, err := handler.LoadASNsFromBytes(asnsData)
	if err != nil {
		log.Fatalf("load asns: %v", err)
	}

	paths, err := handler.ParsePrefixPathsFromBytes(prefixData, asns)
	if err != nil {
		log.Fatalf("parse output: %v", err)
	}

	index := handler.IndexByASN(paths)

	mux := http.NewServeMux()
	web.NewServer(index).RegisterRoutes(mux)

	log.Println("listening on :8080")
	log.Fatal(http.ListenAndServe(":8080", mux))
}
