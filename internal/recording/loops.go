package recording

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/compforge/agentgo/codec"
	agentsession "github.com/compforge/agentgo/session"
	"github.com/compforge/go-stdx/uuid"
	"github.com/qiankunli/case-code-review/internal/harness/msg"
)

// OpenLoop opens a local conversation. Continuations reuse the same file; a
// fresh loop gets its own Session. Review restart/recovery is not supported.
func (sh *Review) OpenLoop(previous string, options ...codec.Option) (*agentsession.Session, string, error) {
	if sh == nil || sh.persist == nil {
		return nil, "", nil
	}
	codec, err := msg.Codec(options...)
	if err != nil {
		return nil, "", err
	}
	dir, err := sh.artifactPath(".sessions")
	if err != nil {
		return nil, "", err
	}
	if err = os.MkdirAll(dir, 0700); err != nil {
		return nil, "", err
	}
	if previous != "" {
		if filepath.Dir(previous) != dir {
			return nil, "", fmt.Errorf("loop session belongs to another review")
		}
		s, err := agentsession.Open(previous, codec)
		return s, previous, err
	}
	path := filepath.Join(dir, uuid.V4()+".jsonl")
	s, err := agentsession.Create(path, codec)
	return s, path, err
}
