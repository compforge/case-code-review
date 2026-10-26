package viewer

import (
	"embed"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/compforge/agentgo/codec"
	"github.com/qiankunli/case-code-review/internal/console"
)

//go:embed templates/*.html static/*
var assets embed.FS

// BrowserURL turns an all-interface listen address into a URL a local browser
// can actually navigate to without changing which interfaces the server binds.
func BrowserURL(addr string) string {
	if strings.HasPrefix(addr, ":") {
		addr = "localhost" + addr
	}
	return "http://" + addr
}

func StartServer(addr string, options ...codec.Option) error {
	root, err := SessionsRoot()
	if err != nil {
		return fmt.Errorf("resolve sessions root: %w", err)
	}

	mux := http.NewServeMux()

	// Static assets (must be registered before "/" catch-all)
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.FS(staticFS()))))

	// Routes
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		handleRepos(w, r, root)
	})
	mux.HandleFunc("/r/{repo}", func(w http.ResponseWriter, r *http.Request) {
		repo := r.PathValue("repo")
		if strings.Contains(repo, "..") || strings.Contains(repo, "/") {
			http.Error(w, "invalid repo path", http.StatusBadRequest)
			return
		}
		handleSessions(w, r, root, repo)
	})
	mux.HandleFunc("/r/{repo}/{sessionID}", func(w http.ResponseWriter, r *http.Request) {
		repo := r.PathValue("repo")
		sid := r.PathValue("sessionID")
		if strings.Contains(repo, "..") || strings.Contains(sid, "..") {
			http.Error(w, "invalid path", http.StatusBadRequest)
			return
		}
		handleSession(w, r, root, repo, sid)
	})
	mux.HandleFunc("GET /r/{repo}/{sessionID}/call", func(w http.ResponseWriter, r *http.Request) {
		handleStageCall(w, r, root, r.PathValue("repo"), r.PathValue("sessionID"))
	})

	mux.HandleFunc("GET /r/{repo}/{sessionID}/agent", func(w http.ResponseWriter, r *http.Request) {
		handleStageAgent(w, r, root, r.PathValue("repo"), r.PathValue("sessionID"))
	})

	mux.HandleFunc("GET /r/{repo}/{sessionID}/job", func(w http.ResponseWriter, r *http.Request) {
		handleJob(w, r, root, r.PathValue("repo"), r.PathValue("sessionID"), options...)
	})

	// Wrap the mux with a Host-header allowlist. Without this, any web page
	// the user visits can DNS-rebind its origin to 127.0.0.1 and read the
	// session JSONL exposed by this viewer (which contains LLM request bodies
	// = source code being reviewed and the LLM's analysis of it).
	allowed := resolveAllowedHostsFromEnv(addr)
	guarded := hostGuard(allowed, mux)

	srv := &http.Server{
		Addr:    addr,
		Handler: guarded,
	}

	fmt.Printf("\nOpen browser: %s\n", BrowserURL(addr))
	return srv.ListenAndServe()
}

var cstZone = func() *time.Location {
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		loc = time.FixedZone("CST", 8*60*60)
	}
	return loc
}()

func formatTime(t time.Time) string {
	return t.In(cstZone).Format("2006-01-02 15:04")
}

func parseTemplate(name string) (*template.Template, error) {
	funcMap := template.FuncMap{
		"nodeChildren": nodeChildren, "nodeDetails": nodeDetails, "prettyJSON": prettyJSON,
		"formatDuration": formatDuration,
		"layoutTimeline": layoutTimeline,
		"formatMillis":   func(ms int64) string { return formatDuration(float64(ms) / 1000) },
		"formatInt":      formatInt,
		"formatTime":     formatTime,
		"truncate":       truncateText,
	}

	content, err := assets.ReadFile("templates/" + name)
	if err != nil {
		return nil, err
	}
	tmpl, err := template.New(name).Funcs(funcMap).Parse(string(content))
	if err != nil {
		return nil, err
	}
	return tmpl.ParseFS(assets, "templates/timeline.html")
}

func truncateText(n int, s string) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func renderTemplate(w http.ResponseWriter, name string, data any) {
	tmpl, err := parseTemplate(name)
	if err != nil {
		http.Error(w, "template error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.Execute(w, data); err != nil {
		// Partially written — just log
		fmt.Fprintf(console.Err(), "[viewer] template execution error: %v\n", err)
	}
}

func staticFS() fs.FS {
	sub, err := fs.Sub(assets, "static")
	if err != nil {
		panic(err)
	}
	return sub
}

func formatDuration(seconds float64) string {
	d := time.Duration(seconds * float64(time.Second))
	if d < time.Minute {
		return fmt.Sprintf("%.1fs", seconds)
	}
	minutes := int(d.Minutes())
	sec := int(d.Seconds()) - minutes*60
	return fmt.Sprintf("%dm%ds", minutes, sec)
}

func formatInt(n int) string {
	s := strconv.Itoa(n)
	sign := ""
	if strings.HasPrefix(s, "-") {
		sign, s = "-", s[1:]
	}
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return sign + s
}
