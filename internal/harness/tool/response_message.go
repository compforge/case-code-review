package tool

// ToolCallResult holds a single tool call and its execution result.
type ToolCallResult struct {
	ToolCallID string // OpenAI-compatible tool call ID
	Name       string // tool name (alias)
	Result     string // output from the tool
}

// TaskCheckpoint mirrors the Java TaskCheckPoint — signals completion or carries data back to the LLM.
type TaskCheckpoint struct {
	Data      string
	Completed bool
}

// Complete returns a checkpoint signaling task completion.
func Complete() TaskCheckpoint { return TaskCheckpoint{Completed: true} }

// CompleteWith returns a completion checkpoint whose receipt is preserved in
// the transcript before the agent loop stops.
func CompleteWith(data string) TaskCheckpoint {
	return TaskCheckpoint{Data: data, Completed: true}
}

// Of returns a checkpoint with data.
func Of(data string) TaskCheckpoint { return TaskCheckpoint{Data: data, Completed: false} }

const ToolNotFoundMsg = "Error: Tool not found. The tool you attempted to call does not exist or is not available. Please check the tool name and try again with a valid tool."
