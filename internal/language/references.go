package language

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	cg "github.com/compforge/codegraph"
)

// Reference is a review projection of a graph-proven use. SymbolID identifies a
// retained declaration; FQN can identify an explicit external import binding.
type Reference struct {
	Name, SymbolID, FQN, SourcePath, SourceName string
}

func (r *RepositoryIndex) Declaration(id string) (cg.Node, bool) {
	if r == nil || r.Graph == nil {
		return cg.Node{}, false
	}
	path, name, ok := SplitSymbolID(id)
	if !ok {
		return cg.Node{}, false
	}
	nodes := r.Graph.Find(path, "", name)
	if len(nodes) != 1 || ReviewSymbolID(nodes[0]) == "" {
		return cg.Node{}, false
	}
	return nodes[0], true
}

// Owners follows semantic membership first, including Go receivers in another
// document, then lexical nesting. Presentation names never establish ownership.
func (r *RepositoryIndex) Owners(id string) []string {
	node, ok := r.Declaration(id)
	if !ok {
		return nil
	}
	var out []string
	for _, kind := range []cg.RelationKind{cg.Contains, cg.Encloses} {
		for _, e := range r.Graph.RelationsTo(node.ID, kind) {
			if !e.Confidence.AtLeast(cg.Scoped) {
				continue
			}
			n, ok := r.Graph.Node(e.Source)
			if !ok {
				continue
			}
			key := ReviewSymbolID(n)
			if _, ok := r.Declaration(key); ok {
				out = append(out, key)
			}
		}
		if len(out) > 0 {
			break
		}
	}
	sort.Strings(out)
	return out
}

// ReferencesAt selects source uses by captured coordinates. It never tokenizes
// diff text or guesses endpoints from bare names. Alias traversal is bounded by
// the publication's nodes; missing and cyclic bindings remain unresolved.
func (a *Analyzer) ReferencesAt(path string, spans []Span) []Reference {
	r := a.Repository()
	if r.Graph == nil || len(spans) == 0 {
		return nil
	}
	var out []Reference
	seen := map[Reference]bool{}
	for _, use := range r.Graph.Nodes() {
		if use.Kind != cg.Reference || use.Location == nil || use.Location.Path != path {
			continue
		}
		selected := false
		for _, span := range spans {
			if use.Location.Line <= span.End && locationSpan(*use.Location).End >= span.Start {
				selected = true
				break
			}
		}
		if !selected {
			continue
		}
		queue := []string{}
		for _, e := range r.Graph.RelationsFrom(use.ID, cg.References) {
			if e.Confidence.AtLeast(cg.Scoped) {
				queue = append(queue, e.Target)
			}
		}
		visited := map[string]bool{}
		for len(queue) > 0 {
			id := queue[0]
			queue = queue[1:]
			if visited[id] {
				continue
			}
			visited[id] = true
			n, ok := r.Graph.Node(id)
			if !ok {
				continue
			}
			ref := Reference{Name: use.Name, SymbolID: ReviewSymbolID(n)}
			if ref.SymbolID != "" {
				if _, ok := r.Declaration(ref.SymbolID); !ok {
					continue
				}
			}
			aliases := r.Graph.RelationsFrom(id, cg.Aliases)
			for _, e := range aliases {
				if e.Confidence.AtLeast(cg.Scoped) {
					queue = append(queue, e.Target)
				}
			}
			// The import binding proves this public name, even when its declaration is
			// outside the supplied materials. A receiver spelling alone proves no binding.
			if len(aliases) == 0 && n.Kind == cg.Import && n.Language == "python" && n.Binding != nil {
				b := n.Binding
				if b.Form == "named" && b.ImportedName != "" && !strings.HasPrefix(b.Specifier, ".") {
					ref.FQN = b.Specifier + "." + b.ImportedName
					ref.SourceName = b.ImportedName
					if a.ref == "" {
						ref.SourcePath, _ = resolvePythonModuleFile(b.Specifier, pythonModuleRoots(a.repoDir))
					}
				}
			}
			if ref.SymbolID == "" && ref.FQN == "" {
				continue
			}
			if !seen[ref] {
				seen[ref] = true
				out = append(out, ref)
			}
		}
	}
	return out
}

// UsesOf reads occurrence edges, following the reverse alias chain so a call
// through a barrel/import still belongs to the final declaration. It does not
// count declaration-level summary edges as additional source occurrences.
func (r *RepositoryIndex) UsesOf(symbol string) []cg.Node {
	target, ok := r.Declaration(symbol)
	if !ok {
		return nil
	}
	queue := []string{target.ID}
	visited := map[string]bool{}
	uses := map[string]cg.Node{}
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		if visited[id] {
			continue
		}
		visited[id] = true
		for _, e := range r.Graph.RelationsTo(id, cg.Aliases, cg.References) {
			if !e.Confidence.AtLeast(cg.Scoped) {
				continue
			}
			n, ok := r.Graph.Node(e.Source)
			if !ok {
				continue
			}
			if e.Kind == cg.Aliases {
				queue = append(queue, n.ID)
			} else if n.Kind == cg.Reference && n.Location != nil {
				uses[n.ID] = n
			}
		}
	}
	out := make([]cg.Node, 0, len(uses))
	for _, n := range uses {
		out = append(out, n)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i].Location, out[j].Location
		if a.Path != b.Path {
			return a.Path < b.Path
		}
		if a.StartByte != b.StartByte {
			return a.StartByte < b.StartByte
		}
		return out[i].ID < out[j].ID
	})
	return out
}

func pythonModuleRoots(repoDir string) []string {
	if repoDir == "" {
		return nil
	}
	var roots []string
	if venv := os.Getenv("VIRTUAL_ENV"); venv != "" {
		roots = append(roots, PythonSitePackageDirs(venv)...)
	}
	roots = append(roots, PythonSitePackageDirs(filepath.Join(repoDir, ".venv"))...)
	return append(roots, repoDir)
}

func resolvePythonModuleFile(module string, roots []string) (string, bool) {
	relative := filepath.FromSlash(strings.ReplaceAll(module, ".", "/"))
	for _, root := range roots {
		for _, candidate := range []string{filepath.Join(root, relative+".py"), filepath.Join(root, relative, "__init__.py")} {
			if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
				return candidate, true
			}
		}
	}
	return "", false
}

// PythonSitePackageDirs returns the conventional dependency roots inside a
// virtual environment on POSIX and Windows.
func PythonSitePackageDirs(venv string) []string {
	var out []string
	if matches, err := filepath.Glob(filepath.Join(venv, "lib", "python*", "site-packages")); err == nil {
		out = append(out, matches...)
	}
	if windows := filepath.Join(venv, "Lib", "site-packages"); directoryExists(windows) {
		out = append(out, windows)
	}
	return out
}

func directoryExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}
