package main

// Deploy history: taildrop.sh appends one JSON line per publish to <root>/deploys.jsonl.
// The server only reads it — for /api/deploys and the page's "Recent deploys" list.

import (
	"bufio"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
)

// Deploy is one publish of one app build.
type Deploy struct {
	T       float64 `json:"t"`
	Slug    string  `json:"slug"`
	Name    string  `json:"name"`
	Version string  `json:"version"`
	Build   string  `json:"build"`
	Bundle  string  `json:"bundle"`
	Sign    string  `json:"sign"` // debugging (Development) | release-testing (Ad Hoc)
	Git     string  `json:"git"`  // project commit, "-dirty" if uncommitted changes
	By      string  `json:"by"`   // build machine
}

// deploys returns the newest n deploys, newest first; torn lines are skipped.
func (s *server) deploys(n int) []Deploy {
	f, err := os.Open(filepath.Join(s.root, "deploys.jsonl"))
	if err != nil {
		return []Deploy{}
	}
	defer f.Close()
	var all []Deploy
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var d Deploy
		if json.Unmarshal(sc.Bytes(), &d) == nil && d.Slug != "" {
			all = append(all, d)
		}
	}
	out := make([]Deploy, 0, n)
	for i := len(all) - 1; i >= 0 && len(out) < n; i-- {
		out = append(out, all[i])
	}
	return out
}

func (s *server) deploysAPI(w http.ResponseWriter, r *http.Request) {
	n, err := strconv.Atoi(r.URL.Query().Get("n"))
	if err != nil || n <= 0 || n > 500 {
		n = 50
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(s.deploys(n))
}

// signLabel is the page's name for a signing method.
func signLabel(sign string) string {
	switch sign {
	case "release-testing":
		return "Ad Hoc"
	case "", "debugging":
		return "Development"
	}
	return sign
}
