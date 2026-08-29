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
definitions, members, calls, supertype_references, imports, decorators, references = [], [], [], [], [], [], {}
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
def annotation_text(node):
    try:
        return ast.unparse(node)
    except Exception:
        return ""
def add_class_members(node, owner):
    for child in node.body:
        names, annotation = [], ""
        if isinstance(child, ast.AnnAssign) and isinstance(child.target, ast.Name):
            names = [child.target.id]
            annotation = annotation_text(child.annotation)
        elif isinstance(child, ast.Assign):
            names = [target.id for target in child.targets if isinstance(target, ast.Name)]
        for name in names:
            declared = f"{name}: {annotation}" if annotation else name
            members.append({"name": f"{owner}.{name}", "owner": owner, "label": "attribute",
                            "start": child.lineno, "end": getattr(child, "end_lineno", child.lineno),
                            "signature": declared})
def visit_scope(node, owners):
    for child in ast.iter_child_nodes(node):
        if isinstance(child, ast.ClassDef):
            name = ".".join(owners + [child.name])
            definitions.append({"name": name, "owner": ".".join(owners), "kind": "class",
                                "start": start(child), "end": child.end_lineno, "signature": signature(child)})
            for base in child.bases:
                target = annotation_text(base)
                if target:
                    supertype_references.append({"subtype": name, "kind": "base", "supertype": target,
                                                 "start": base.lineno,
                                                 "end": getattr(base, "end_lineno", base.lineno)})
            add_class_members(child, name)
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
    if isinstance(node, ast.Import):
        for imported in node.names:
            imports.append({"kind": "import", "path": imported.name,
                            "name": imported.name.rsplit(".", 1)[-1], "alias": imported.asname or "",
                            "start": node.lineno, "end": getattr(node, "end_lineno", node.lineno)})
    elif isinstance(node, ast.ImportFrom):
        module = node.module or ""
        for imported in node.names:
            path = module if imported.name == "*" else ".".join(filter(None, [module, imported.name]))
            imports.append({"kind": "from_import", "path": path, "from": module,
                            "name": imported.name, "alias": imported.asname or "",
                            "wildcard": imported.name == "*", "relative": node.level,
                            "start": node.lineno, "end": getattr(node, "end_lineno", node.lineno)})
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
json.dump({"definitions": definitions, "members": members, "calls": calls,
           "supertype_references": supertype_references, "imports": imports,
           "decorators": decorators, "references": references}, sys.stdout)
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
		Members []struct {
			Name      string `json:"name"`
			Owner     string `json:"owner"`
			Label     string `json:"label"`
			Start     int    `json:"start"`
			End       int    `json:"end"`
			Signature string `json:"signature"`
		} `json:"members"`
		Calls []struct {
			Caller string `json:"caller"`
			Name   string `json:"name"`
		} `json:"calls"`
		SupertypeReferences []struct {
			Subtype   string        `json:"subtype"`
			Kind      SupertypeKind `json:"kind"`
			Supertype string        `json:"supertype"`
			Start     int           `json:"start"`
			End       int           `json:"end"`
		} `json:"supertype_references"`
		Imports []struct {
			Kind     ImportKind `json:"kind"`
			Path     string     `json:"path"`
			From     string     `json:"from"`
			Name     string     `json:"name"`
			Alias    string     `json:"alias"`
			Wildcard bool       `json:"wildcard"`
			Relative int        `json:"relative"`
			Start    int        `json:"start"`
			End      int        `json:"end"`
		} `json:"imports"`
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
	for _, member := range payload.Members {
		analysis.outlineMembers = append(analysis.outlineMembers, outlineMember{
			Name: member.Name, Owner: member.Owner, Label: member.Label,
			Span: Span{Start: member.Start, End: member.End}, Signature: member.Signature,
		})
	}
	for _, call := range payload.Calls {
		analysis.Calls = append(analysis.Calls, Call{
			CallerID: SymbolID(source.Path, "", call.Caller), Name: call.Name,
		})
	}
	for _, reference := range payload.SupertypeReferences {
		analysis.SupertypeReferences = append(analysis.SupertypeReferences, SupertypeReference{
			SubtypeID: SymbolID(source.Path, "", reference.Subtype),
			Kind:      reference.Kind,
			Supertype: reference.Supertype,
			Span:      Span{Start: reference.Start, End: reference.End},
		})
	}
	for _, imported := range payload.Imports {
		analysis.Imports = append(analysis.Imports, Import{
			Kind: imported.Kind, Path: imported.Path, From: imported.From,
			Name: imported.Name, Alias: imported.Alias, Wildcard: imported.Wildcard,
			Relative: imported.Relative, Span: Span{Start: imported.Start, End: imported.End},
		})
	}
	return analysis, nil
}
