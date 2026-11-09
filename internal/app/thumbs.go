package app

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	_ "image/gif"
	"image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"strconv"

	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
)

// Thumbnails for the grid view and the photo viewer: JPEG, PNG, GIF and WebP pictures, shrunk to ?w= pixels wide (at
// most 1600) and kept in thumbs/ until the picture changes.

var thumbSlots = make(chan struct{}, 3) // decoding big photos takes memory: a few at a time

func (s *Server) fileThumb(w http.ResponseWriter, r *http.Request, u user, a access) {
	s.serveThumb(w, r, s.rel(a.owner, a.path(r.URL.Query().Get("path"))))
}

func (s *Server) shareThumb(w http.ResponseWriter, r *http.Request) {
	sh, ok := s.openShare(w, r, true)
	if !ok || sh.Mode == "drop" {
		if ok {
			http.Error(w, "This link only takes files.", http.StatusForbidden)
		}
		return
	}
	s.serveThumb(w, r, s.within(sh, r.URL.Query().Get("path")))
}

func (s *Server) serveThumb(w http.ResponseWriter, r *http.Request, name string) {
	width, _ := strconv.Atoi(r.URL.Query().Get("w"))
	switch {
	case width <= 0:
		width = 320
	case width > 1600:
		width = 1600
	}
	info, err := s.data.Stat(name)
	if err != nil || info.IsDir() {
		http.NotFound(w, r)
		return
	}
	key := hashHex(fmt.Sprintf("%s\x00%d\x00%d\x00%d", name, info.Size(), info.ModTime().UnixNano(), width))[:32]
	cached := "thumbs/" + key[:2] + "/" + key + ".jpg"
	etag := `"` + key + `"`
	w.Header().Set("Cache-Control", "private, max-age=86400")
	w.Header().Set("ETag", etag)
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	if b, err := s.data.ReadFile(cached); err == nil {
		w.Header().Set("Content-Type", "image/jpeg")
		w.Write(b)
		return
	}
	thumbSlots <- struct{}{}
	b, err := s.makeThumb(name, width)
	<-thumbSlots
	if err != nil {
		http.Error(w, "no thumbnail for this file", http.StatusUnsupportedMediaType)
		return
	}
	if s.data.MkdirAll("thumbs/"+key[:2], 0o750) == nil {
		s.data.WriteFile(cached, b, 0o640)
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Write(b)
}

func (s *Server) makeThumb(name string, width int) ([]byte, error) {
	f, err := s.data.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	cfg, _, err := image.DecodeConfig(f)
	if err != nil {
		return nil, err
	}
	if cfg.Width*cfg.Height > 120_000_000 {
		return nil, fmt.Errorf("too big to shrink (%d×%d)", cfg.Width, cfg.Height)
	}
	f.Seek(0, io.SeekStart)
	orientation := jpegOrientation(f)
	f.Seek(0, io.SeekStart)
	src, _, err := image.Decode(f)
	if err != nil {
		return nil, err
	}
	b := src.Bounds()
	if orientation >= 5 { // turned a quarter: the width to fit is the picture's height
		b = image.Rect(0, 0, b.Dy(), b.Dx())
	}
	if b.Dx() < width {
		width = b.Dx()
	}
	height := max(1, b.Dy()*width/max(1, b.Dx()))
	srcW, srcH := width, height
	if orientation >= 5 {
		srcW, srcH = height, width
	}
	small := image.NewRGBA(image.Rect(0, 0, srcW, srcH))
	draw.CatmullRom.Scale(small, small.Bounds(), src, src.Bounds(), draw.Over, nil)
	out := orient(small, orientation)
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, out, &jpeg.Options{Quality: 82}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// jpegOrientation reads the EXIF orientation (1–8) phones write instead of turning the picture; 1 if there's none.
func jpegOrientation(r io.Reader) int {
	head := make([]byte, 128<<10)
	n, _ := io.ReadFull(r, head)
	head = head[:n]
	if len(head) < 4 || head[0] != 0xFF || head[1] != 0xD8 {
		return 1
	}
	for i := 2; i+4 <= len(head); {
		if head[i] != 0xFF {
			return 1
		}
		marker, size := head[i+1], int(binary.BigEndian.Uint16(head[i+2:]))
		if marker == 0xE1 && i+4+size <= len(head) && size > 8 && string(head[i+4:i+10]) == "Exif\x00\x00" {
			return exifOrientation(head[i+10 : i+2+size])
		}
		if marker == 0xDA { // the picture itself starts: no EXIF before it
			return 1
		}
		i += 2 + size
	}
	return 1
}

func exifOrientation(t []byte) int {
	if len(t) < 8 {
		return 1
	}
	var bo binary.ByteOrder
	switch string(t[:2]) {
	case "II":
		bo = binary.LittleEndian
	case "MM":
		bo = binary.BigEndian
	default:
		return 1
	}
	ifd := int(bo.Uint32(t[4:]))
	if ifd+2 > len(t) {
		return 1
	}
	count := int(bo.Uint16(t[ifd:]))
	for k := 0; k < count; k++ {
		e := ifd + 2 + k*12
		if e+12 > len(t) {
			return 1
		}
		if bo.Uint16(t[e:]) == 0x0112 {
			if v := int(bo.Uint16(t[e+8:])); v >= 1 && v <= 8 {
				return v
			}
		}
	}
	return 1
}

// orient turns and flips a picture as its EXIF orientation says.
func orient(src *image.RGBA, o int) image.Image {
	if o <= 1 || o > 8 {
		return src
	}
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	dw, dh := w, h
	if o >= 5 {
		dw, dh = h, w
	}
	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var dx, dy int
			switch o {
			case 2:
				dx, dy = w-1-x, y
			case 3:
				dx, dy = w-1-x, h-1-y
			case 4:
				dx, dy = x, h-1-y
			case 5:
				dx, dy = y, x
			case 6:
				dx, dy = h-1-y, x
			case 7:
				dx, dy = h-1-y, w-1-x
			case 8:
				dx, dy = y, w-1-x
			}
			dst.Set(dx, dy, src.At(x, y))
		}
	}
	return dst
}
