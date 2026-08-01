package language

import (
	"context"
	"encoding/json"
	"os/exec"
	"strings"
	"time"
)

const pythonAnalysisScript = `
import ast, json, sys
try:
    source = sys.stdin.read()
    tree = ast.parse(source)
except SyntaxError:
    sys.exit(2)
lines = source.splitlines()
definitions, calls, decorators, references = [], [], [], {}
def dotted_name(node):
    if isinstance(node, ast.Call):
        return dotted_name(node.func)
    if isinstance(node, ast.Name):
        return node.id
    if isinstance(node, ast.Attribute):
        base = dotted_name(node.value)
        return f"{base}.{node.attr}" if base else node.attr
    return None
def start(n):
    return n.decorator_list[0].lineno if getattr(n, "decorator_list", None) else n.lineno
def signature(n):
    i = n.lineno - 1
    return " ".join(lines[i].split()) if 0 <= i < len(lines) else ""
def visit_scope(node, owners):
    for child in ast.iter_child_nodes(node):
        if isinstance(child, ast.ClassDef):
            name = ".".join(owners + [child.name])
            definitions.append({"name": name, "owner": ".".join(owners), "kind": "class",
                                "start": start(child), "end": child.end_lineno, "signature": signature(child)})
            visit_scope(child, owners + [child.name])
        elif isinstance(child, (ast.FunctionDef, ast.AsyncFunctionDef)):
            name = ".".join(owners + [child.name])
            kind = "method" if owners else "function"
            definitions.append({"name": name, "owner": ".".join(owners), "kind": kind,
                                "start": start(child), "end": child.end_lineno, "signature": signature(child)})
            seen = set()
            for nested in ast.walk(child):
                if isinstance(nested, ast.Call):
                    fn = nested.func
                    called = fn.id if isinstance(fn, ast.Name) else (fn.attr if isinstance(fn, ast.Attribute) else None)
                    if called and called not in seen:
                        seen.add(called); calls.append({"caller": name, "name": called})
visit_scope(tree, [])
for node in ast.walk(tree):
    if isinstance(node, (ast.ClassDef, ast.FunctionDef, ast.AsyncFunctionDef)):
        for decorator in node.decorator_list:
            name = dotted_name(decorator)
            if name and name not in decorators:
                decorators.append(name)
    name = None
    if isinstance(node, ast.Name) and len(node.id) >= 3:
        name = node.id
    elif isinstance(node, ast.Attribute) and len(node.attr) >= 3:
        name = node.attr
    if name:
        references[name] = references.get(name, 0) + 1
json.dump({"definitions": definitions, "calls": calls, "decorators": decorators, "references": references}, sys.stdout)
`

func analyzePython(parent context.Context, source Source) (Analysis, error) {
	ctx, cancel := context.WithTimeout(parent, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "python3", "-c", pythonAnalysisScript)
	cmd.Stdin = strings.NewReader(source.Content)
	out, err := cmd.Output()
	if err != nil {
		return Analysis{}, err
	}
	var payload struct {
		Definitions []struct {
			Name      string `json:"name"`
			Owner     string `json:"owner"`
			Kind      Kind   `json:"kind"`
			Start     int    `json:"start"`
			End       int    `json:"end"`
			Signature string `json:"signature"`
		} `json:"definitions"`
		Calls []struct {
			Caller string `json:"caller"`
			Name   string `json:"name"`
		} `json:"calls"`
		Decorators []string       `json:"decorators"`
		References map[string]int `json:"references"`
	}
	if err := json.Unmarshal(out, &payload); err != nil {
		return Analysis{}, err
	}
	analysis := Analysis{
		Language: Python, Quality: QualitySyntax,
		Decorators: payload.Decorators, References: payload.References,
	}
	for _, d := range payload.Definitions {
		analysis.Definitions = append(analysis.Definitions, Definition{
			SymbolID: SymbolID(source.Path, "", d.Name), Name: d.Name, Owner: d.Owner,
			Kind: d.Kind, Span: Span{Start: d.Start, End: d.End}, Signature: d.Signature,
		})
	}
	for _, call := range payload.Calls {
		analysis.Calls = append(analysis.Calls, Call{
			CallerID: SymbolID(source.Path, "", call.Caller), Name: call.Name,
		})
	}
	return analysis, nil
}
