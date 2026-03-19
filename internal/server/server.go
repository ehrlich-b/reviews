package server

import (
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"sort"
	"strings"
	gosync "sync"
	"time"

	"github.com/ehrlich-b/reviews/internal/db"
	reviewsync "github.com/ehrlich-b/reviews/internal/sync"
)

//go:embed templates/*.html
var templatesFS embed.FS

type Server struct {
	store  *db.Store
	mux    *http.ServeMux
	tmpl   *template.Template
	syncer *reviewsync.Syncer
	orgs   []string

	syncMu           gosync.Mutex
	lastSyncComplete time.Time
	syncPending      bool
	syncRunning      bool
}

func New(store *db.Store, syncer *reviewsync.Syncer, orgs []string) *Server {
	funcMap := template.FuncMap{
		"reltime":    reltime,
		"shortRepo":  shortRepo,
		"ciClass":    ciClass,
		"ciLabel":    ciLabel,
		"deref":      deref,
		"pluralize":  pluralize,
		"join":       strings.Join,
		"loc":        loc,
		"add":        func(a, b int) int { return a + b },
		"approvedBy": approvedBy,
		"epoch": func(iso string) int64 {
			t, err := time.Parse(time.RFC3339, iso)
			if err != nil {
				return 0
			}
			return t.Unix()
		},
	}

	tmpl := template.Must(template.New("").Funcs(funcMap).ParseFS(templatesFS, "templates/*.html"))

	s := &Server{
		store:  store,
		mux:    http.NewServeMux(),
		tmpl:   tmpl,
		syncer: syncer,
		orgs:   orgs,
	}
	s.routes()

	if syncer != nil {
		s.syncRunning = true
		go func() {
			log.Printf("running initial sync...")
			s.runSync()
			go s.backgroundSync()
		}()
	}

	return s
}

func (s *Server) requestSync() string {
	s.syncMu.Lock()
	defer s.syncMu.Unlock()

	if s.syncRunning {
		s.syncPending = true
		return "queued"
	}

	if time.Since(s.lastSyncComplete) < time.Minute {
		s.syncPending = true
		remaining := time.Until(s.lastSyncComplete.Add(time.Minute))
		time.AfterFunc(remaining, func() {
			s.syncMu.Lock()
			if s.syncPending && !s.syncRunning {
				s.syncPending = false
				s.syncRunning = true
				s.syncMu.Unlock()
				s.runSync()
			} else {
				s.syncMu.Unlock()
			}
		})
		return "queued"
	}

	s.syncRunning = true
	go s.runSync()
	return "syncing"
}

func (s *Server) runSync() {
	for {
		if sum, err := s.syncer.Run(false, s.orgs); err != nil {
			log.Printf("sync: %v", err)
		} else {
			log.Printf("sync: %d PRs across %d repos", sum.Total, sum.Repos)
		}

		s.syncMu.Lock()
		s.lastSyncComplete = time.Now()
		if !s.syncPending {
			s.syncRunning = false
			s.syncMu.Unlock()
			return
		}
		s.syncPending = false
		remaining := time.Until(s.lastSyncComplete.Add(time.Minute))
		s.syncMu.Unlock()
		if remaining > 0 {
			time.Sleep(remaining)
		}
	}
}

func (s *Server) backgroundSync() {
	ticker := time.NewTicker(10 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		s.requestSync()
	}
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

func (s *Server) routes() {
	s.mux.HandleFunc("GET /", s.handleIndex)
	s.mux.HandleFunc("POST /api/sync", s.handleSync)
	s.mux.HandleFunc("GET /api/sync/status", s.handleSyncStatus)
	s.mux.HandleFunc("POST /api/team", s.handleAddTeam)
	s.mux.HandleFunc("DELETE /api/team", s.handleRemoveTeam)
}

func (s *Server) handleSync(w http.ResponseWriter, r *http.Request) {
	if s.syncer == nil {
		http.Error(w, "sync not configured (no GITHUB_TOKEN)", 503)
		return
	}
	status := s.requestSync()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": status})
}

func (s *Server) handleSyncStatus(w http.ResponseWriter, r *http.Request) {
	s.syncMu.Lock()
	running := s.syncRunning
	lastSync := s.lastSyncComplete
	s.syncMu.Unlock()

	status := "idle"
	if running {
		status = "syncing"
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"status":   status,
		"lastSync": lastSync.Format(time.RFC3339),
	})
}

func (s *Server) handleAddTeam(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Username string `json:"username"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", 400)
		return
	}
	if body.Username == "" {
		http.Error(w, "bad request", 400)
		return
	}
	if err := s.store.AddTeamMember(body.Username); err != nil {
		log.Printf("add team member: %v", err)
		http.Error(w, "internal error", 500)
		return
	}
	w.WriteHeader(204)
}

func (s *Server) handleRemoveTeam(w http.ResponseWriter, r *http.Request) {
	username := r.URL.Query().Get("user")
	if username == "" {
		http.Error(w, "bad request", 400)
		return
	}
	if err := s.store.RemoveTeamMember(username); err != nil {
		log.Printf("remove team member: %v", err)
		http.Error(w, "internal error", 500)
		return
	}
	w.WriteHeader(204)
}

type ticketGroup struct {
	TicketKey       string
	PRs             []*db.PullRequest
	Repos           []string
	Authors         []string
	Additions       int
	Deletions       int
	LastActivity    string
	OldestUpdatedAt string
}

type repoGroup struct {
	Owner string
	Repos []string
}

type dashboardData struct {
	Viewer            string
	NeedsReview       []ticketGroup
	NeedsReviewCount  int
	AuthorsCourt      []ticketGroup
	AuthorsCourtCount int
	YourApproved      []*db.PullRequest
	YourChanges       []*db.PullRequest
	YourWaiting       []*db.PullRequest
	YourPRsCount      int
	AllRepos          []string
	RepoGroups        []repoGroup
	TotalPRs          int
	TotalRepos        int
	LastSync          string
	TeamMembers       []string
	TeamAuthors       map[string]bool
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	viewer := r.URL.Query().Get("viewer")

	prs, err := s.store.ListPRs()
	if err != nil {
		log.Printf("list PRs: %v", err)
		http.Error(w, "internal error", 500)
		return
	}

	teamMembers, err := s.store.ListTeamMembers()
	if err != nil {
		log.Printf("list team members: %v", err)
		http.Error(w, "internal error", 500)
		return
	}

	repoCount, prCount, lastSync, err := s.store.GetSyncInfo()
	if err != nil {
		log.Printf("get sync info: %v", err)
		http.Error(w, "internal error", 500)
		return
	}

	data := buildDashboard(viewer, prs, repoCount, prCount, lastSync, teamMembers)

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.ExecuteTemplate(w, "base", data); err != nil {
		log.Printf("render: %v", err)
	}
}

func buildDashboard(viewer string, prs []*db.PullRequest, repoCount, prCount int, lastSync string, teamMembers []string) *dashboardData {
	teamSet := map[string]bool{}
	for _, m := range teamMembers {
		teamSet[m] = true
	}
	teamAuthors := map[string]bool{}
	for _, pr := range prs {
		if teamSet[pr.Author] {
			teamAuthors[pr.Author] = true
		}
	}

	data := &dashboardData{
		Viewer:      viewer,
		TotalPRs:    prCount,
		TotalRepos:  repoCount,
		LastSync:    lastSync,
		TeamMembers: teamMembers,
		TeamAuthors: teamAuthors,
	}

	var needsReview []*db.PullRequest
	var authorsCourt []*db.PullRequest
	for _, pr := range prs {
		if viewer != "" && pr.Author == viewer {
			data.YourPRsCount++
			switch {
			case pr.TriageBucket == "approved":
				data.YourApproved = append(data.YourApproved, pr)
			case pr.ReviewStatus != nil && *pr.ReviewStatus == "changes_requested":
				data.YourChanges = append(data.YourChanges, pr)
			default:
				data.YourWaiting = append(data.YourWaiting, pr)
			}
		} else {
			if pr.TriageBucket == "needs_review" {
				needsReview = append(needsReview, pr)
			} else if pr.TriageBucket == "author_court" {
				authorsCourt = append(authorsCourt, pr)
			}
		}
	}

	data.NeedsReview = groupByTicket(needsReview)
	data.AuthorsCourt = groupByTicket(authorsCourt)

	data.NeedsReviewCount = 0
	for _, g := range data.NeedsReview {
		data.NeedsReviewCount += len(g.PRs)
	}
	data.AuthorsCourtCount = 0
	for _, g := range data.AuthorsCourt {
		data.AuthorsCourtCount += len(g.PRs)
	}

	// Build repo pills from PRs in displayed sections only
	var displayedPRs []*db.PullRequest
	for _, g := range data.NeedsReview {
		displayedPRs = append(displayedPRs, g.PRs...)
	}
	for _, g := range data.AuthorsCourt {
		displayedPRs = append(displayedPRs, g.PRs...)
	}
	displayedPRs = append(displayedPRs, data.YourApproved...)
	displayedPRs = append(displayedPRs, data.YourChanges...)
	displayedPRs = append(displayedPRs, data.YourWaiting...)

	repoSet := map[string]bool{}
	repoOwner := map[string]string{}
	for _, pr := range displayedPRs {
		short := shortRepo(pr.Repo)
		repoSet[short] = true
		if i := strings.IndexByte(pr.Repo, '/'); i >= 0 {
			repoOwner[short] = pr.Repo[:i]
		}
	}
	var allRepos []string
	for r := range repoSet {
		allRepos = append(allRepos, r)
	}
	sort.Strings(allRepos)
	ownerRepos := map[string][]string{}
	for _, r := range allRepos {
		ownerRepos[repoOwner[r]] = append(ownerRepos[repoOwner[r]], r)
	}
	var owners []string
	for o := range ownerRepos {
		owners = append(owners, o)
	}
	sort.Strings(owners)
	var repoGroups []repoGroup
	for _, o := range owners {
		repoGroups = append(repoGroups, repoGroup{Owner: o, Repos: ownerRepos[o]})
	}
	data.AllRepos = allRepos
	data.RepoGroups = repoGroups

	return data
}

func groupByTicket(prs []*db.PullRequest) []ticketGroup {
	groups := map[string]*ticketGroup{}
	var order []string
	for _, pr := range prs {
		var key string
		if pr.TicketKey != nil {
			key = *pr.TicketKey
		} else {
			key = fmt.Sprintf("%s#%d", shortRepo(pr.Repo), pr.Number)
		}
		g, ok := groups[key]
		if !ok {
			g = &ticketGroup{TicketKey: key}
			groups[key] = g
			order = append(order, key)
		}
		g.PRs = append(g.PRs, pr)
		g.Additions += pr.Additions
		g.Deletions += pr.Deletions

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

		found = false
		for _, a := range g.Authors {
			if a == pr.Author {
				found = true
				break
			}
		}
		if !found {
			g.Authors = append(g.Authors, pr.Author)
		}

		if pr.UpdatedAt > g.LastActivity {
			g.LastActivity = pr.UpdatedAt
		}
		if g.OldestUpdatedAt == "" || pr.UpdatedAt < g.OldestUpdatedAt {
			g.OldestUpdatedAt = pr.UpdatedAt
		}
	}

	sort.SliceStable(order, func(i, j int) bool {
		return groups[order[i]].OldestUpdatedAt < groups[order[j]].OldestUpdatedAt
	})
	var result []ticketGroup
	for _, key := range order {
		g := groups[key]
		sort.SliceStable(g.PRs, func(i, j int) bool {
			si := g.PRs[i].Additions + g.PRs[i].Deletions
			sj := g.PRs[j].Additions + g.PRs[j].Deletions
			if si != sj {
				return si < sj
			}
			return g.PRs[i].UpdatedAt < g.PRs[j].UpdatedAt
		})
		result = append(result, *g)
	}
	return result
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

func loc(additions, deletions int) string {
	total := additions + deletions
	if total == 0 {
		return ""
	}
	return fmt.Sprintf("+%d/-%d", additions, deletions)
}

func approvedBy(approvers *string, viewer string) bool {
	if approvers == nil || viewer == "" {
		return false
	}
	var list []string
	if err := json.Unmarshal([]byte(*approvers), &list); err != nil {
		return false
	}
	for _, a := range list {
		if a == viewer {
			return true
		}
	}
	return false
}
