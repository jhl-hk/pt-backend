package main

import (
	"log"
	"net/http"

	"github.com/jianyuelab/pt-backend/handler"
	"github.com/jianyuelab/pt-backend/web"
)

func main() {
	asns, err := handler.LoadASNs("asns")
	if err != nil {
		log.Fatalf("load asns: %v", err)
	}

	paths, err := handler.ParsePrefixPaths("output", asns)
	if err != nil {
		log.Fatalf("parse output: %v", err)
	}

	index := handler.IndexByASN(paths)

	mux := http.NewServeMux()
	web.NewServer(index).RegisterRoutes(mux)

	log.Println("listening on :8080")
	log.Fatal(http.ListenAndServe(":8080", mux))
}
