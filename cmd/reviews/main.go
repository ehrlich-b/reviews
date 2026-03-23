package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/ehrlich-b/reviews/internal/db"
	"github.com/ehrlich-b/reviews/internal/github"
	"github.com/ehrlich-b/reviews/internal/jira"
	"github.com/ehrlich-b/reviews/internal/server"
	"github.com/ehrlich-b/reviews/internal/slack"
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

	// Jira integration (optional)
	var jiraClient *jira.Client
	jiraBaseURL := os.Getenv("JIRA_BASE_URL")
	jiraEmail := os.Getenv("JIRA_EMAIL")
	jiraToken := os.Getenv("JIRA_TOKEN")
	if jiraBaseURL != "" && jiraEmail != "" && jiraToken != "" {
		jiraClient = jira.NewClient(jiraBaseURL, jiraEmail, jiraToken)
		log.Printf("jira integration enabled (%s)", jiraBaseURL)
		if syncer != nil {
			syncer.SetJiraClient(jiraClient)
		}
	}

	// Slack client (optional)
	var slackClient *slack.Client
	if slackToken := os.Getenv("SLACK_BOT_TOKEN"); slackToken != "" {
		slackClient = slack.NewClient(slackToken)
		log.Printf("slack client configured")
	}

	nagThresholdDays := 7
	if v := os.Getenv("NAG_THRESHOLD_DAYS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			nagThresholdDays = n
		}
	}

	cfg := server.Config{
		AdminToken:       os.Getenv("ADMIN_TOKEN"),
		SlackClient:      slackClient,
		NagEnabled:       os.Getenv("NAG_ENABLED") == "true",
		NagDryRun:        os.Getenv("NAG_DRY_RUN") == "true",
		NagThresholdDays: nagThresholdDays,
		JiraBaseURL:      jiraBaseURL,
	}

	srv := server.New(store, syncer, parseOrgs(), cfg)

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

	// Wire Jira if configured
	jiraBaseURL := os.Getenv("JIRA_BASE_URL")
	jiraEmail := os.Getenv("JIRA_EMAIL")
	jiraToken := os.Getenv("JIRA_TOKEN")
	if jiraBaseURL != "" && jiraEmail != "" && jiraToken != "" {
		syncer.SetJiraClient(jira.NewClient(jiraBaseURL, jiraEmail, jiraToken))
	}

	sum, err := syncer.Run(*verbose, parseOrgs())
	if err != nil {
		log.Fatalf("sync: %v", err)
	}

	fmt.Printf("%d PRs across %d repos: %d needs review, %d author's court, %d approved, %d skipped\n",
		sum.Total, sum.Repos, sum.NeedsReview, sum.AuthorCourt, sum.Approved, sum.Skipped)
}

func parseOrgs() []string {
	if orgs := loadConfig(); len(orgs) > 0 {
		return orgs
	}
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

func loadConfig() []string {
	home, _ := os.UserHomeDir()
	f, err := os.Open(filepath.Join(home, ".reviews", "config.json"))
	if err != nil {
		return nil
	}
	defer f.Close()
	var cfg struct {
		Org string `json:"org"`
	}
	if err := json.NewDecoder(f).Decode(&cfg); err != nil {
		log.Printf("parse config.json: %v", err)
		return nil
	}
	if cfg.Org != "" {
		return []string{cfg.Org}
	}
	return nil
}

func defaultDBPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".reviews", "reviews.db")
}
