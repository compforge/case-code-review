package language

import (
	"context"
	"errors"
	"io/fs"
	"sort"

	"github.com/compforge/repokit/toolkit/go"
)

// reviewGraphSource overlays captured diff bytes when no shared Snapshot was
// supplied. Material enumeration and reading remain RepoKit capabilities.
type reviewGraphSource struct {
	repokit.GraphSource
	documents map[string]string
	id        string
	overlay   *repokit.SourceTree
}

func (s reviewGraphSource) SnapshotID() string {
	if s.id != "" {
		return s.id
	}
	return s.GraphSource.SnapshotID()
}

func newReviewGraphSource(source repokit.GraphSource, documents map[string]string) (reviewGraphSource, error) {
	files := make([]repokit.SourceFile, 0, len(documents))
	for name, data := range documents {
		files = append(files, repokit.SourceFile{Path: name, Size: int64(len(data))})
	}
	tree, err := repokit.NewSourceTree(files)
	return reviewGraphSource{GraphSource: source, documents: documents, overlay: tree}, err
}

func (s reviewGraphSource) ReadDirectory(ctx context.Context, name string) (repokit.SourceDirectory, error) {
	base, err := s.GraphSource.ReadDirectory(ctx, name)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return repokit.SourceDirectory{}, err
	}
	overlay, overlayErr := s.overlay.ReadDirectory(ctx, name)
	if overlayErr != nil && !errors.Is(overlayErr, fs.ErrNotExist) {
		return repokit.SourceDirectory{}, overlayErr
	}
	if err != nil && overlayErr != nil {
		return repokit.SourceDirectory{}, err
	}
	dirs := map[string]bool{}
	files := map[string]repokit.SourceFile{}
	for _, d := range []repokit.SourceDirectory{base, overlay} {
		for _, child := range d.Directories {
			dirs[child] = true
		}
		for _, file := range d.Documents {
			files[file.Path] = file
		}
	}
	var out repokit.SourceDirectory
	for child := range dirs {
		out.Directories = append(out.Directories, child)
	}
	for _, file := range files {
		out.Documents = append(out.Documents, file)
	}
	sort.Strings(out.Directories)
	sort.Slice(out.Documents, func(i, j int) bool { return out.Documents[i].Path < out.Documents[j].Path })
	return out, nil
}
func (s reviewGraphSource) ReadSource(ctx context.Context, name string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if data, ok := s.documents[name]; ok {
		return data, nil
	}
	return s.GraphSource.ReadSource(ctx, name)
}
