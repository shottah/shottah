// Rebuilds the sections of README.md between the <!-- name starts/ends -->
// markers from the GitHub API. Run with GITHUB_TOKEN set.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"
)

const login = "shottah"

func gh(method, url string, body []byte) ([]byte, error) {
	req, err := http.NewRequest(method, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	if tok := os.Getenv("GITHUB_TOKEN"); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s %s: %s: %s", method, url, resp.Status, data)
	}
	return data, nil
}

type release struct {
	repo string
	tag  string
	url  string
	at   time.Time
}

func recentReleases() ([]string, error) {
	query := fmt.Sprintf(`{ user(login: %q) { repositories(first: 100, ownerAffiliations: OWNER, isFork: false) {
		nodes { name releases(first: 5, orderBy: {field: CREATED_AT, direction: DESC}) {
			nodes { tagName url publishedAt isDraft } } } } } }`, login)
	payload, err := json.Marshal(map[string]string{"query": query})
	if err != nil {
		return nil, err
	}
	data, err := gh("POST", "https://api.github.com/graphql", payload)
	if err != nil {
		return nil, err
	}
	var out struct {
		Data struct {
			User struct {
				Repositories struct {
					Nodes []struct {
						Name     string
						Releases struct {
							Nodes []struct {
								TagName     string
								URL         string
								PublishedAt string
								IsDraft     bool
							}
						}
					}
				}
			}
		}
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, err
	}
	var all []release
	for _, repo := range out.Data.User.Repositories.Nodes {
		for _, r := range repo.Releases.Nodes {
			if r.IsDraft || r.PublishedAt == "" {
				continue
			}
			at, err := time.Parse(time.RFC3339, r.PublishedAt)
			if err != nil {
				continue
			}
			all = append(all, release{repo.Name, r.TagName, r.URL, at})
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].at.After(all[j].at) })
	cutoff := time.Now().AddDate(-2, 0, 0)
	recent := all
	for i, r := range all {
		if !r.at.After(cutoff) {
			recent = all[:i]
			break
		}
	}
	// Never render an empty column: fall back to the last few all-time releases.
	if len(recent) == 0 {
		recent = all
		if len(recent) > 3 {
			recent = recent[:3]
		}
	}
	perRepo := map[string]int{}
	var lines []string
	for _, r := range recent {
		if perRepo[r.repo] >= 2 {
			continue
		}
		perRepo[r.repo]++
		lines = append(lines, fmt.Sprintf("[%s %s](%s) — %s", r.repo, r.tag, r.url, r.at.Format("2006-01-02")))
		if len(lines) == 6 {
			break
		}
	}
	return lines, nil
}

func recentPRs() ([]string, error) {
	// is:public keeps private client work out even when run with a
	// personal token that can see it.
	url := "https://api.github.com/search/issues?q=author:" + login + "+type:pr+is:public&sort=created&order=desc&per_page=30"
	data, err := gh("GET", url, nil)
	if err != nil {
		return nil, err
	}
	var out struct {
		Items []struct {
			Title         string    `json:"title"`
			HTMLURL       string    `json:"html_url"`
			Number        int       `json:"number"`
			State         string    `json:"state"`
			RepositoryURL string    `json:"repository_url"`
			CreatedAt     time.Time `json:"created_at"`
			PullRequest   struct {
				MergedAt *time.Time `json:"merged_at"`
			} `json:"pull_request"`
		} `json:"items"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, err
	}
	perRepo := map[string]int{}
	var lines []string
	for _, item := range out.Items {
		repo := item.RepositoryURL
		if i := strings.Index(repo, "/repos/"); i >= 0 {
			repo = repo[i+len("/repos/"):]
		}
		merged := item.PullRequest.MergedAt != nil
		if item.State == "closed" && !merged {
			continue
		}
		if perRepo[repo] >= 3 {
			continue
		}
		perRepo[repo]++
		status := "opened " + item.CreatedAt.Format("2006-01-02")
		if merged {
			status = "merged " + item.PullRequest.MergedAt.Format("2006-01-02")
		}
		title := []rune(item.Title)
		if len(title) > 70 {
			title = append(title[:67], '…')
		}
		lines = append(lines, fmt.Sprintf("[%s#%d](%s) %s — %s",
			repo, item.Number, item.HTMLURL, string(title), status))
		if len(lines) == 6 {
			break
		}
	}
	return lines, nil
}

func splice(content, name string, lines []string) (string, error) {
	start := "<!-- " + name + " starts -->"
	end := "<!-- " + name + " ends -->"
	i := strings.Index(content, start)
	j := strings.Index(content, end)
	if i < 0 || j < 0 || j < i {
		return "", fmt.Errorf("markers for %q not found in README", name)
	}
	if len(lines) == 0 {
		lines = []string{"*nothing yet*"}
	}
	chunk := strings.Join(lines, "\n\n")
	return content[:i+len(start)] + "\n" + chunk + "\n" + content[j:], nil
}

func main() {
	releases, err := recentReleases()
	if err != nil {
		fmt.Fprintln(os.Stderr, "releases:", err)
		os.Exit(1)
	}
	prs, err := recentPRs()
	if err != nil {
		fmt.Fprintln(os.Stderr, "prs:", err)
		os.Exit(1)
	}
	raw, err := os.ReadFile("README.md")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	content := string(raw)
	for name, lines := range map[string][]string{"releases": releases, "prs": prs} {
		content, err = splice(content, name, lines)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
	if content == string(raw) {
		fmt.Println("README unchanged")
		return
	}
	if err := os.WriteFile("README.md", []byte(content), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("README updated")
}
