package telemetry

import (
	"context"
	"fmt"
	"github.com/qiankunli/case-code-review/internal/console"
	"github.com/qiankunli/case-code-review/internal/recording/operation"
)

// BeginOperation preserves cancellation and explicit business parentage.
func BeginOperation(ctx context.Context, name string, opts ...operation.Option) (context.Context, operation.Handle) {
	ref, ok := operation.FromContext(ctx)
	if !ok || ref.ReviewID == "" {
		return ctx, operation.Handle{}
	}
	h, err := operation.Begin(ref.ReviewID, name, opts...)
	if err != nil {
		ReportRecordingError(err)
	}
	return operation.NewContext(ctx, operation.Ref{ReviewID: ref.ReviewID, OperationID: h.ID()}), h
}
func EndOperation(_ context.Context, h operation.Handle, err error, opts ...operation.EndOption) {
	ReportRecordingError(h.End(err, opts...))
}
func ReportRecordingError(err error) {
	if err != nil {
		fmt.Fprintf(console.Err(), "[ccr recording] operation failed: %v\n", err)
	}
}
