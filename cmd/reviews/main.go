package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/ehrlich-b/reviews/internal/db"
	"github.com/ehrlich-b/reviews/internal/github"
	"github.com/ehrlich-b/reviews/internal/server"
	reviewsync "github.com/ehrlich-b/reviews/internal/sync"
)

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "sync":
			syncCmd(os.Args[2:])
			return
		}
	}
	serveCmd()
}

func serveCmd() {
	port := flag.Int("port", 8080, "HTTP port")
	dbPath := flag.String("db", defaultDBPath(), "SQLite database path")
	flag.Parse()

	store, err := db.Open(*dbPath)
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	defer store.Close()

	var syncer *reviewsync.Syncer
	token := os.Getenv("GITHUB_TOKEN")
	if token != "" {
		gh := github.NewClient(token)
		syncer = reviewsync.New(gh, store)
	}

	srv := server.New(store, syncer, parseOrgs())

	log.Printf("listening on :%d (db: %s)", *port, *dbPath)
	log.Fatal(http.ListenAndServe(fmt.Sprintf(":%d", *port), srv))
}

func syncCmd(args []string) {
	fs := flag.NewFlagSet("sync", flag.ExitOnError)
	dbPath := fs.String("db", defaultDBPath(), "SQLite database path")
	verbose := fs.Bool("verbose", false, "per-repo detail")
	fs.Parse(args)

	token := os.Getenv("GITHUB_TOKEN")
	if token == "" {
		log.Fatal("GITHUB_TOKEN environment variable is required")
	}

	store, err := db.Open(*dbPath)
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	defer store.Close()

	gh := github.NewClient(token)
	syncer := reviewsync.New(gh, store)
	sum, err := syncer.Run(*verbose, parseOrgs())
	if err != nil {
		log.Fatalf("sync: %v", err)
	}

	fmt.Printf("%d PRs across %d repos: %d needs review, %d author's court, %d approved, %d skipped\n",
		sum.Total, sum.Repos, sum.NeedsReview, sum.AuthorCourt, sum.Approved, sum.Skipped)
}

func parseOrgs() []string {
	var orgs []string
	if v := os.Getenv("GITHUB_ORGS"); v != "" {
		for _, o := range strings.Split(v, ",") {
			if o = strings.TrimSpace(o); o != "" {
				orgs = append(orgs, o)
			}
		}
	}
	return orgs
}

func defaultDBPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".reviews", "reviews.db")
}
