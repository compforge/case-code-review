package language

import (
	"context"
	"fmt"
	"strings"
	"time"

	gotreesitter "github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
)

const (
	treeSitterTimeout           = 10 * time.Second
	maxTreeSitterSignatureRunes = 240
)

type treeSitterDefinition struct {
	Definition
	startByte uint32
	endByte   uint32
}

// analyzeTreeSitter is the common fallback for languages without a
// higher-fidelity ccr backend. It intentionally returns only the stable facts
// Analysis exposes; grammar nodes and query captures stop at this boundary.
func analyzeTreeSitter(ctx context.Context, language Language, source Source) (Analysis, error) {
	entry := grammars.DetectLanguageByName(string(language))
	if entry == nil {
		return Analysis{}, fmt.Errorf("%w: %s", ErrUnsupported, source.Path)
	}
	if err := ctx.Err(); err != nil {
		return Analysis{}, err
	}

	lang := entry.Language()
	parser := gotreesitter.NewParser(lang)
	timeout := treeSitterTimeout
	if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) < timeout {
		timeout = max(time.Until(deadline), time.Microsecond)
	}
	parser.SetTimeoutMicros(uint64(timeout / time.Microsecond))
	content := []byte(source.Content)

	var tree *gotreesitter.Tree
	var err error
	if entry.TokenSourceFactory != nil {
		tree, err = parser.ParseWithTokenSourceStrict(content, entry.TokenSourceFactory(content, lang))
	} else {
		tree, err = parser.ParseStrict(content)
	}
	// Strict parsing may return a partial tree together with a timeout or
	// cancellation error. Release it even though ccr discards partial facts.
	if tree != nil {
		defer tree.Release()
	}
	if err != nil {
		return Analysis{}, fmt.Errorf("parse %s with gotreesitter: %w", source.Path, err)
	}
	if tree == nil || tree.RootNode() == nil {
		return Analysis{}, fmt.Errorf("parse %s with gotreesitter: empty syntax tree", source.Path)
	}

	tags := treeSitterTags(*entry, tree)
	definitions := treeSitterDefinitions(source, tree, tags)
	references := treeSitterReferences(tags)
	quality := QualityPartial
	members := treeSitterDataMembers(source, tree)
	if isTypeScriptFamily(language) {
		definitions = treeSitterTypeScriptDefinitions(source, tree)
		members = treeSitterTypeScriptMembers(source, tree)
		references = treeSitterTypeScriptReferences(tree)
		quality = QualitySyntax
	}
	return Analysis{
		Language:       language,
		Quality:        quality,
		Definitions:    flattenTreeSitterDefinitions(definitions),
		Calls:          treeSitterCalls(tree, tags, definitions),
		References:     references,
		outlineMembers: members,
	}, nil
}

func treeSitterTags(entry grammars.LangEntry, tree *gotreesitter.Tree) []gotreesitter.Tag {
	query := grammars.ResolveTagsQuery(entry)
	if strings.TrimSpace(query) == "" {
		return nil
	}
	tagger, err := gotreesitter.NewTagger(tree.Language(), query)
	if err != nil {
		return nil
	}
	return tagger.TagTree(tree)
}

func treeSitterDefinitions(source Source, tree *gotreesitter.Tree, tags []gotreesitter.Tag) []treeSitterDefinition {
	spans := gotreesitter.ExtractDefinitionSpans(tree)
	definitions := make([]treeSitterDefinition, 0, len(spans)+len(tags))
	seen := map[string]bool{}
	for _, span := range spans {
		kind, ok := treeSitterKind(span.Kind)
		if !ok {
			continue
		}
		key := fmt.Sprintf("%d:%d:%s", span.StartByte, span.EndByte, span.Name)
		seen[key] = true
		definitions = append(definitions, newTreeSitterDefinition(source, span.Name, kind, span.StartByte, span.EndByte))
	}
	for _, tag := range tags {
		kind, ok := treeSitterTagKind(tag.Kind)
		if !ok || tag.Name == "" {
			continue
		}
		key := fmt.Sprintf("%d:%d:%s", tag.Range.StartByte, tag.Range.EndByte, tag.Name)
		if seen[key] {
			continue
		}
		seen[key] = true
		definitions = append(definitions, newTreeSitterDefinition(source, tag.Name, kind, tag.Range.StartByte, tag.Range.EndByte))
	}
	assignTreeSitterOwners(source.Path, definitions)
	return definitions
}

func newTreeSitterDefinition(source Source, name string, kind Kind, startByte, endByte uint32) treeSitterDefinition {
	return treeSitterDefinition{
		Definition: Definition{
			SymbolID:  SymbolID(source.Path, "", name),
			Name:      name,
			Kind:      kind,
			Span:      byteSpan(source.Content, startByte, endByte),
			Signature: treeSitterSignature(source.Content, startByte, endByte),
		},
		startByte: startByte,
		endByte:   endByte,
	}
}

func assignTreeSitterOwners(path string, definitions []treeSitterDefinition) {
	localNames := make([]string, len(definitions))
	parents := make([]int, len(definitions))
	for i := range definitions {
		localNames[i] = definitions[i].Name
		parents[i] = -1
		if definitions[i].Callable() && definitions[i].Kind != KindMethod {
			continue
		}
		owner := -1
		for j := range definitions {
			if i == j || definitions[j].Callable() ||
				definitions[j].startByte > definitions[i].startByte ||
				definitions[i].endByte > definitions[j].endByte ||
				(definitions[j].startByte == definitions[i].startByte && definitions[j].endByte == definitions[i].endByte) {
				continue
			}
			if owner < 0 || definitions[j].endByte-definitions[j].startByte < definitions[owner].endByte-definitions[owner].startByte {
				owner = j
			}
		}
		parents[i] = owner
	}

	qualified := make([]bool, len(definitions))
	var qualify func(int)
	qualify = func(index int) {
		if qualified[index] {
			return
		}
		if parent := parents[index]; parent >= 0 {
			qualify(parent)
			definitions[index].Owner = definitions[parent].Name
			definitions[index].Name = definitions[parent].Name + "." + localNames[index]
			definitions[index].SymbolID = SymbolID(path, "", definitions[index].Name)
		}
		qualified[index] = true
	}
	for i := range definitions {
		qualify(i)
	}
}

func flattenTreeSitterDefinitions(definitions []treeSitterDefinition) []Definition {
	out := make([]Definition, 0, len(definitions))
	for _, definition := range definitions {
		out = append(out, definition.Definition)
	}
	return out
}

func treeSitterCalls(tree *gotreesitter.Tree, tags []gotreesitter.Tag, definitions []treeSitterDefinition) []Call {
	type callSite struct {
		name      string
		startByte uint32
	}
	var sites []callSite
	seen := map[string]bool{}
	for _, call := range gotreesitter.ExtractCalls(tree) {
		key := fmt.Sprintf("%d:%s", call.StartByte, call.Name)
		seen[key] = true
		sites = append(sites, callSite{name: call.Name, startByte: call.StartByte})
	}
	for _, tag := range tags {
		key := fmt.Sprintf("%d:%s", tag.Range.StartByte, tag.Name)
		if tag.Kind == "reference.call" && !seen[key] {
			seen[key] = true
			sites = append(sites, callSite{name: tag.Name, startByte: tag.Range.StartByte})
		}
	}
	var calls []Call
	for _, site := range sites {
		caller := enclosingTreeSitterDefinition(site.startByte, definitions)
		if caller == nil || site.name == "" {
			continue
		}
		calls = append(calls, Call{CallerID: caller.SymbolID, Name: site.name})
	}
	return calls
}

func enclosingTreeSitterDefinition(offset uint32, definitions []treeSitterDefinition) *Definition {
	best := -1
	for i := range definitions {
		if !definitions[i].Callable() || offset < definitions[i].startByte || offset > definitions[i].endByte {
			continue
		}
		if best < 0 || definitions[i].endByte-definitions[i].startByte < definitions[best].endByte-definitions[best].startByte {
			best = i
		}
	}
	if best < 0 {
		return nil
	}
	return &definitions[best].Definition
}

func treeSitterReferences(tags []gotreesitter.Tag) map[string]int {
	references := map[string]int{}
	for _, tag := range tags {
		if strings.HasPrefix(tag.Kind, "reference.") && tag.Name != "" {
			references[tag.Name]++
		}
	}
	return references
}

func treeSitterKind(kind string) (Kind, bool) {
	switch kind {
	case "function":
		return KindFunction, true
	case "method", "constructor":
		return KindMethod, true
	case "class", "enum", "record":
		return KindClass, true
	case "interface":
		return KindInterface, true
	case "type":
		return KindType, true
	default:
		return "", false
	}
}

func treeSitterTagKind(kind string) (Kind, bool) {
	return treeSitterKind(strings.TrimPrefix(kind, "definition."))
}

func byteSpan(content string, startByte, endByte uint32) Span {
	start := lineAtByte(content, startByte)
	end := lineAtByte(content, endByte)
	return Span{Start: start, End: end}
}

func lineAtByte(content string, offset uint32) int {
	if int(offset) > len(content) {
		offset = uint32(len(content))
	}
	return 1 + strings.Count(content[:offset], "\n")
}

func treeSitterSignature(content string, startByte, endByte uint32) string {
	if startByte >= endByte || int(startByte) >= len(content) {
		return ""
	}
	if int(endByte) > len(content) {
		endByte = uint32(len(content))
	}
	header := content[startByte:endByte]
	if i := strings.IndexAny(header, "{\n"); i >= 0 {
		header = header[:i]
	}
	signature := strings.Join(strings.Fields(header), " ")
	runes := []rune(signature)
	if len(runes) > maxTreeSitterSignatureRunes {
		signature = string(runes[:maxTreeSitterSignatureRunes-3]) + "..."
	}
	return signature
}
