package language

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Reference is one source-level name used by a changed snippet. FQN is set
// when imports resolve the name precisely; SourcePath/SourceName point at a
// resolvable Python module for adoption-free doc extraction.
type Reference struct {
	Name       string
	FQN        string
	SourcePath string
	SourceName string
}

var (
	identifier = regexp.MustCompile(`[A-Za-z_$][A-Za-z0-9_$]*`)
	goSelector = regexp.MustCompile(`\b([a-z][A-Za-z0-9_]*)\.([A-Z][A-Za-z0-9_]*)\b`)
)

// ReferencesIn extracts names from a changed snippet and enriches references
// that the source file's imports resolve. It intentionally returns bare names
// alongside precise FQNs: consumers decide whether an FQN hit is authoritative
// enough to suppress same-name fallback.
func (a *Analyzer) ReferencesIn(source Source, snippet string) []Reference {
	lang, _ := Detect(source.Path)
	facts, _ := a.extract(context.Background(), source)
	var out []Reference
	seen := map[Reference]bool{}
	add := func(reference Reference) {
		if reference.Name == "" || seen[reference] {
			return
		}
		seen[reference] = true
		out = append(out, reference)
	}

	switch lang {
	case Go:
		imports := map[string]string{}
		for _, imp := range facts.Imports {
			alias := imp.Alias
			if alias == "" {
				alias = imp.Binding
			}
			if alias != "" && alias != "_" && alias != "." {
				imports[alias] = imp.Path
			}
		}
		for _, match := range goSelector.FindAllStringSubmatch(snippet, -1) {
			if path, ok := imports[match[1]]; ok {
				add(Reference{Name: match[2], FQN: path + "." + match[2]})
			}
		}
	case Python:
		imports := map[string]importedSymbol{}
		for _, imp := range facts.Imports {
			if imp.From == "" {
				continue
			}
			for _, binding := range imp.Bindings {
				imports[binding.Local] = importedSymbol{module: imp.From, name: binding.Name}
			}
			if len(imp.Bindings) == 0 {
				for _, name := range imp.Names {
					local := name
					if imp.Alias != "" {
						local = imp.Alias
					}
					imports[local] = importedSymbol{module: imp.From, name: name}
				}
			}
		}
		roots := pythonModuleRoots(a.repoDir)
		for _, name := range identifier.FindAllString(snippet, -1) {
			if imported, ok := imports[name]; ok {
				reference := Reference{Name: name, FQN: imported.module + "." + imported.name, SourceName: imported.name}
				if path, ok := resolvePythonModuleFile(imported.module, roots); ok {
					reference.SourcePath = path
				}
				add(reference)
			}
		}
	}

	for _, name := range identifier.FindAllString(snippet, -1) {
		add(Reference{Name: name})
	}
	return out
}

type importedSymbol struct{ module, name string }

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
