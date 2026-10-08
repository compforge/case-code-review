package language

import (
	"strings"

	cg "github.com/compforge/codegraph"
)

// projectAnalysis preserves CCR's symbol join keys and review presentation;
// declarations, imports and reference evidence belong to CodeGraph.
func projectAnalysis(source Source, graph *cg.Graph) Analysis {
	path := documentPath(source.Path)
	doc, _ := graph.Node(cg.DocumentID(path))
	a := Analysis{Language: Language(doc.Language), Quality: QualitySyntax}
	if len(graph.Report().Diagnostics) > 0 {
		a.Quality = QualityPartial
	}
	for _, node := range graph.Find(path, "", "") {
		if definition, ok := reviewDefinition(source.Path, node); ok {
			a.Definitions = append(a.Definitions, definition)
		}
	}
	for _, node := range graph.Nodes() {
		if node.Location == nil || node.Location.Path != path {
			continue
		}
		switch node.Kind {
		case cg.Import:
			if node.Binding == nil {
				continue
			}
			b := node.Binding
			imp := Import{Kind: ImportModule, Path: b.Specifier, Alias: b.LocalName, Static: b.Form != "dynamic", Wildcard: b.Form == "wildcard", Span: locationSpan(*node.Location)}
			if a.Language == Python && b.Form == "named" {
				imp.Kind = ImportFrom
				imp.Relative = len(b.Specifier) - len(strings.TrimLeft(b.Specifier, "."))
				imp.From = strings.TrimLeft(b.Specifier, ".")
				imp.Name = b.ImportedName
				imp.Path = imp.From + "." + b.ImportedName
			}
			a.Imports = append(a.Imports, imp)
		case cg.Reference:
			switch node.ReferenceKind {
			case cg.DecoratorReference:
				if len(graph.RelationsFrom(node.ID, cg.Decorates)) > 0 {
					a.Decorators = append(a.Decorators, referenceName(node))
				}
			case cg.CallReference, cg.BaseReference, cg.InterfaceReference:
				for _, edge := range graph.RelationsFrom(node.ID, cg.OccursIn) {
					owner, ok := graph.Node(edge.Target)
					if !ok {
						continue
					}
					d, ok := reviewDefinition(source.Path, owner)
					if !ok {
						continue
					}
					if node.ReferenceKind == cg.CallReference && d.Callable() {
						a.Calls = append(a.Calls, Call{CallerID: d.SymbolID, Name: node.Name})
					}
					if node.ReferenceKind == cg.BaseReference || node.ReferenceKind == cg.InterfaceReference {
						kind := SupertypeKind(node.ReferenceKind)
						if a.Language == Python && kind == SupertypeExtends {
							kind = SupertypeBase
						}
						a.SupertypeReferences = append(a.SupertypeReferences, SupertypeReference{SubtypeID: d.SymbolID, Kind: kind, Supertype: referenceName(node), Span: locationSpan(*node.Location)})
					}
				}
			}
		}
	}
	a.outlineEntries = graphOutlineEntries(graph, path)
	return a
}

func referenceName(n cg.Node) string {
	if n.Receiver != "" {
		return n.Receiver + "." + n.Name
	}
	return n.Name
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
	if node.Location == nil || node.QualifiedName == "" || node.Kind == cg.Reference || node.Kind == cg.Import || node.Kind == cg.Export || node.Kind == cg.DocumentNodeKind {
		return ""
	}
	return SymbolID(node.Location.Path, "", node.QualifiedName)
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
