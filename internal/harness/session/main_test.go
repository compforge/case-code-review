package session

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/qiankunli/case-code-review/internal/telemetry"
)

func TestMain(m *testing.M) {
	shutdownTimeline, err := telemetry.InitTimeline()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	code := m.Run()
	if err := shutdownTimeline(context.Background()); err != nil {
		fmt.Fprintln(os.Stderr, err)
		code = 1
	}
	os.Exit(code)
}
