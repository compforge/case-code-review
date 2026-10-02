package language

import "strings"

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

func sourceSignature(content string, startByte, endByte uint32) string {
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
	if len(runes) > 240 {
		signature = string(runes[:240-3]) + "..."
	}
	return signature
}
