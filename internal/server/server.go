package server

import (
	"crypto/hmac"
	"crypto/sha256"
	"embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	gosync "sync"
	"time"

	"github.com/ehrlich-b/reviews/internal/db"
	"github.com/ehrlich-b/reviews/internal/slack"
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

	adminToken  string
	slackClient *slack.Client
	nagEnabled       bool
	nagDryRun        bool
	nagThresholdDays int
	jiraBaseURL      string

	syncMu           gosync.Mutex
	lastSyncComplete time.Time
	syncPending      bool
	syncRunning      bool

	slackCacheMu gosync.RWMutex
	slackCache   []slack.SlackUser
}

type Config struct {
	AdminToken       string
	SlackClient      *slack.Client
	NagEnabled       bool
	NagDryRun        bool
	NagThresholdDays int
	JiraBaseURL      string
}

func New(store *db.Store, syncer *reviewsync.Syncer, orgs []string, cfg Config) *Server {
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
		"jiraURL": func(baseURL, key string) string {
			return baseURL + "/browse/" + key
		},
		"epoch": func(iso string) int64 {
			t, err := time.Parse(time.RFC3339, iso)
			if err != nil {
				return 0
			}
			return t.Unix()
		},
		"dict": func(pairs ...any) (map[string]any, error) {
			if len(pairs)%2 != 0 {
				return nil, fmt.Errorf("dict: odd number of args")
			}
			m := make(map[string]any, len(pairs)/2)
			for i := 0; i < len(pairs); i += 2 {
				key, ok := pairs[i].(string)
				if !ok {
					return nil, fmt.Errorf("dict: non-string key at index %d", i)
				}
				m[key] = pairs[i+1]
			}
			return m, nil
		},
	}

	tmpl := template.Must(template.New("").Funcs(funcMap).ParseFS(templatesFS, "templates/*.html"))

	s := &Server{
		store:       store,
		mux:         http.NewServeMux(),
		tmpl:        tmpl,
		syncer:      syncer,
		orgs:        orgs,
		adminToken:  cfg.AdminToken,
		slackClient: cfg.SlackClient,
		nagEnabled:       cfg.NagEnabled,
		nagDryRun:        cfg.NagDryRun,
		nagThresholdDays: cfg.NagThresholdDays,
		jiraBaseURL:      cfg.JiraBaseURL,
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

	if s.nagEnabled {
		go s.nagLoop()
	}

	if s.slackClient != nil {
		go s.slackCacheLoop()
	}

	return s
}

func (s *Server) loadSlackCacheFromDB() {
	raw, err := s.store.GetConfig("slack_users_cache")
	if err != nil || raw == "" {
		return
	}
	var users []slack.SlackUser
	if err := json.Unmarshal([]byte(raw), &users); err != nil {
		return
	}
	s.slackCacheMu.Lock()
	s.slackCache = users
	s.slackCacheMu.Unlock()
	log.Printf("slack cache loaded from db: %d users", len(users))
}

func (s *Server) refreshSlackCache() {
	users, err := s.slackClient.ListUsers()
	if err != nil {
		log.Printf("slack cache refresh: %v", err)
		return
	}
	s.slackCacheMu.Lock()
	s.slackCache = users
	s.slackCacheMu.Unlock()
	log.Printf("slack cache: %d users", len(users))

	// Persist to SQLite
	if raw, err := json.Marshal(users); err == nil {
		s.store.SetConfig("slack_users_cache", string(raw))
	}
}

func (s *Server) slackCacheLoop() {
	s.loadSlackCacheFromDB()
	s.refreshSlackCache()
	ticker := time.NewTicker(15 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		s.refreshSlackCache()
	}
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
	s.mux.HandleFunc("GET /metrics", s.handleMetrics)
	s.mux.HandleFunc("POST /api/sync", s.handleSync)
	s.mux.HandleFunc("GET /api/sync/status", s.handleSyncStatus)

	// Admin routes
	s.mux.HandleFunc("GET /admin", s.handleAdmin)
	s.mux.HandleFunc("POST /api/admin/login", s.handleAdminLogin)
	s.mux.HandleFunc("POST /api/admin/team", s.adminAuth(s.handleAdminCreateTeam))
	s.mux.HandleFunc("DELETE /api/admin/team", s.adminAuth(s.handleAdminDeleteTeam))
	s.mux.HandleFunc("POST /api/admin/team/member", s.adminAuth(s.handleAdminAddMember))
	s.mux.HandleFunc("DELETE /api/admin/team/member", s.adminAuth(s.handleAdminRemoveMember))
	s.mux.HandleFunc("POST /api/admin/slack", s.adminAuth(s.handleAdminSetSlack))
	s.mux.HandleFunc("DELETE /api/admin/slack", s.adminAuth(s.handleAdminRemoveSlack))
	s.mux.HandleFunc("GET /api/admin/slack/mappings", s.adminAuth(s.handleAdminSlackMappings))
	s.mux.HandleFunc("GET /api/admin/slack/users", s.adminAuth(s.handleAdminSlackUsers))
	s.mux.HandleFunc("GET /api/admin/github/users", s.adminAuth(s.handleAdminGithubUsers))
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

// signAdminToken creates an HMAC-signed session token: base64(expiry|sig)
func (s *Server) signAdminToken(expiry time.Time) string {
	exp := strconv.FormatInt(expiry.Unix(), 10)
	mac := hmac.New(sha256.New, []byte(s.adminToken))
	mac.Write([]byte(exp))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return base64.RawURLEncoding.EncodeToString([]byte(exp + "|" + sig))
}

// verifyAdminToken checks the HMAC-signed session token
func (s *Server) verifyAdminToken(token string) bool {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return false
	}
	parts := strings.SplitN(string(raw), "|", 2)
	if len(parts) != 2 {
		return false
	}
	exp, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || time.Now().Unix() > exp {
		return false
	}
	mac := hmac.New(sha256.New, []byte(s.adminToken))
	mac.Write([]byte(parts[0]))
	expected := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(parts[1]), []byte(expected))
}

func (s *Server) adminAuthed(r *http.Request) bool {
	if s.adminToken == "" {
		return false
	}
	c, err := r.Cookie("reviews_admin")
	if err != nil {
		return false
	}
	return s.verifyAdminToken(c.Value)
}

// Admin auth middleware
func (s *Server) adminAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.adminAuthed(r) {
			http.Error(w, "unauthorized", 401)
			return
		}
		next(w, r)
	}
}

func (s *Server) handleAdminLogin(w http.ResponseWriter, r *http.Request) {
	if s.adminToken == "" {
		http.Error(w, "admin not configured (set ADMIN_TOKEN)", 503)
		return
	}
	var body struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Token == "" {
		http.Error(w, "bad request", 400)
		return
	}
	if body.Token != s.adminToken {
		http.Error(w, "unauthorized", 401)
		return
	}
	signed := s.signAdminToken(time.Now().Add(7 * 24 * time.Hour))
	http.SetCookie(w, &http.Cookie{
		Name:     "reviews_admin",
		Value:    signed,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   7 * 24 * 60 * 60,
	})
	w.WriteHeader(204)
}

func (s *Server) handleAdmin(w http.ResponseWriter, r *http.Request) {
	if s.adminToken == "" {
		http.Error(w, "admin not configured (set ADMIN_TOKEN)", 503)
		return
	}

	if !s.adminAuthed(r) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := s.tmpl.ExecuteTemplate(w, "admin", nil); err != nil {
			log.Printf("render admin: %v", err)
		}
		return
	}

	teams, _ := s.store.ListTeams()
	memberships, _ := s.store.ListTeamMemberships()
	slackMappings, _ := s.store.ListSlackMappings()
	uncategorized, _ := s.store.ListUncategorizedAuthors()

	type adminData struct {
		Authed         bool
		Teams          []string
		Memberships    map[string][]string
		SlackMappings  []db.SlackMapping
		Uncategorized  []db.UncategorizedAuthor
		HasSlackClient bool
	}
	data := adminData{
		Authed:         true,
		Teams:          teams,
		Memberships:    memberships,
		SlackMappings:  slackMappings,
		Uncategorized:  uncategorized,
		HasSlackClient: s.slackClient != nil,
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.ExecuteTemplate(w, "admin", data); err != nil {
		log.Printf("render admin: %v", err)
	}
}

func (s *Server) handleAdminCreateTeam(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Name == "" {
		http.Error(w, "bad request", 400)
		return
	}
	if err := s.store.CreateTeam(body.Name); err != nil {
		log.Printf("create team: %v", err)
		http.Error(w, "internal error", 500)
		return
	}
	w.WriteHeader(204)
}

func (s *Server) handleAdminDeleteTeam(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	if name == "" {
		http.Error(w, "bad request", 400)
		return
	}
	if err := s.store.DeleteTeam(name); err != nil {
		log.Printf("delete team: %v", err)
		http.Error(w, "internal error", 500)
		return
	}
	w.WriteHeader(204)
}

func (s *Server) handleAdminAddMember(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Team     string `json:"team"`
		Username string `json:"username"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Team == "" || body.Username == "" {
		http.Error(w, "bad request", 400)
		return
	}
	if err := s.store.AddTeamMembership(body.Team, body.Username); err != nil {
		log.Printf("add team member: %v", err)
		http.Error(w, "internal error", 500)
		return
	}
	w.WriteHeader(204)
}

func (s *Server) handleAdminRemoveMember(w http.ResponseWriter, r *http.Request) {
	team := r.URL.Query().Get("team")
	user := r.URL.Query().Get("user")
	if team == "" || user == "" {
		http.Error(w, "bad request", 400)
		return
	}
	if err := s.store.RemoveTeamMembership(team, user); err != nil {
		log.Printf("remove team member: %v", err)
		http.Error(w, "internal error", 500)
		return
	}
	w.WriteHeader(204)
}

func (s *Server) handleAdminSetSlack(w http.ResponseWriter, r *http.Request) {
	var body struct {
		GithubUsername string `json:"github_username"`
		SlackUserID   string `json:"slack_user_id"`
		Timezone      string `json:"timezone"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.GithubUsername == "" || body.SlackUserID == "" {
		http.Error(w, "bad request", 400)
		return
	}
	if body.Timezone == "" {
		body.Timezone = "America/New_York"
	}
	if err := s.store.SetSlackMapping(body.GithubUsername, body.SlackUserID, body.Timezone); err != nil {
		log.Printf("set slack mapping: %v", err)
		http.Error(w, "internal error", 500)
		return
	}
	w.WriteHeader(204)
}

func (s *Server) handleAdminRemoveSlack(w http.ResponseWriter, r *http.Request) {
	user := r.URL.Query().Get("user")
	if user == "" {
		http.Error(w, "bad request", 400)
		return
	}
	if err := s.store.RemoveSlackMapping(user); err != nil {
		log.Printf("remove slack mapping: %v", err)
		http.Error(w, "internal error", 500)
		return
	}
	w.WriteHeader(204)
}


func (s *Server) handleAdminSlackMappings(w http.ResponseWriter, r *http.Request) {
	mappings, err := s.store.ListSlackMappings()
	if err != nil {
		http.Error(w, "internal error", 500)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(mappings)
}

func (s *Server) handleAdminSlackUsers(w http.ResponseWriter, r *http.Request) {
	s.slackCacheMu.RLock()
	users := s.slackCache
	s.slackCacheMu.RUnlock()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(users)
}

func (s *Server) handleAdminGithubUsers(w http.ResponseWriter, r *http.Request) {
	known, err := s.store.ListKnownAuthors()
	if err != nil {
		http.Error(w, "internal error", 500)
		return
	}
	mappings, err := s.store.ListSlackMappings()
	if err != nil {
		http.Error(w, "internal error", 500)
		return
	}
	seen := map[string]bool{}
	users := make([]string, 0, len(known)+len(mappings))
	for _, a := range known {
		if !seen[a] {
			seen[a] = true
			users = append(users, a)
		}
	}
	for _, m := range mappings {
		if !seen[m.GithubUsername] {
			seen[m.GithubUsername] = true
			users = append(users, m.GithubUsername)
		}
	}
	sort.Strings(users)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(users)
}

// Nag system

func (s *Server) nagLoop() {
	log.Printf("nag goroutine started (dry_run=%v, slack_configured=%v)", s.nagDryRun, s.slackClient != nil)
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	s.runNag()
	for range ticker.C {
		s.runNag()
	}
}

func (s *Server) runNag() {
	dryRun := s.nagDryRun || s.slackClient == nil

	prs, err := s.store.ListPRs()
	if err != nil {
		log.Printf("nag: list PRs: %v", err)
		return
	}

	mappings, err := s.store.ListSlackMappings()
	if err != nil {
		log.Printf("nag: list slack mappings: %v", err)
		return
	}
	if len(mappings) == 0 {
		return
	}

	mappingByUser := map[string]db.SlackMapping{}
	for _, m := range mappings {
		mappingByUser[m.GithubUsername] = m
	}

	// Group open PRs by author, skip PRs younger than threshold
	threshold := time.Duration(s.nagThresholdDays) * 24 * time.Hour
	authorPRs := map[string][]*db.PullRequest{}
	for _, pr := range prs {
		if pr.Draft || pr.CreatedAt == "" {
			continue
		}
		created, err := time.Parse(time.RFC3339, pr.CreatedAt)
		if err != nil || time.Since(created) < threshold {
			continue
		}
		authorPRs[pr.Author] = append(authorPRs[pr.Author], pr)
	}

	nagCount := 0
	for author, prList := range authorPRs {
		mapping, ok := mappingByUser[author]
		if !ok {
			continue
		}

		// Must be 1pm-5pm in their local timezone
		loc, err := time.LoadLocation(mapping.Timezone)
		if err != nil {
			log.Printf("nag: bad timezone %q for %s: %v", mapping.Timezone, author, err)
			continue
		}
		now := time.Now().In(loc)
		if now.Weekday() == time.Saturday || now.Weekday() == time.Sunday {
			continue
		}
		if now.Hour() < 13 || now.Hour() > 16 {
			continue
		}

		// Check if already nagged today (per-author, not per-PR)
		authorKey := "author:" + author
		today := now.Format("2006-01-02")
		lastNag, _ := s.store.GetLastNag(authorKey)
		if lastNag != "" {
			nagTime, err := time.Parse(time.RFC3339, lastNag)
			if err == nil && nagTime.In(loc).Format("2006-01-02") == today {
				continue
			}
		}

		// Filter BLOCKED PRs: only include if last nag was 7+ days ago
		var nagAge time.Duration
		if lastNag != "" {
			if t, err := time.Parse(time.RFC3339, lastNag); err == nil {
				nagAge = time.Since(t)
			}
		}
		var filtered []*db.PullRequest
		for _, pr := range prList {
			if strings.HasPrefix(pr.Title, "BLOCKED") && nagAge > 0 && nagAge < 7*24*time.Hour {
				continue
			}
			filtered = append(filtered, pr)
		}
		if len(filtered) == 0 {
			continue
		}

		// Build message
		var lines []string
		lines = append(lines, fmt.Sprintf("You have %d PRs open for more than %d days:", len(filtered), s.nagThresholdDays))
		for _, pr := range filtered {
			line := fmt.Sprintf("  - %s#%d: %s (%s)", shortRepo(pr.Repo), pr.Number, pr.Title, pr.URL)
			if pr.CreatedAt != "" {
				if created, err := time.Parse(time.RFC3339, pr.CreatedAt); err == nil {
					days := int(time.Since(created).Hours() / 24)
					if days >= s.nagThresholdDays*2 {
						line += fmt.Sprintf(" (open for %d days)", days)
					}
				}
			}
			lines = append(lines, line)
		}
		msg := strings.Join(lines, "\n")

		// Record BEFORE sending — if Slack fails we skip for the day rather than risk turbo-blasting
		naggedAt := time.Now().UTC().Format(time.RFC3339)
		s.store.SetLastNag(authorKey, naggedAt)

		if dryRun {
			log.Printf("nag [dry-run] would DM %s (%s): %s", author, mapping.Timezone, msg)
		} else {
			if err := s.slackClient.SendDM(mapping.SlackUserID, msg); err != nil {
				log.Printf("nag: send DM to %s failed (will retry tomorrow): %v", author, err)
				continue
			}
			log.Printf("nag: sent DM to %s (%d PRs)", author, len(filtered))
		}
		nagCount++
	}

	if nagCount > 0 || os.Getenv("NAG_VERBOSE") == "true" {
		log.Printf("nag: processed %d authors", nagCount)
	}
}

type ticketGroup struct {
	TicketKey       string
	PRs             []*db.PullRequest
	Repos           []string
	Authors         []string
	Additions       int
	Deletions       int
	LastActivity    string
	OldestCreatedAt string
	JiraSummary     string
	JiraStatus      string
	EpicKey         string
	EpicSummary     string
}

type repoGroup struct {
	Owner string
	Repos []string
}

type teamInfo struct {
	Name    string
	Members []string
}

type dashboardData struct {
	Viewer            string
	Engaged           []*db.PullRequest
	NeedsReview       []ticketGroup
	NeedsReviewCount  int
	AuthorsCourt      []ticketGroup
	AuthorsCourtCount int
	YourApproved      []*db.PullRequest
	YourChanges       []*db.PullRequest
	YourWaiting       []*db.PullRequest
	YourPRsCount      int
	OthersDrafts      []*db.PullRequest
	OthersApproved    []*db.PullRequest
	OthersSkipped     []*db.PullRequest
	AllRepos          []string
	RepoGroups        []repoGroup
	TotalPRs          int
	TotalRepos        int
	LastSync          string
	Teams             []teamInfo
	AuthorTeams       map[string][]string
	JiraBaseURL       string
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	viewer := ""
	if c, err := r.Cookie("reviews_viewer"); err == nil {
		viewer = c.Value
	}

	prs, err := s.store.ListPRs()
	if err != nil {
		log.Printf("list PRs: %v", err)
		http.Error(w, "internal error", 500)
		return
	}

	teams, _ := s.store.ListTeams()
	memberships, _ := s.store.ListTeamMemberships()

	repoCount, prCount, lastSync, err := s.store.GetSyncInfo()
	if err != nil {
		log.Printf("get sync info: %v", err)
		http.Error(w, "internal error", 500)
		return
	}

	// Load Jira data for displayed ticket keys
	var jiraIssues map[string]*db.JiraIssue
	if s.jiraBaseURL != "" {
		var keys []string
		seen := map[string]bool{}
		for _, pr := range prs {
			if pr.TicketKey != nil && !seen[*pr.TicketKey] {
				keys = append(keys, *pr.TicketKey)
				seen[*pr.TicketKey] = true
			}
		}
		jiraIssues, _ = s.store.GetJiraIssues(keys)
	}

	data := buildDashboard(viewer, prs, repoCount, prCount, lastSync, teams, memberships, s.jiraBaseURL, jiraIssues)

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.ExecuteTemplate(w, "base", data); err != nil {
		log.Printf("render: %v", err)
	}
}

func buildDashboard(viewer string, prs []*db.PullRequest, repoCount, prCount int, lastSync string, teams []string, memberships map[string][]string, jiraBaseURL string, jiraIssues map[string]*db.JiraIssue) *dashboardData {
	// Build team info and author->teams map
	var teamInfos []teamInfo
	authorTeams := map[string][]string{}
	for _, t := range teams {
		members := memberships[t]
		teamInfos = append(teamInfos, teamInfo{Name: t, Members: members})
		for _, m := range members {
			authorTeams[m] = append(authorTeams[m], t)
		}
	}

	data := &dashboardData{
		Viewer:      viewer,
		TotalPRs:    prCount,
		TotalRepos:  repoCount,
		LastSync:    lastSync,
		Teams:       teamInfos,
		AuthorTeams: authorTeams,
		JiraBaseURL: jiraBaseURL,
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
		} else if viewer != "" && viewerEngaged(pr.EngagedUsers, viewer) {
			data.Engaged = append(data.Engaged, pr)
		} else {
			switch pr.TriageBucket {
			case "needs_review":
				needsReview = append(needsReview, pr)
			case "author_court":
				authorsCourt = append(authorsCourt, pr)
			case "approved":
				data.OthersApproved = append(data.OthersApproved, pr)
			case "skipped":
				if pr.Draft {
					data.OthersDrafts = append(data.OthersDrafts, pr)
				} else {
					data.OthersSkipped = append(data.OthersSkipped, pr)
				}
			}
		}
	}

	data.NeedsReview = groupByTicket(needsReview)
	data.AuthorsCourt = groupByTicket(authorsCourt)

	// Engaged: sort by most recent activity (updated_at desc)
	sort.SliceStable(data.Engaged, func(i, j int) bool {
		return data.Engaged[i].UpdatedAt > data.Engaged[j].UpdatedAt
	})

	// Enrich ticket groups with Jira data
	if jiraIssues != nil {
		enrichJira := func(groups []ticketGroup) {
			for i := range groups {
				if ji, ok := jiraIssues[groups[i].TicketKey]; ok {
					groups[i].JiraSummary = ji.Summary
					groups[i].JiraStatus = ji.Status
					if ji.EpicKey != nil {
						groups[i].EpicKey = *ji.EpicKey
					}
					if ji.EpicSummary != nil {
						groups[i].EpicSummary = *ji.EpicSummary
					}
				}
			}
		}
		enrichJira(data.NeedsReview)
		enrichJira(data.AuthorsCourt)
	}

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
	displayedPRs = append(displayedPRs, data.Engaged...)
	displayedPRs = append(displayedPRs, data.OthersDrafts...)
	displayedPRs = append(displayedPRs, data.OthersApproved...)
	displayedPRs = append(displayedPRs, data.OthersSkipped...)

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
		if g.OldestCreatedAt == "" || pr.CreatedAt < g.OldestCreatedAt {
			g.OldestCreatedAt = pr.CreatedAt
		}
	}

	sort.SliceStable(order, func(i, j int) bool {
		return groups[order[i]].OldestCreatedAt < groups[order[j]].OldestCreatedAt
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
			return g.PRs[i].CreatedAt < g.PRs[j].CreatedAt
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
	return userInJSONList(approvers, viewer)
}

func viewerEngaged(engagedUsers *string, viewer string) bool {
	return userInJSONList(engagedUsers, viewer)
}

func userInJSONList(jsonList *string, user string) bool {
	if jsonList == nil || user == "" {
		return false
	}
	var list []string
	if err := json.Unmarshal([]byte(*jsonList), &list); err != nil {
		return false
	}
	for _, u := range list {
		if u == user {
			return true
		}
	}
	return false
}
