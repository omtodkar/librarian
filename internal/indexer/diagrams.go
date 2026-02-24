package indexer

import (
	"fmt"
	"regexp"
	"strings"
)

type DiagramType string

const (
	DiagramMermaid  DiagramType = "mermaid"
	DiagramPlantUML DiagramType = "plantuml"
	DiagramASCII    DiagramType = "ascii"
)

type DiagramInfo struct {
	Type    DiagramType
	Summary string
	RawCode string
}

func detectDiagramType(lang, content string) (DiagramType, bool) {
	switch strings.ToLower(strings.TrimSpace(lang)) {
	case "mermaid":
		return DiagramMermaid, true
	case "plantuml", "puml":
		return DiagramPlantUML, true
	case "ascii", "ascii-art":
		return DiagramASCII, true
	case "", "text", "txt":
		if isASCIIDiagram(content) {
			return DiagramASCII, true
		}
		return "", false
	default:
		return "", false
	}
}

var boxDrawingPatterns = []string{
	"+--", "-+", "│", "├", "┤", "┌", "┐", "└", "┘", "─",
	"-->", "<--", "->", "<-",
}

func isASCIIDiagram(content string) bool {
	if len(content) < 20 {
		return false
	}
	lines := strings.Split(content, "\n")
	if len(lines) < 3 {
		return false
	}

	patternChars := 0
	for _, line := range lines {
		for _, p := range boxDrawingPatterns {
			patternChars += strings.Count(line, p) * len(p)
		}
		// Count pipe chars separately (single char pattern)
		patternChars += strings.Count(line, "|")
	}

	totalChars := len(content)
	if totalChars == 0 {
		return false
	}
	density := float64(patternChars) / float64(totalChars)
	return density > 0.30
}

var (
	mermaidNodeLabelRe = regexp.MustCompile(`[\[\(\{]+([^\]\)\}]+)[\]\)\}]+`)
	mermaidEdgeLabelRe = regexp.MustCompile(`\|([^\|]+)\|`)
	mermaidParticipantRe = regexp.MustCompile(`(?:participant|actor)\s+(?:"([^"]+)"|(\S+))`)
	mermaidTitleRe       = regexp.MustCompile(`(?i)title\s*:\s*(.+)`)
	mermaidSubgraphRe    = regexp.MustCompile(`subgraph\s+(\S+)`)
)

func extractMermaidLabels(content string) map[string]bool {
	labels := make(map[string]bool)

	for _, m := range mermaidNodeLabelRe.FindAllStringSubmatch(content, -1) {
		label := strings.TrimSpace(m[1])
		if len(label) > 2 || strings.Contains(label, " ") {
			labels[label] = true
		}
	}
	for _, m := range mermaidEdgeLabelRe.FindAllStringSubmatch(content, -1) {
		label := strings.TrimSpace(m[1])
		if label != "" {
			labels[label] = true
		}
	}
	for _, m := range mermaidParticipantRe.FindAllStringSubmatch(content, -1) {
		label := m[1]
		if label == "" {
			label = m[2]
		}
		label = strings.TrimSpace(label)
		if label != "" {
			labels[label] = true
		}
	}
	for _, m := range mermaidTitleRe.FindAllStringSubmatch(content, -1) {
		label := strings.TrimSpace(m[1])
		if label != "" {
			labels[label] = true
		}
	}
	for _, m := range mermaidSubgraphRe.FindAllStringSubmatch(content, -1) {
		label := strings.TrimSpace(m[1])
		if len(label) > 2 || strings.Contains(label, " ") {
			labels[label] = true
		}
	}

	return labels
}

var (
	plantumlTitleRe       = regexp.MustCompile(`(?i)title:?\s+(.+)`)
	plantumlParticipantRe = regexp.MustCompile(`(?:participant|actor|database|entity|boundary|control|collections)\s+(?:"([^"]+)"|(\S+))`)
	plantumlClassRe       = regexp.MustCompile(`(?:class|interface|enum|component|package)\s+(?:"([^"]+)"|(\S+))`)
	plantumlArrowLabelRe  = regexp.MustCompile(`(?:-->|->|<--|<-)\s*\S*\s*:\s*(.+)`)
)

func extractPlantUMLLabels(content string) map[string]bool {
	labels := make(map[string]bool)

	for _, m := range plantumlTitleRe.FindAllStringSubmatch(content, -1) {
		label := strings.TrimSpace(m[1])
		if label != "" {
			labels[label] = true
		}
	}
	for _, m := range plantumlParticipantRe.FindAllStringSubmatch(content, -1) {
		label := m[1]
		if label == "" {
			label = m[2]
		}
		label = strings.TrimSpace(label)
		if label != "" {
			labels[label] = true
		}
	}
	for _, m := range plantumlClassRe.FindAllStringSubmatch(content, -1) {
		label := m[1]
		if label == "" {
			label = m[2]
		}
		label = strings.TrimSpace(label)
		if label != "" {
			labels[label] = true
		}
	}
	for _, m := range plantumlArrowLabelRe.FindAllStringSubmatch(content, -1) {
		label := strings.TrimSpace(m[1])
		if label != "" {
			labels[label] = true
		}
	}

	return labels
}

var asciiBoxTextRe = regexp.MustCompile(`\|\s*([a-zA-Z][a-zA-Z0-9\s]{2,})\s*\|`)

func extractASCIILabels(content string) map[string]bool {
	labels := make(map[string]bool)

	for _, m := range asciiBoxTextRe.FindAllStringSubmatch(content, -1) {
		label := strings.TrimSpace(m[1])
		if len(label) >= 3 {
			labels[label] = true
		}
	}

	return labels
}

func diagramSubtype(dt DiagramType, content string) string {
	switch dt {
	case DiagramMermaid:
		lower := strings.ToLower(content)
		switch {
		case strings.HasPrefix(lower, "graph") || strings.HasPrefix(lower, "flowchart"):
			return "flowchart"
		case strings.HasPrefix(lower, "sequencediagram") || strings.Contains(lower, "participant"):
			return "sequence diagram"
		case strings.HasPrefix(lower, "classDiagram"):
			return "class diagram"
		case strings.HasPrefix(lower, "statediagram"):
			return "state diagram"
		case strings.HasPrefix(lower, "erdiagram"):
			return "ER diagram"
		case strings.HasPrefix(lower, "gantt"):
			return "gantt chart"
		case strings.HasPrefix(lower, "pie"):
			return "pie chart"
		default:
			return "diagram"
		}
	case DiagramPlantUML:
		return "diagram"
	case DiagramASCII:
		return "diagram"
	default:
		return "diagram"
	}
}

func formatLabels(diagramType DiagramType, subtype string, labels map[string]bool) string {
	if len(labels) == 0 {
		return fmt.Sprintf("[Diagram: %s %s]", diagramType, subtype)
	}

	collected := make([]string, 0, len(labels))
	for l := range labels {
		collected = append(collected, l)
	}

	// Cap at 10 labels
	if len(collected) > 10 {
		collected = collected[:10]
	}

	return fmt.Sprintf("[Diagram: %s %s — %s]", diagramType, subtype, strings.Join(collected, ", "))
}

// ProcessDiagramBlock checks if a fenced code block is a diagram, extracts labels,
// and returns a summary string suitable for embedding.
func ProcessDiagramBlock(lang, content string) (*DiagramInfo, string, bool) {
	dt, isDiagram := detectDiagramType(lang, content)
	if !isDiagram {
		return nil, "", false
	}

	var labels map[string]bool
	switch dt {
	case DiagramMermaid:
		labels = extractMermaidLabels(content)
	case DiagramPlantUML:
		labels = extractPlantUMLLabels(content)
	case DiagramASCII:
		labels = extractASCIILabels(content)
	}

	subtype := diagramSubtype(dt, content)
	summary := formatLabels(dt, subtype, labels)

	info := &DiagramInfo{
		Type:    dt,
		Summary: summary,
		RawCode: content,
	}

	return info, summary, true
}
