package etl

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"strings"
)

// docxText 从 .docx 里提取正文。
//
// docx 就是一个 zip，正文在 word/document.xml，段落是 <w:p>、
// 文本片段是 <w:t>。这里只取文本，忽略样式、表格结构。
func docxText(data []byte) (string, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", fmt.Errorf("不是有效的 docx: %w", err)
	}

	var doc *zip.File
	for _, f := range zr.File {
		if f.Name == "word/document.xml" {
			doc = f
			break
		}
	}
	if doc == nil {
		return "", fmt.Errorf("docx 里找不到 word/document.xml")
	}

	rc, err := doc.Open()
	if err != nil {
		return "", err
	}
	defer rc.Close()

	dec := xml.NewDecoder(rc)
	var (
		out       strings.Builder
		inText    bool
		paraCount int
	)
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", fmt.Errorf("解析 document.xml 失败: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "t":
				inText = true
			case "p":
				paraCount++
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "t":
				inText = false
			case "p":
				out.WriteString("\n\n")
			}
		case xml.CharData:
			if inText {
				out.Write(t)
			}
		}
	}

	text := strings.TrimSpace(out.String())
	if text == "" {
		return "", fmt.Errorf("docx 没有提取到文本（段落 %d 个）", paraCount)
	}
	return text, nil
}
