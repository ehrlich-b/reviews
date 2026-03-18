package server

import (
	"database/sql"
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/ehrlich-b/reviews/internal/db"
	reviewsync "github.com/ehrlich-b/reviews/internal/sync"
)

//go:embed templates/*.html
var templatesFS embed.FS

type Server struct {
	store   *db.Store
	mux     *http.ServeMux
	tmpl    *template.Template
	syncer  *reviewsync.Syncer
	orgs    []string
	syncSem chan struct{} // buffered 1, prevents concurrent syncs
}

func New(store *db.Store, syncer *reviewsync.Syncer, orgs []string) *Server {
	funcMap := template.FuncMap{
		"reltime":   reltime,
		"shortRepo": shortRepo,
		"ciClass":   ciClass,
		"ciLabel":   ciLabel,
		"deref":     deref,
		"pluralize": pluralize,
		"join":      strings.Join,
	}

	tmpl := template.Must(template.New("").Funcs(funcMap).ParseFS(templatesFS, "templates/*.html"))

	s := &Server{
		store:   store,
		mux:     http.NewServeMux(),
		tmpl:    tmpl,
		syncer:  syncer,
		orgs:    orgs,
		syncSem: make(chan struct{}, 1),
	}
	s.routes()

	if syncer != nil {
		log.Printf("running initial sync...")
		if _, err := s.doSync(); err != nil {
			log.Printf("initial sync: %v", err)
		}
		go s.backgroundSync()
	}

	return s
}

func (s *Server) doSync() (*reviewsync.Summary, error) {
	select {
	case s.syncSem <- struct{}{}:
		defer func() { <-s.syncSem }()
		return s.syncer.Run(false, s.orgs)
	default:
		return nil, fmt.Errorf("sync already in progress")
	}
}

func (s *Server) backgroundSync() {
	ticker := time.NewTicker(10 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		if _, err := s.doSync(); err != nil {
			log.Printf("background sync: %v", err)
		}
	}
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

func (s *Server) routes() {
	s.mux.HandleFunc("GET /", s.handleIndex)
	s.mux.HandleFunc("POST /api/sync", s.handleSync)
}

func (s *Server) handleSync(w http.ResponseWriter, r *http.Request) {
	if s.syncer == nil {
		http.Error(w, "sync not configured (no GITHUB_TOKEN)", 503)
		return
	}
	sum, err := s.doSync()
	if err != nil {
		log.Printf("manual sync: %v", err)
		http.Error(w, err.Error(), 500)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(sum)
}

type ticketGroup struct {
	TicketKey    string
	PRs          []*db.PullRequest
	Repos        []string
	LastActivity string
}

type dashboardData struct {
	Viewer           string
	NeedsReview      []ticketGroup
	NeedsReviewCount int
	YourApproved     []*db.PullRequest
	YourChanges      []*db.PullRequest
	YourWaiting      []*db.PullRequest
	YourPRsCount     int
	TotalPRs         int
	TotalRepos       int
	LastSync         string
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	viewer, err := s.store.GetConfig("viewer")
	if err == sql.ErrNoRows {
		viewer = ""
	} else if err != nil {
		log.Printf("get viewer: %v", err)
		http.Error(w, "internal error", 500)
		return
	}

	prs, err := s.store.ListPRs()
	if err != nil {
		log.Printf("list PRs: %v", err)
		http.Error(w, "internal error", 500)
		return
	}

	repoCount, prCount, lastSync, err := s.store.GetSyncInfo()
	if err != nil {
		log.Printf("get sync info: %v", err)
		http.Error(w, "internal error", 500)
		return
	}

	data := buildDashboard(viewer, prs, repoCount, prCount, lastSync)

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.ExecuteTemplate(w, "base", data); err != nil {
		log.Printf("render: %v", err)
	}
}

func buildDashboard(viewer string, prs []*db.PullRequest, repoCount, prCount int, lastSync string) *dashboardData {
	data := &dashboardData{
		Viewer:     viewer,
		TotalPRs:   prCount,
		TotalRepos: repoCount,
		LastSync:   lastSync,
	}

	var needsReview []*db.PullRequest
	for _, pr := range prs {
		if pr.Author == viewer {
			data.YourPRsCount++
			switch {
			case pr.TriageBucket == "approved":
				data.YourApproved = append(data.YourApproved, pr)
			case pr.ReviewStatus != nil && *pr.ReviewStatus == "changes_requested":
				data.YourChanges = append(data.YourChanges, pr)
			default:
				data.YourWaiting = append(data.YourWaiting, pr)
			}
		} else if pr.TriageBucket == "needs_review" {
			needsReview = append(needsReview, pr)
		}
	}
	data.NeedsReviewCount = len(needsReview)

	// Group by ticket key
	groups := map[string]*ticketGroup{}
	var order []string
	for _, pr := range needsReview {
		key := "ungrouped"
		if pr.TicketKey != nil {
			key = *pr.TicketKey
		}
		g, ok := groups[key]
		if !ok {
			g = &ticketGroup{TicketKey: key}
			groups[key] = g
			order = append(order, key)
		}
		g.PRs = append(g.PRs, pr)

		short := shortRepo(pr.Repo)
		found := false
		for _, r := range g.Repos {
			if r == short {
				found = true
				break
			}
		}
		if !found {
			g.Repos = append(g.Repos, short)
		}

		if pr.UpdatedAt > g.LastActivity {
			g.LastActivity = pr.UpdatedAt
		}
	}

	// Sort: most recent first, ungrouped last
	sort.SliceStable(order, func(i, j int) bool {
		if order[i] == "ungrouped" {
			return false
		}
		if order[j] == "ungrouped" {
			return true
		}
		return groups[order[i]].LastActivity > groups[order[j]].LastActivity
	})
	for _, key := range order {
		data.NeedsReview = append(data.NeedsReview, *groups[key])
	}

	return data
}

// Template helpers

func reltime(iso string) string {
	if iso == "" {
		return ""
	}
	t, err := time.Parse(time.RFC3339, iso)
	if err != nil {
		return iso
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		m := int(d.Minutes())
		if m == 1 {
			return "1m ago"
		}
		return fmt.Sprintf("%dm ago", m)
	case d < 24*time.Hour:
		h := int(d.Hours())
		if h == 1 {
			return "1h ago"
		}
		return fmt.Sprintf("%dh ago", h)
	default:
		days := int(d.Hours() / 24)
		if days == 1 {
			return "1d ago"
		}
		return fmt.Sprintf("%dd ago", days)
	}
}

func shortRepo(repo string) string {
	if i := strings.IndexByte(repo, '/'); i >= 0 {
		return repo[i+1:]
	}
	return repo
}

func ciClass(status *string) string {
	if status == nil {
		return ""
	}
	switch *status {
	case "success":
		return "ci-pass"
	case "failure", "error":
		return "ci-fail"
	case "pending":
		return "ci-pending"
	}
	return ""
}

func ciLabel(status *string) string {
	if status == nil {
		return ""
	}
	switch *status {
	case "success":
		return "CI pass"
	case "failure", "error":
		return "CI fail"
	case "pending":
		return "CI pending"
	}
	return ""
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func pluralize(n int, singular string) string {
	if n == 1 {
		return "1 " + singular
	}
	return fmt.Sprintf("%d %ss", n, singular)
}
