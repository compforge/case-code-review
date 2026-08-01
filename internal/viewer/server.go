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

	"github.com/qiankunli/case-code-review/internal/console"
)

//go:embed templates/*.html static/style.css
var assets embed.FS

// BrowserURL turns an all-interface listen address into a URL a local browser
// can actually navigate to without changing which interfaces the server binds.
func BrowserURL(addr string) string {
	if strings.HasPrefix(addr, ":") {
		addr = "localhost" + addr
	}
	return "http://" + addr
}

func StartServer(addr string) error {
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
	mux.HandleFunc("/r/{repo}/{sessionID}/review", func(w http.ResponseWriter, r *http.Request) {
		repo := r.PathValue("repo")
		sid := r.PathValue("sessionID")
		if strings.Contains(repo, "..") || strings.Contains(sid, "..") {
			http.Error(w, "invalid path", http.StatusBadRequest)
			return
		}
		handleReview(w, r, root, repo, sid)
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
		"formatDuration": formatDuration,
		"formatMillis":   func(ms int64) string { return formatDuration(float64(ms) / 1000) },
		"formatInt":      formatInt,
		"formatRatio": func(value, total int) string {
			if total == 0 {
				return "-"
			}
			return fmt.Sprintf("%d/%d (%d%%)", value, total, value*100/total)
		},
		"formatSigned": func(n int) string {
			if n > 0 {
				return "+" + formatInt(n)
			}
			return formatInt(n)
		},
		"barPercent": func(value, maximum int) int {
			if value <= 0 || maximum <= 0 {
				return 0
			}
			pct := value * 100 / maximum
			if pct < 1 {
				return 1
			}
			if pct > 100 {
				return 100
			}
			return pct
		},
		"formatTime": formatTime,
		"truncate":   truncateText,
		"add":        func(a, b int) int { return a + b },
		"reviewsOfStage": func(reviews []*ReviewRun, stage string) []*ReviewRun {
			out := make([]*ReviewRun, 0, len(reviews))
			for _, review := range reviews {
				if string(review.Stage) == stage {
					out = append(out, review)
				}
			}
			return out
		},
		"toolCallCount": func(tools []ToolUsage) int {
			total := 0
			for _, tool := range tools {
				total += tool.Calls
			}
			return total
		},
		"systemPromptsFor": func(prompts []SystemPrompt, tasks map[TaskType][]*TaskCard) []SystemPrompt {
			out := make([]SystemPrompt, 0, len(prompts))
			for _, prompt := range prompts {
				for _, taskType := range prompt.TaskTypes {
					if len(tasks[taskType]) > 0 {
						out = append(out, prompt)
						break
					}
				}
			}
			return out
		},
		"taskTypeClass": func(tt TaskType) string {
			switch tt {
			case PlanTask:
				return "task-plan"
			case MainTask:
				return "task-main"
			case MemoryCompressionTask:
				return "task-memory"
			case ReLocationTask:
				return "task-relocation"
			case HypothesisReviewTask:
				return "task-review"
			default:
				return "task-default"
			}
		},
		"orderedTasks": func(tasks map[TaskType][]*TaskCard) []struct {
			Type  TaskType
			Cards []*TaskCard
		} {
			order := []TaskType{PlanTask, MainTask, ReLocationTask, MemoryCompressionTask, HypothesisReviewTask}
			var result []struct {
				Type  TaskType
				Cards []*TaskCard
			}
			for _, tt := range order {
				if cards, ok := tasks[tt]; ok {
					result = append(result, struct {
						Type  TaskType
						Cards []*TaskCard
					}{tt, cards})
				}
			}
			for tt, cards := range tasks {
				if tt != PlanTask && tt != MainTask && tt != ReLocationTask && tt != MemoryCompressionTask && tt != HypothesisReviewTask {
					result = append(result, struct {
						Type  TaskType
						Cards []*TaskCard
					}{tt, cards})
				}
			}
			return result
		},
	}
	content, err := assets.ReadFile("templates/" + name)
	if err != nil {
		return nil, err
	}
	return template.New(name).Funcs(funcMap).Parse(string(content))
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
