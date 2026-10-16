package language

import (
	"context"
	"sort"

	"github.com/compforge/repocli/toolkit/go"
)

// reviewGraphSource overlays captured diff bytes when no shared Snapshot was
// supplied. Material enumeration and reading remain repocli capabilities.
type reviewGraphSource struct {
	repocli.GraphSource
	documents map[string]string
	id        string
}

func (s reviewGraphSource) SnapshotID() string {
	if s.id != "" {
		return s.id
	}
	return s.GraphSource.SnapshotID()
}

func (s reviewGraphSource) SourceFiles(ctx context.Context) ([]repocli.SourceFile, error) {
	files, err := s.GraphSource.SourceFiles(ctx)
	if err != nil {
		return nil, err
	}
	sizes := map[string]int64{}
	for _, file := range files {
		sizes[file.Path] = file.Size
	}
	for name, data := range s.documents {
		sizes[name] = int64(len(data))
	}
	out := make([]repocli.SourceFile, 0, len(sizes))
	for name, size := range sizes {
		out = append(out, repocli.SourceFile{Path: name, Size: size})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
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
