package runner

import (
	"github.com/compforge/agentgo/codec"
	"github.com/qiankunli/case-code-review/internal/runner/hypothesisreview"
)

// SessionCodecOptions exposes the message codecs owned by the Review domain.
// Observation callers can decode a Session without creating a Runner or Agent.
func SessionCodecOptions() []codec.Option {
	return append(clueCodecOptions(), hypothesisreview.SessionCodecOptions()...)
}
