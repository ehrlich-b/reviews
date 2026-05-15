package server

import (
	"fmt"
	"log"
	"net/http"
	"sort"
	"strings"
	"time"
)

const unassignedTeam = "_unassigned"

type teamMetrics struct {
	count        int
	byBucket     map[string]int
	additions    int
	deletions    int
	oldestAge    time.Duration
	ageSum       time.Duration
	ageSamples   int
}

func newTeamMetrics() *teamMetrics {
	return &teamMetrics{byBucket: map[string]int{}}
}

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	prs, err := s.store.ListPRs()
	if err != nil {
		log.Printf("metrics: list PRs: %v", err)
		http.Error(w, "internal error", 500)
		return
	}
	teams, err := s.store.ListTeams()
	if err != nil {
		log.Printf("metrics: list teams: %v", err)
		http.Error(w, "internal error", 500)
		return
	}
	memberships, err := s.store.ListTeamMemberships()
	if err != nil {
		log.Printf("metrics: list memberships: %v", err)
		http.Error(w, "internal error", 500)
		return
	}

	authorTeams := map[string][]string{}
	for _, t := range teams {
		for _, m := range memberships[t] {
			authorTeams[m] = append(authorTeams[m], t)
		}
	}

	now := time.Now()
	stats := map[string]*teamMetrics{}
	getStats := func(team string) *teamMetrics {
		if s, ok := stats[team]; ok {
			return s
		}
		s := newTeamMetrics()
		stats[team] = s
		return s
	}

	for _, pr := range prs {
		prTeams := authorTeams[pr.Author]
		if len(prTeams) == 0 {
			prTeams = []string{unassignedTeam}
		}

		var age time.Duration
		hasAge := false
		if pr.CreatedAt != "" {
			if t, err := time.Parse(time.RFC3339, pr.CreatedAt); err == nil {
				age = now.Sub(t)
				hasAge = true
			}
		}

		for _, team := range prTeams {
			tm := getStats(team)
			tm.count++
			tm.byBucket[pr.TriageBucket]++
			tm.additions += pr.Additions
			tm.deletions += pr.Deletions
			if hasAge {
				if age > tm.oldestAge {
					tm.oldestAge = age
				}
				tm.ageSum += age
				tm.ageSamples++
			}
		}
	}

	// Make sure every team appears in the output, even if it has 0 PRs
	for _, t := range teams {
		if _, ok := stats[t]; !ok {
			stats[t] = newTeamMetrics()
		}
	}

	teamNames := make([]string, 0, len(stats))
	for name := range stats {
		teamNames = append(teamNames, name)
	}
	sort.Strings(teamNames)

	var b strings.Builder
	emitGauge(&b, "reviews_wip_prs", "Number of open PRs in flight per team")
	for _, name := range teamNames {
		fmt.Fprintf(&b, "reviews_wip_prs{team=%q} %d\n", escapeLabel(name), stats[name].count)
	}

	emitGauge(&b, "reviews_wip_prs_by_bucket", "Open PRs per team broken down by triage bucket")
	buckets := []string{"needs_review", "author_court", "approved", "skipped"}
	for _, name := range teamNames {
		for _, bk := range buckets {
			fmt.Fprintf(&b, "reviews_wip_prs_by_bucket{team=%q,bucket=%q} %d\n",
				escapeLabel(name), bk, stats[name].byBucket[bk])
		}
	}

	emitGauge(&b, "reviews_wip_loc_additions", "Lines added across open PRs per team")
	for _, name := range teamNames {
		fmt.Fprintf(&b, "reviews_wip_loc_additions{team=%q} %d\n", escapeLabel(name), stats[name].additions)
	}

	emitGauge(&b, "reviews_wip_loc_deletions", "Lines deleted across open PRs per team")
	for _, name := range teamNames {
		fmt.Fprintf(&b, "reviews_wip_loc_deletions{team=%q} %d\n", escapeLabel(name), stats[name].deletions)
	}

	emitGauge(&b, "reviews_wip_pr_oldest_age_seconds", "Age of the oldest open PR per team in seconds")
	for _, name := range teamNames {
		fmt.Fprintf(&b, "reviews_wip_pr_oldest_age_seconds{team=%q} %d\n",
			escapeLabel(name), int64(stats[name].oldestAge.Seconds()))
	}

	emitGauge(&b, "reviews_wip_pr_avg_age_seconds", "Mean age of open PRs per team in seconds")
	for _, name := range teamNames {
		s := stats[name]
		var avg int64
		if s.ageSamples > 0 {
			avg = int64(s.ageSum.Seconds()) / int64(s.ageSamples)
		}
		fmt.Fprintf(&b, "reviews_wip_pr_avg_age_seconds{team=%q} %d\n", escapeLabel(name), avg)
	}

	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	_, _ = w.Write([]byte(b.String()))
}

func emitGauge(b *strings.Builder, name, help string) {
	fmt.Fprintf(b, "# HELP %s %s\n", name, help)
	fmt.Fprintf(b, "# TYPE %s gauge\n", name)
}

// escapeLabel returns the label value with Prometheus-required escapes applied.
func escapeLabel(v string) string {
	v = strings.ReplaceAll(v, `\`, `\\`)
	v = strings.ReplaceAll(v, `"`, `\"`)
	v = strings.ReplaceAll(v, "\n", `\n`)
	return v
}

