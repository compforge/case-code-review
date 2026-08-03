package language

import (
	"strings"

	gotreesitter "github.com/odvcencio/gotreesitter"
)

// treeSitterDataMembers handles the common field_declaration shape used by
// Java and several C-family grammars. Unsupported grammars simply contribute
// no data members; their callable/type outline remains available.
func treeSitterDataMembers(source Source, tree *gotreesitter.Tree) []outlineMember {
	lang := tree.Language()
	var members []outlineMember
	var walk func(*gotreesitter.Node, string)
	walk = func(node *gotreesitter.Node, owner string) {
		if node == nil {
			return
		}
		typ := node.Type(lang)
		switch typ {
		case "class_declaration", "interface_declaration", "record_declaration", "enum_declaration", "annotation_type_declaration":
			if name := treeSitterNodeName(node, lang, source.Content); name != "" {
				owner = joinOutlineOwner(owner, name)
			}
		case "field_declaration", "constant_declaration":
			members = append(members, treeSitterFieldMembers(source, node, owner, "field", lang)...)
			return
		case "record_component":
			if owner != "" {
				name := treeSitterNodeName(node, lang, source.Content)
				if name != "" {
					members = append(members, outlineMember{
						Name: joinOutlineOwner(owner, name), Owner: owner, Label: "field",
						Signature: compactNodeText(node, tree.Source()), Span: nodeSpan(node),
					})
				}
			}
			return
		}
		for i := 0; i < node.NamedChildCount(); i++ {
			walk(node.NamedChild(i), owner)
		}
	}
	walk(tree.RootNode(), "")
	return members
}

func treeSitterFieldMembers(source Source, declaration *gotreesitter.Node, owner, label string, lang *gotreesitter.Language) []outlineMember {
	if owner == "" {
		return nil
	}
	typeText := ""
	if node := declaration.ChildByFieldName("type", lang); node != nil {
		typeText = compactNodeText(node, []byte(source.Content))
	}
	modifiers := ""
	for i := 0; i < declaration.NamedChildCount(); i++ {
		child := declaration.NamedChild(i)
		if child != nil && child.Type(lang) == "modifiers" {
			modifiers = compactNodeText(child, []byte(source.Content))
			break
		}
	}
	var out []outlineMember
	for i := 0; i < declaration.NamedChildCount(); i++ {
		child := declaration.NamedChild(i)
		if child == nil || child.Type(lang) != "variable_declarator" {
			continue
		}
		name := treeSitterNodeName(child, lang, source.Content)
		if name == "" {
			continue
		}
		signature := strings.TrimSpace(strings.Join(nonEmptyStrings(modifiers, typeText, name), " "))
		out = append(out, outlineMember{
			Name: joinOutlineOwner(owner, name), Owner: owner, Label: label,
			Signature: signature, Span: nodeSpan(child),
		})
	}
	return out
}

func treeSitterTypeScriptMembers(source Source, tree *gotreesitter.Tree) []outlineMember {
	lang := tree.Language()
	var members []outlineMember
	var walk func(*gotreesitter.Node, string)
	walk = func(node *gotreesitter.Node, owner string) {
		if node == nil {
			return
		}
		typ := node.Type(lang)
		switch typ {
		case "internal_module", "ambient_declaration":
			if name := treeSitterNodeName(node, lang, source.Content); name != "" {
				owner = joinOutlineOwner(owner, name)
			}
		case "class_declaration", "class", "interface_declaration":
			if name := treeSitterNodeName(node, lang, source.Content); name != "" {
				owner = joinOutlineOwner(owner, name)
			}
		case "public_field_definition", "field_definition", "property_signature":
			name := treeSitterNodeName(node, lang, source.Content)
			if owner != "" && name != "" && treeSitterFunctionLike(treeSitterNodeValue(node, lang), lang) == nil {
				members = append(members, outlineMember{
					Name: joinOutlineOwner(owner, name), Owner: owner, Label: "property",
					Signature: treeSitterDeclarationHeader(node, lang, source.Content), Span: nodeSpan(node),
				})
			}
			return
		}
		for i := 0; i < node.NamedChildCount(); i++ {
			walk(node.NamedChild(i), owner)
		}
	}
	walk(tree.RootNode(), "")
	return members
}

func treeSitterDeclarationHeader(node *gotreesitter.Node, lang *gotreesitter.Language, content string) string {
	end := node.EndByte()
	for _, field := range []string{"value", "initializer"} {
		if value := node.ChildByFieldName(field, lang); value != nil && value.StartByte() > node.StartByte() {
			end = value.StartByte()
			break
		}
	}
	if int(end) > len(content) || node.StartByte() >= end {
		return ""
	}
	header := strings.TrimSpace(content[node.StartByte():end])
	header = strings.TrimSpace(strings.TrimSuffix(header, "="))
	header = strings.TrimSuffix(header, ";")
	return strings.Join(strings.Fields(header), " ")
}

func compactNodeText(node *gotreesitter.Node, content []byte) string {
	if node == nil {
		return ""
	}
	return strings.Join(strings.Fields(node.Text(content)), " ")
}

func nodeSpan(node *gotreesitter.Node) Span {
	return Span{Start: int(node.StartPoint().Row) + 1, End: int(node.EndPoint().Row) + 1}
}

func joinOutlineOwner(owner, name string) string {
	if owner == "" {
		return name
	}
	return owner + "." + name
}

func nonEmptyStrings(values ...string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value != "" {
			out = append(out, value)
		}
	}
	return out
}
