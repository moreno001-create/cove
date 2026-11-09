package app

import (
	"archive/zip"
	"bytes"
	"fmt"
	"html"
	"net/http"
	"strings"
	"time"
)

// Templates for new documents (made here as small Office files) and notes (Markdown).

type template struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind"` // document, spreadsheet or note
	Desc string `json:"desc"`
}

var templates = []template{
	{"letter", "Letter", "document", "Your address, the date, a greeting and a closing"},
	{"meeting", "Meeting notes", "document", "Who came, the agenda, decisions and what's next"},
	{"budget", "Monthly budget", "spreadsheet", "Income and expenses, with totals that add up themselves"},
	{"note-meeting", "Meeting notes", "note", "Agenda, notes and a checklist of what's next"},
	{"note-packing", "Packing list", "note", "A checklist for a trip"},
	{"note-recipe", "Recipe", "note", "Ingredients and steps"},
	{"note-week", "Week plan", "note", "A checklist for each day"},
}

func (s *Server) listTemplates(w http.ResponseWriter, r *http.Request, u user) {
	writeJSON(w, http.StatusOK, templates)
}

func noteTemplate(id, title string) string {
	switch id {
	case "note-meeting":
		return "# " + title + "\n\n**When:** " + time.Now().Format("Monday, January 2") + "\n**Who:** \n\n## Agenda\n\n1. \n\n## Notes\n\n\n## Next\n\n- [ ] \n"
	case "note-packing":
		return "# " + title + "\n\n## Clothes\n\n- [ ] Shirts\n- [ ] Trousers\n- [ ] Socks\n- [ ] Jacket\n\n## Wash bag\n\n- [ ] Toothbrush\n- [ ] Toothpaste\n- [ ] Medicine\n\n## Other\n\n- [ ] Phone charger\n- [ ] Passport or ID\n- [ ] Keys\n"
	case "note-recipe":
		return "# " + title + "\n\nServes 4 · 30 minutes\n\n## Ingredients\n\n- \n\n## Steps\n\n1. \n\n## Notes\n\n"
	case "note-week":
		var b strings.Builder
		b.WriteString("# " + title + "\n")
		start := time.Now()
		for i := 0; i < 7; i++ {
			fmt.Fprintf(&b, "\n## %s\n\n- [ ] \n", start.AddDate(0, 0, i).Format("Monday, January 2"))
		}
		return b.String()
	}
	return "# " + title + "\n\n"
}

// --- small Office files

func zipOf(files map[string]string) []byte {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, name := range []string{"[Content_Types].xml", "_rels/.rels"} { // these first, as Office expects
		if body, ok := files[name]; ok {
			f, _ := zw.Create(name)
			f.Write([]byte(body))
		}
	}
	for name, body := range files {
		if name == "[Content_Types].xml" || name == "_rels/.rels" {
			continue
		}
		f, _ := zw.Create(name)
		f.Write([]byte(body))
	}
	zw.Close()
	return buf.Bytes()
}

const xmlHead = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>`

type para struct {
	text string
	size int // half-points (24 = 12 pt)
	bold bool
}

func docx(paras []para) []byte {
	var body strings.Builder
	for _, p := range paras {
		rpr := ""
		if p.bold || p.size > 0 {
			rpr = "<w:rPr>"
			if p.bold {
				rpr += "<w:b/>"
			}
			if p.size > 0 {
				rpr += fmt.Sprintf(`<w:sz w:val="%d"/>`, p.size)
			}
			rpr += "</w:rPr>"
		}
		fmt.Fprintf(&body, `<w:p><w:r>%s<w:t xml:space="preserve">%s</w:t></w:r></w:p>`, rpr, html.EscapeString(p.text))
	}
	return zipOf(map[string]string{
		"[Content_Types].xml": xmlHead + `<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="xml" ContentType="application/xml"/><Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/></Types>`,
		"_rels/.rels":         xmlHead + `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/document.xml"/></Relationships>`,
		"word/document.xml":   xmlHead + `<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body>` + body.String() + `<w:sectPr><w:pgSz w:w="12240" w:h="15840"/><w:pgMar w:top="1440" w:right="1440" w:bottom="1440" w:left="1440"/></w:sectPr></w:body></w:document>`,
	})
}

// xlsx makes a one-sheet workbook; a cell starting with "=" is a formula, a number a number, anything else text.
func xlsx(sheet string, rows [][]string) []byte {
	var data strings.Builder
	for i, row := range rows {
		fmt.Fprintf(&data, `<row r="%d">`, i+1)
		for j, v := range row {
			ref := fmt.Sprintf("%c%d", 'A'+j, i+1)
			switch {
			case v == "":
			case strings.HasPrefix(v, "="):
				fmt.Fprintf(&data, `<c r="%s"><f>%s</f></c>`, ref, html.EscapeString(v[1:]))
			case isNumber(v):
				fmt.Fprintf(&data, `<c r="%s"><v>%s</v></c>`, ref, v)
			default:
				fmt.Fprintf(&data, `<c r="%s" t="inlineStr"><is><t>%s</t></is></c>`, ref, html.EscapeString(v))
			}
		}
		data.WriteString("</row>")
	}
	return zipOf(map[string]string{
		"[Content_Types].xml":        xmlHead + `<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="xml" ContentType="application/xml"/><Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/><Override PartName="/xl/worksheets/sheet1.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/></Types>`,
		"_rels/.rels":                xmlHead + `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="xl/workbook.xml"/></Relationships>`,
		"xl/workbook.xml":            xmlHead + `<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="` + html.EscapeString(sheet) + `" sheetId="1" r:id="rId1"/></sheets></workbook>`,
		"xl/_rels/workbook.xml.rels": xmlHead + `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/></Relationships>`,
		"xl/worksheets/sheet1.xml":   xmlHead + `<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><cols><col min="1" max="1" width="28" customWidth="1"/><col min="2" max="3" width="14" customWidth="1"/></cols><sheetData>` + data.String() + `</sheetData></worksheet>`,
	})
}

func isNumber(v string) bool {
	if v == "" {
		return false
	}
	for i, r := range v {
		if !(r >= '0' && r <= '9' || r == '.' || (r == '-' && i == 0)) {
			return false
		}
	}
	return true
}

// templateFile is a template's contents and the extension it goes with.
func templateFile(id string, u user) ([]byte, string, bool) {
	name := displayName(u)
	today := time.Now().Format("January 2, 2006")
	switch id {
	case "letter":
		return docx([]para{{name, 0, true}, {"Your street", 0, false}, {"Your city", 0, false}, {"", 0, false}, {today, 0, false}, {"", 0, false},
			{"Dear …,", 0, false}, {"", 0, false}, {"Write your letter here.", 0, false}, {"", 0, false}, {"Kind regards,", 0, false}, {"", 0, false}, {name, 0, false}}), ".docx", true
	case "meeting":
		return docx([]para{{"Meeting notes", 36, true}, {today, 0, false}, {"", 0, false}, {"Who came", 28, true}, {"…", 0, false}, {"", 0, false},
			{"Agenda", 28, true}, {"1. …", 0, false}, {"", 0, false}, {"Decisions", 28, true}, {"…", 0, false}, {"", 0, false}, {"Next steps", 28, true}, {"☐ … (who, by when)", 0, false}}), ".docx", true
	case "budget":
		return xlsx("Budget", [][]string{
			{"Monthly budget", "", ""}, {"", "", ""},
			{"Income", "Planned", "Actual"}, {"Pay", "0", "0"}, {"Other", "0", "0"}, {"Total income", "=SUM(B4:B5)", "=SUM(C4:C5)"}, {"", "", ""},
			{"Expenses", "Planned", "Actual"}, {"Rent or mortgage", "0", "0"}, {"Groceries", "0", "0"}, {"Utilities", "0", "0"}, {"Transport", "0", "0"},
			{"Insurance", "0", "0"}, {"Fun", "0", "0"}, {"Savings", "0", "0"}, {"Total expenses", "=SUM(B9:B15)", "=SUM(C9:C15)"}, {"", "", ""},
			{"Left over", "=B6-B16", "=C6-C16"},
		}), ".xlsx", true
	}
	return nil, "", false
}
