package app

import (
	"bytes"
	"context"
	"fmt"
	"image"
	_ "image/jpeg"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path"
	"strings"
	"sync"
	"time"
)

// Scanning with a phone: the app straightens and cleans up each page (in the browser) and sends them as JPEGs;
// Cove binds them into a PDF in the person's files. The words on scanned pages, and in photos of papers, are read
// with Tesseract (OCR) when the image has it, so search finds receipts and letters.

// makePDF puts JPEG pages into a PDF, each on a page as wide as US Letter (612 points), as tall as it needs.
func makePDF(pages [][]byte) ([]byte, error) {
	var b bytes.Buffer
	var offsets []int
	obj := func(body string, stream []byte) {
		offsets = append(offsets, b.Len())
		fmt.Fprintf(&b, "%d 0 obj\n%s", len(offsets), body)
		if stream != nil {
			b.WriteString("\nstream\n")
			b.Write(stream)
			b.WriteString("\nendstream")
		}
		b.WriteString("\nendobj\n")
	}
	b.WriteString("%PDF-1.4\n%\xe2\xe3\xcf\xd3\n")
	n := len(pages)
	// 1: catalog, 2: pages, then for each page: page, image, content
	kids := make([]string, n)
	for i := range pages {
		kids[i] = fmt.Sprintf("%d 0 R", 3+i*3)
	}
	obj("<< /Type /Catalog /Pages 2 0 R >>", nil)
	obj(fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", strings.Join(kids, " "), n), nil)
	for i, jpg := range pages {
		cfg, format, err := image.DecodeConfig(bytes.NewReader(jpg))
		if err != nil || format != "jpeg" {
			return nil, fmt.Errorf("page %d isn't a JPEG picture", i+1)
		}
		w := 612.0
		h := w * float64(cfg.Height) / float64(max(1, cfg.Width))
		colors := "/DeviceRGB"
		if isGray(jpg) {
			colors = "/DeviceGray"
		}
		imgID, contentID := 4+i*3, 5+i*3
		obj(fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 %.2f %.2f] /Resources << /XObject << /Im%d %d 0 R >> >> /Contents %d 0 R >>",
			w, h, i, imgID, contentID), nil)
		obj(fmt.Sprintf("<< /Type /XObject /Subtype /Image /Width %d /Height %d /ColorSpace %s /BitsPerComponent 8 /Filter /DCTDecode /Length %d >>",
			cfg.Width, cfg.Height, colors, len(jpg)), jpg)
		content := fmt.Sprintf("q %.2f 0 0 %.2f 0 0 cm /Im%d Do Q", w, h, i)
		obj(fmt.Sprintf("<< /Length %d >>", len(content)), []byte(content))
	}
	xref := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", len(offsets)+1)
	for _, o := range offsets {
		fmt.Fprintf(&b, "%010d 00000 n \n", o)
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(offsets)+1, xref)
	return b.Bytes(), nil
}

// isGray says whether a JPEG has one color channel.
func isGray(jpg []byte) bool {
	for i := 2; i+9 < len(jpg); {
		if jpg[i] != 0xFF {
			return false
		}
		m := jpg[i+1]
		size := int(jpg[i+2])<<8 | int(jpg[i+3])
		if m >= 0xC0 && m <= 0xC3 {
			return jpg[i+9] == 1
		}
		i += 2 + size
	}
	return false
}

// saveScan takes the pages (multipart "page" parts, in order) and saves them as a PDF at ?path=.
func (s *Server) saveScan(w http.ResponseWriter, r *http.Request, u user, a access) {
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		jsonError(w, http.StatusBadRequest, "The pages didn't arrive: "+err.Error())
		return
	}
	defer r.MultipartForm.RemoveAll()
	var pages [][]byte
	for _, fh := range r.MultipartForm.File["page"] {
		f, err := fh.Open()
		if err != nil {
			continue
		}
		b, _ := io.ReadAll(io.LimitReader(f, 20<<20))
		f.Close()
		pages = append(pages, b)
	}
	if len(pages) == 0 || len(pages) > 200 {
		jsonError(w, http.StatusBadRequest, "Scan at least one page (at most 200).")
		return
	}
	pdf, err := makePDF(pages)
	if err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	p := a.path(r.URL.Query().Get("path"))
	if !strings.HasSuffix(strings.ToLower(p), ".pdf") {
		p += ".pdf"
	}
	final, err := s.writeFile(a.owner, p, bytes.NewReader(pdf), false, time.Time{})
	if err != nil {
		jsonError(w, http.StatusInsufficientStorage, err.Error())
		return
	}
	go func() { // read the words now, not in 20 minutes
		s.indexUser(a.owner, 0)
		s.embedPending(a.owner, 5*time.Minute)
	}()
	writeJSON(w, http.StatusCreated, map[string]any{"path": a.shown(final), "name": path.Base(final), "ocr": ocrAvailable()})
}

// --- OCR with Tesseract, and PDF text with Poppler, when they're installed

var (
	toolsOnce          sync.Once
	tesseract, popText string
	popImages          string
)

func findTools() {
	toolsOnce.Do(func() {
		tesseract, _ = exec.LookPath(envOr("COVE_TESSERACT", "tesseract"))
		popText, _ = exec.LookPath("pdftotext")
		popImages, _ = exec.LookPath("pdftoppm")
	})
}

func envOr(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

func ocrAvailable() bool {
	findTools()
	return tesseract != ""
}

var ocrSlots = make(chan struct{}, 2)

// ocrImage reads the words in a picture file (a path on disk).
func ocrImage(file string) string {
	if !ocrAvailable() {
		return ""
	}
	ocrSlots <- struct{}{}
	defer func() { <-ocrSlots }()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	out, err := exec.CommandContext(ctx, tesseract, file, "stdout", "-l", envOr("COVE_OCR_LANG", "eng"), "--psm", "3").Output()
	if err != nil {
		return ""
	}
	return string(out)
}

// pdfToolText is a PDF's text from pdftotext (better than Cove's own reader), "" without it.
func pdfToolText(file string) string {
	findTools()
	if popText == "" {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	out, _ := exec.CommandContext(ctx, popText, "-q", "-l", "300", file, "-").Output()
	return string(out)
}

// ocrPDF reads scanned pages (the first 30): each is drawn as a picture, then read.
func ocrPDF(file string) string {
	findTools()
	if popImages == "" || !ocrAvailable() {
		return ""
	}
	dir, err := os.MkdirTemp("", "cove-ocr-")
	if err != nil {
		return ""
	}
	defer os.RemoveAll(dir)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	if exec.CommandContext(ctx, popImages, "-q", "-r", "200", "-gray", "-png", "-l", "30", file, dir+"/p").Run() != nil {
		return ""
	}
	entries, _ := os.ReadDir(dir)
	var b strings.Builder
	for _, e := range entries {
		b.WriteString(ocrImage(dir + "/" + e.Name()))
		b.WriteByte('\n')
	}
	return b.String()
}

// osPath is where a file in the data folder is on disk, for the tools.
func (s *Server) osPath(name string) string {
	return s.cfg.Data + "/" + name
}

func meaningful(text string) bool {
	n := 0
	for _, r := range text {
		if r > ' ' {
			n++
		}
	}
	return n >= 15
}
