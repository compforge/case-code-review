package tool

import "testing"

func TestNamedAllowsDomainTools(t *testing.T) {
	domainTool := Named("code_comment")
	if !domainTool.IsKnown() || OfName(domainTool.Name()) != domainTool {
		t.Fatalf("domain tool must remain a stable Harness identity: %+v", domainTool)
	}
}
