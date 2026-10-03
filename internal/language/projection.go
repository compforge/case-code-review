package language

import (
	"strings"

	cg "github.com/compforge/codegraph"
)

// projectAnalysis preserves CCR's symbol join keys and review presentation;
// declarations, imports and reference evidence belong to CodeGraph.
func projectAnalysis(source Source, facts cg.Facts, graph *cg.Graph) Analysis {
	a := Analysis{Language: Language(facts.Language), Quality: QualitySyntax, References: map[string]int{}}
	if len(facts.Issues) > 0 {
		a.Quality = QualityPartial
	}
	for _, node := range graph.Find(facts.Path, "", "") {
		if definition, ok := reviewDefinition(source.Path, node); ok {
			a.Definitions = append(a.Definitions, definition)
		}
	}
	for _, call := range facts.Calls {
		// Byte containment avoids assigning a call to a same-line sibling.
		owner := -1
		for i, d := range facts.Declarations {
			kind, ok := reviewKind(d.Kind)
			if !ok || (kind != KindFunction && kind != KindMethod) || d.Location.StartByte > call.Location.StartByte || d.Location.EndByte < call.Location.EndByte {
				continue
			}
			if owner < 0 || d.Location.EndByte-d.Location.StartByte < facts.Declarations[owner].Location.EndByte-facts.Declarations[owner].Location.StartByte {
				owner = i
			}
		}
		if owner >= 0 {
			a.Calls = append(a.Calls, Call{CallerID: SymbolID(source.Path, "", facts.Declarations[owner].QualifiedName), Name: call.Name})
		}
	}
	for _, ref := range facts.References {
		a.References[ref.Name]++
	}
	for _, ref := range facts.TypeRelations {
		if ref.Owner < 0 || ref.Owner >= len(facts.Declarations) {
			continue
		}
		kind := SupertypeKind(ref.Kind)
		if a.Language == Python && kind == SupertypeExtends {
			kind = SupertypeBase
		}
		a.SupertypeReferences = append(a.SupertypeReferences, SupertypeReference{SubtypeID: SymbolID(source.Path, "", facts.Declarations[ref.Owner].QualifiedName), Kind: kind, Supertype: ref.Name, Span: locationSpan(ref.Location)})
	}
	for _, imp := range facts.Imports {
		kind := ImportModule
		if imp.From != "" {
			kind = ImportFrom
		}
		if len(imp.Names) == 0 {
			a.Imports = append(a.Imports, Import{Kind: kind, Path: imp.Path, From: imp.From, Alias: imp.Alias, Relative: imp.Relative, Span: locationSpan(imp.Location)})
		} else {
			for _, name := range imp.Names {
				path := imp.Path
				if a.Language == Python && imp.From != "" {
					path = imp.From + "." + name
				}
				alias := imp.Alias
				for _, binding := range imp.Bindings {
					if binding.Name == name && binding.Local != name {
						alias = binding.Local
					}
				}
				a.Imports = append(a.Imports, Import{Kind: kind, Path: path, From: imp.From, Name: name, Alias: alias, Relative: imp.Relative, Span: locationSpan(imp.Location)})
			}
		}
	}
	var decorators func([]cg.Statement)
	decorators = func(statements []cg.Statement) {
		for _, statement := range statements {
			if statement.Value.Kind == "decorator" {
				if name := expressionName(statement.Value); name != "" {
					a.Decorators = append(a.Decorators, name)
				}
			}
			decorators(statement.Body)
			decorators(statement.Else)
		}
	}
	decorators(facts.Statements)
	a.outlineEntries = graphOutlineEntries(graph, facts.Path)

	return a
}

func reviewKind(kind cg.NodeKind) (Kind, bool) {
	switch kind {
	case cg.Function:
		return KindFunction, true
	case cg.Method, cg.Constructor:
		return KindMethod, true
	case cg.Class:
		return KindClass, true
	case cg.Interface:
		return KindInterface, true
	case cg.Variable, cg.Constant:
		return KindVariable, true
	case cg.Type, cg.TypeAlias, cg.Struct, cg.Enum, cg.Record, cg.Trait, cg.Union:
		return KindType, true
	default:
		return "", false
	}
}

func locationSpan(loc cg.Location) Span {
	end := loc.EndLine
	if loc.EndColumn == 1 && end > loc.Line {
		end--
	}
	return Span{Start: loc.Line, End: end}
}

// ReviewSymbolID translates a graph declaration to the contract/Unit join key.
func ReviewSymbolID(node cg.Node) string {
	if node.Location == nil || node.Kind == cg.DocumentKind {
		return ""
	}
	return SymbolID(node.Location.Path, "", node.QualifiedName)
}

// The statement tree already identifies decorators; CCR selects names for FileRole policy.
func expressionName(expr cg.Expression) string {
	switch expr.Kind {
	case "identifier":
		return expr.Text
	case "attribute":
		if len(expr.Children) > 0 {
			return expressionName(expr.Children[0]) + "." + expr.Text
		}
	case "decorator", "call":
		if len(expr.Children) > 0 {
			return expressionName(expr.Children[0])
		}
	}
	return ""
}

func reviewDefinition(path string, node cg.Node) (Definition, bool) {
	kind, ok := reviewKind(node.Kind)
	if !ok || node.Location == nil {
		return Definition{}, false
	}
	owner := ""
	if i := strings.LastIndex(node.QualifiedName, "."); i >= 0 {
		owner = node.QualifiedName[:i]
	}
	return Definition{SymbolID: SymbolID(path, "", node.QualifiedName), Name: node.QualifiedName, Owner: owner, Kind: kind, Span: locationSpan(*node.Location), Signature: displaySignature(node.Signature)}, true
}
