package jira

import (
	"encoding/json"
	"fmt"
	"strings"
)

// formatIssueDescription turns Jira REST API v3 issue description into markdown.
// Descriptions are Atlassian Document Format (ADF) JSON; legacy plain strings are supported.
func formatIssueDescription(desc interface{}) string {
	if desc == nil {
		return ""
	}
	if s, ok := desc.(string); ok {
		return strings.TrimSpace(s)
	}
	raw, err := json.Marshal(desc)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(adfJSONToMarkdown(raw))
}

func adfJSONToMarkdown(raw []byte) string {
	var root map[string]interface{}
	if err := json.Unmarshal(raw, &root); err != nil {
		return ""
	}
	if typ, _ := root["type"].(string); typ == "doc" {
		return renderADFBlocks(root["content"])
	}
	return renderADFNode(root)
}

func renderADFBlocks(content interface{}) string {
	arr, ok := content.([]interface{})
	if !ok {
		return ""
	}
	var b strings.Builder
	for _, item := range arr {
		m, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		b.WriteString(renderADFNode(m))
	}
	return b.String()
}

func renderADFNode(n map[string]interface{}) string {
	typ, _ := n["type"].(string)
	switch typ {
	case "paragraph":
		return renderInline(n["content"]) + "\n\n"
	case "heading":
		level := 1
		if attrs, ok := n["attrs"].(map[string]interface{}); ok {
			if lv, ok := attrs["level"].(float64); ok {
				level = int(lv)
			}
		}
		if level < 1 {
			level = 1
		}
		if level > 6 {
			level = 6
		}
		return strings.Repeat("#", level) + " " + strings.TrimSpace(renderInline(n["content"])) + "\n\n"
	case "bulletList":
		return renderBulletList(n["content"])
	case "orderedList":
		return renderOrderedList(n["content"])
	case "blockquote":
		body := strings.TrimSpace(renderADFBlocks(n["content"]))
		if body == "" {
			return ""
		}
		var lines []string
		for _, line := range strings.Split(body, "\n") {
			if strings.TrimSpace(line) == "" {
				lines = append(lines, ">")
			} else {
				lines = append(lines, "> "+line)
			}
		}
		return strings.Join(lines, "\n") + "\n\n"
	case "codeBlock":
		lang := ""
		if attrs, ok := n["attrs"].(map[string]interface{}); ok {
			if l, ok := attrs["language"].(string); ok {
				lang = l
			}
		}
		code := strings.TrimSuffix(renderInline(n["content"]), "\n")
		return fmt.Sprintf("```%s\n%s\n```\n\n", lang, code)
	case "horizontalRule", "rule":
		return "---\n\n"
	case "panel", "expand", "extension":
		if c, ok := n["content"]; ok {
			return renderADFBlocks(c)
		}
		return ""
	case "mediaSingle", "mediaGroup", "embedCard":
		return "_[embedded media omitted]_\n\n"
	default:
		if c, ok := n["content"]; ok {
			return renderADFBlocks(c)
		}
		return ""
	}
}

func renderBulletList(content interface{}) string {
	arr, ok := content.([]interface{})
	if !ok {
		return ""
	}
	var b strings.Builder
	for _, item := range arr {
		m, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		if m["type"] != "listItem" {
			continue
		}
		inner := strings.TrimSpace(renderADFBlocks(m["content"]))
		if inner == "" {
			b.WriteString("- \n")
			continue
		}
		lines := strings.Split(inner, "\n")
		for i, line := range lines {
			if i == 0 {
				b.WriteString("- ")
			} else {
				b.WriteString("  ")
			}
			b.WriteString(line)
			b.WriteByte('\n')
		}
	}
	return b.String() + "\n"
}

func renderOrderedList(content interface{}) string {
	arr, ok := content.([]interface{})
	if !ok {
		return ""
	}
	var b strings.Builder
	idx := 0
	for _, item := range arr {
		m, ok := item.(map[string]interface{})
		if !ok || m["type"] != "listItem" {
			continue
		}
		idx++
		inner := strings.TrimSpace(renderADFBlocks(m["content"]))
		prefix := fmt.Sprintf("%d. ", idx)
		if inner == "" {
			b.WriteString(prefix + "\n")
			continue
		}
		lines := strings.Split(inner, "\n")
		indent := strings.Repeat(" ", len(prefix))
		for i, line := range lines {
			if i == 0 {
				b.WriteString(prefix)
			} else {
				b.WriteString(indent)
			}
			b.WriteString(line)
			b.WriteByte('\n')
		}
	}
	return b.String() + "\n"
}

func renderInline(content interface{}) string {
	arr, ok := content.([]interface{})
	if !ok {
		return ""
	}
	var b strings.Builder
	for _, item := range arr {
		m, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		b.WriteString(renderInlineNode(m))
	}
	return b.String()
}

func renderInlineNode(n map[string]interface{}) string {
	typ, _ := n["type"].(string)
	switch typ {
	case "text":
		t, _ := n["text"].(string)
		return applyTextMarks(t, n["marks"])
	case "hardBreak":
		return "\n"
	case "emoji":
		if attrs, ok := n["attrs"].(map[string]interface{}); ok {
			if s, ok := attrs["text"].(string); ok && s != "" {
				return s
			}
			if s, ok := attrs["shortName"].(string); ok {
				return ":" + s + ":"
			}
		}
		return ""
	case "mention":
		if attrs, ok := n["attrs"].(map[string]interface{}); ok {
			if text, ok := attrs["text"].(string); ok && text != "" {
				return "@" + text
			}
			if id, ok := attrs["id"].(string); ok {
				return "@mention:" + id
			}
		}
		return "@user"
	case "inlineCard":
		if attrs, ok := n["attrs"].(map[string]interface{}); ok {
			if u, ok := attrs["url"].(string); ok {
				return u
			}
		}
		return ""
	default:
		if c, ok := n["content"]; ok {
			return renderInline(c)
		}
		return ""
	}
}

func applyTextMarks(text string, marks interface{}) string {
	arr, ok := marks.([]interface{})
	if !ok || len(arr) == 0 {
		return text
	}
	for i := len(arr) - 1; i >= 0; i-- {
		m, ok := arr[i].(map[string]interface{})
		if !ok {
			continue
		}
		mt, _ := m["type"].(string)
		switch mt {
		case "strong":
			text = "**" + text + "**"
		case "em":
			text = "*" + text + "*"
		case "code":
			text = "`" + text + "`"
		case "strike", "deleted":
			text = "~~" + text + "~~"
		case "link":
			href := ""
			if attrs, ok := m["attrs"].(map[string]interface{}); ok {
				if h, ok := attrs["href"].(string); ok {
					href = h
				}
			}
			if href != "" {
				text = fmt.Sprintf("[%s](%s)", text, href)
			}
		}
	}
	return text
}
