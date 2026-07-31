package tool

import "testing"

func TestNamedAllowsDomainTools(t *testing.T) {
	domainTool := Named("code_comment")
	if !domainTool.IsKnown() || OfName(domainTool.Name()) != domainTool {
		t.Fatalf("domain tool must remain a stable Harness identity: %+v", domainTool)
	}
}

func TestZeroToolIsUnknown(t *testing.T) {
	var zero Tool
	if zero.IsKnown() {
		t.Fatal("zero-value Tool must not be treated as a registered identity")
	}
}
