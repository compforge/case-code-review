package unit

import (
	"context"
	"github.com/compforge/repocli/toolkit/go"
	"github.com/qiankunli/case-code-review/internal/unit/change"
)

// Existing language fixtures test the review binding of native repocli fragments.
type AutoSplitter struct{}

func (AutoSplitter) Split(ch change.Change) ([]Fragment, error) {
	fs, err := repocli.SplitChange(context.Background(), ch)
	if err != nil {
		return nil, err
	}
	return BindFragments(context.Background(), fs, []change.Change{ch}, "", nil, nil)
}
