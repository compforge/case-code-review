package language

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/qiankunli/case-code-review/internal/pathutil"
	"github.com/qiankunli/case-code-review/internal/sourceview"
	"golang.org/x/mod/modfile"
	"golang.org/x/mod/module"
	"golang.org/x/mod/sumdb/dirhash"
)

type GoDependencyReader struct {
	readSnapshot   func(context.Context, string) (string, error)
	maxLines       int
	maxResultBytes int
	goRoot         string
	moduleCache    string
}

// NewGoDependencyReader reads local dependency material selected by the fixed
// review manifest. Source parsing and symbol binding remain owned by CodeGraph.
func NewGoDependencyReader(readSnapshot func(context.Context, string) (string, error), maxLines, maxResultBytes int) *GoDependencyReader {
	return &GoDependencyReader{readSnapshot: readSnapshot, maxLines: maxLines, maxResultBytes: maxResultBytes, goRoot: runtime.GOROOT(), moduleCache: goModCache()}
}

type goDependencyRequest struct {
	// ReviewedPath is supplied by the review handler, never chosen by the model.
	ReviewedPath string `json:"_reviewed_path"`
	ImportPath   string `json:"import_path"`
	FilePath     string `json:"file_path"`
	Start        int    `json:"start_line"`
	End          int    `json:"end_line"`
}

func (p *GoDependencyReader) Execute(ctx context.Context, args map[string]any) (string, error) {
	data, err := json.Marshal(args)
	if err != nil {
		return "", err
	}
	var request goDependencyRequest
	if err := json.Unmarshal(data, &request); err != nil {
		return "", fmt.Errorf("invalid dependency source arguments: %w", err)
	}
	result := sourceview.DependencySource{ImportPath: request.ImportPath}
	if err := p.read(ctx, request, &result); err != nil {
		result.Status = "unavailable"
		result.Message = err.Error()
	}
	data, err = json.Marshal(result)
	if err == nil && len(data) > p.maxResultBytes {
		data, err = json.Marshal(sourceview.DependencySource{Status: "unavailable", ImportPath: result.ImportPath, Message: "source result exceeds the tool budget; request a smaller source range"})
	}
	return string(data), err
}

func (p *GoDependencyReader) read(ctx context.Context, request goDependencyRequest, result *sourceview.DependencySource) error {
	if err := module.CheckImportPath(result.ImportPath); err != nil {
		return fmt.Errorf("invalid Go import path")
	}
	manifestPath, manifest, err := p.manifest(ctx, request.ReviewedPath)
	if err != nil {
		return err
	}
	parsed, err := modfile.Parse(manifestPath, []byte(manifest), nil)
	if err != nil {
		return fmt.Errorf("reviewed %s could not be parsed", manifestPath)
	}
	result.ManifestPath = manifestPath
	if parsed.Go != nil {
		result.DeclaredGo = parsed.Go.Version
	}
	var packageDir string
	first := strings.Split(result.ImportPath, "/")[0]
	if !strings.Contains(first, ".") {
		result.Module = "stdlib"
		result.Version = runtime.Version()
		packageDir = filepath.Join(p.goRoot, "src", filepath.FromSlash(result.ImportPath))
	} else {
		var selected module.Version
		for _, require := range parsed.Require {
			if (result.ImportPath == require.Mod.Path || strings.HasPrefix(result.ImportPath, require.Mod.Path+"/")) && len(require.Mod.Path) > len(selected.Path) {
				selected = require.Mod
			}
		}
		if selected.Path == "" {
			return fmt.Errorf("import path has no declared dependency version in reviewed go.mod")
		}
		relative := strings.TrimPrefix(strings.TrimPrefix(result.ImportPath, selected.Path), "/")
		for _, replacement := range parsed.Replace {
			if replacement.Old.Path == selected.Path && (replacement.Old.Version == "" || replacement.Old.Version == selected.Version) {
				if replacement.New.Version == "" {
					return fmt.Errorf("local replacement has no locked module-cache version; use repository source tools")
				}
				selected = replacement.New
				break
			}
		}
		result.Module = selected.Path
		result.Version = selected.Version
		sums, err := p.readSnapshot(ctx, path.Join(path.Dir(manifestPath), "go.sum"))
		if err != nil {
			return fmt.Errorf("reviewed snapshot has no readable go.sum")
		}
		for _, line := range strings.Split(sums, "\n") {
			fields := strings.Fields(line)
			if len(fields) == 3 && fields[0] == selected.Path && fields[1] == selected.Version {
				result.Checksum = fields[2]
				break
			}
		}
		if result.Checksum == "" {
			return fmt.Errorf("declared dependency has no source checksum in reviewed go.sum")
		}
		escapedPath, err := module.EscapePath(selected.Path)
		if err != nil {
			return err
		}
		escapedVersion, err := module.EscapeVersion(selected.Version)
		if err != nil {
			return err
		}
		directory := filepath.Join(p.moduleCache, escapedPath+"@"+escapedVersion)
		if err := ctx.Err(); err != nil {
			return err
		}
		// Verify unpacked source, not only the download cache's ziphash: local
		// modifications must never masquerade as the locked dependency source.
		hash, err := dirhash.HashDir(directory, selected.Path+"@"+selected.Version, dirhash.Hash1)
		if err != nil {
			return fmt.Errorf("locked dependency source is unavailable locally; this tool does not download modules")
		}
		if hash != result.Checksum {
			return fmt.Errorf("module-cache source does not match the reviewed go.sum checksum")
		}
		packageDir = filepath.Join(directory, filepath.FromSlash(relative))
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	packageDir, err = pathutil.CanonicalPath(packageDir)
	if err != nil {
		return fmt.Errorf("dependency package source is unavailable locally")
	}
	file := request.FilePath
	if file == "" {
		entries, err := os.ReadDir(packageDir)
		if err != nil {
			return fmt.Errorf("dependency package source is unavailable locally")
		}
		result.Status = "discovery"
		for _, entry := range entries {
			if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".go") && !strings.HasSuffix(entry.Name(), "_test.go") {
				result.Files = append(result.Files, entry.Name())
				if len(result.Files) == p.maxLines {
					break
				}
			}
		}
		return nil
	}
	if filepath.Base(file) != file || !strings.HasSuffix(file, ".go") {
		return fmt.Errorf("file_path must be one package-relative Go filename")
	}
	full := filepath.Join(packageDir, file)
	resolved, err := filepath.EvalSymlinks(full)
	if err != nil {
		return fmt.Errorf("requested dependency source file is unavailable")
	}
	if !pathutil.WithinBase(packageDir, resolved) {
		return fmt.Errorf("dependency source file escapes its package directory")
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 2<<20 {
		return fmt.Errorf("dependency source exceeds the source read limit")
	}
	source, err := os.ReadFile(resolved)
	if err != nil {
		return fmt.Errorf("dependency source could not be read")
	}
	sum := sha256.Sum256(source)
	result.Ref = fmt.Sprintf("%s@%s/%s#sha256=%x", result.Module, result.Version, result.ImportPath+"/"+file, sum)
	result.Path = result.ImportPath + "/" + file
	lines := strings.Split(strings.TrimSuffix(string(source), "\n"), "\n")
	result.Total = len(lines)
	start := request.Start
	if start <= 0 {
		start = 1
	}
	end := request.End
	if end <= 0 {
		end = start + p.maxLines - 1
	}
	end = min(end, start+p.maxLines-1, len(lines))
	if start > len(lines) || end < start {
		return fmt.Errorf("requested range is outside the dependency source file")
	}
	var body strings.Builder
	result.Start = start
	result.End = start - 1
	for i := start - 1; i < end; i++ {
		line := fmt.Sprintf("%d|%s\n", i+1, lines[i])
		if body.Len()+len(line) > p.maxResultBytes/2 {
			break
		}
		body.WriteString(line)
		result.End = i + 1
	}
	if result.End < result.Start {
		return fmt.Errorf("requested source line exceeds the tool result limit; choose a narrower file")
	}
	result.Status = "read"
	result.Content = fmt.Sprintf("Dependency: %s@%s (project Go %s; checksum %s)\nFile: %s (%d lines total, showing lines %d-%d)\nSnapshot: dependency; Ref: %s\n%s", result.Module, result.Version, result.DeclaredGo, result.Checksum, result.Path, result.Total, result.Start, result.End, result.Ref, body.String())
	return nil
}

// manifest selects dependency declarations from the reviewed file's module,
// including nested modules, without consulting the live working tree.
func (p *GoDependencyReader) manifest(ctx context.Context, reviewedPath string) (string, string, error) {
	directory := "."
	if reviewedPath != "" {
		reviewedPath = path.Clean(reviewedPath)
		if !fs.ValidPath(reviewedPath) {
			return "", "", fmt.Errorf("invalid reviewed source path")
		}
		directory = path.Dir(reviewedPath)
	}
	for {
		manifestPath := path.Join(directory, "go.mod")
		body, err := p.readSnapshot(ctx, manifestPath)
		if err == nil {
			return manifestPath, body, nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return "", "", fmt.Errorf("reviewed %s is unavailable: %w", manifestPath, err)
		}
		if directory == "." {
			return "", "", fmt.Errorf("reviewed source has no readable go.mod")
		}
		directory = path.Dir(directory)
	}
}
