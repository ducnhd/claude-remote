package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	maxUploadBytes = 25 << 20 // per request; phone photos are a few MB
	maxUploadFiles = 10
)

// uploadsDir holds photos sent from the phone. It sits outside every project
// so pictures never end up committed, and Claude is started with --add-dir on
// it so it can read them without a permission prompt.
func uploadsDir(cfg *Config) string {
	return filepath.Join(cfg.DataDir, "uploads")
}

// sipsPath converts HEIC (the iPhone default), which Claude cannot read.
var sipsPath = "/usr/bin/sips"

var imageExts = map[string]string{
	"image/jpeg": ".jpg",
	"image/png":  ".png",
	"image/gif":  ".gif",
	"image/webp": ".webp",
}

// handleUpload stores images from the phone and returns their paths, which
// the chat page drops into the prompt for Claude to read.
func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
	// A multipart form is a "simple" request a foreign page could send, so
	// insist the browser says it came from this origin.
	if r.Method != http.MethodPost || !sameOrigin(r) {
		writeJSONError(w, http.StatusForbidden, "forbidden")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadBytes)
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		writeJSONError(w, http.StatusRequestEntityTooLarge, "upload too large or malformed")
		return
	}
	defer r.MultipartForm.RemoveAll()
	files := r.MultipartForm.File["files"]
	if len(files) == 0 || len(files) > maxUploadFiles {
		writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("send 1–%d images", maxUploadFiles))
		return
	}
	dir := uploadsDir(s.config)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "cannot create uploads folder")
		return
	}

	var paths []string
	for _, fh := range files {
		f, err := fh.Open()
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "cannot read upload")
			return
		}
		path, err := saveImage(dir, f, fh.Filename)
		f.Close()
		if err != nil {
			log.Printf("upload %q: %v", fh.Filename, err)
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		paths = append(paths, path)
	}
	log.Printf("uploaded %d image(s) from %s", len(paths), clientIP(r))
	writeJSON(w, http.StatusOK, map[string]any{"paths": paths})
}

// saveImage writes one upload under a random name. The type comes from the
// bytes, not the client's filename; HEIC is converted to JPEG.
func saveImage(dir string, src io.Reader, filename string) (string, error) {
	head := make([]byte, 512)
	n, _ := io.ReadFull(src, head)
	head = head[:n]
	body := io.MultiReader(strings.NewReader(string(head)), src)

	ext, ok := imageExts[http.DetectContentType(head)]
	heic := !ok && isHEIC(head)
	if !ok && !heic {
		return "", fmt.Errorf("%s is not a supported image (JPEG, PNG, GIF, WebP, HEIC)", filepath.Base(filename))
	}
	if heic {
		ext = ".heic"
	}

	var rnd [4]byte
	rand.Read(rnd[:])
	name := time.Now().Format("20060102-150405") + "-" + hex.EncodeToString(rnd[:])
	path := filepath.Join(dir, name+ext)
	out, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", fmt.Errorf("cannot save image")
	}
	if _, err := io.Copy(out, body); err != nil {
		out.Close()
		os.Remove(path)
		return "", fmt.Errorf("cannot save image")
	}
	out.Close()

	if heic {
		jpg := filepath.Join(dir, name+".jpg")
		if b, err := exec.Command(sipsPath, "-s", "format", "jpeg", path, "--out", jpg).CombinedOutput(); err != nil {
			log.Printf("sips: %v %s", err, b)
			return "", fmt.Errorf("could not convert HEIC to JPEG")
		}
		os.Remove(path)
		path = jpg
	}
	return path, nil
}

// isHEIC spots the ISO-BMFF "ftyp" box with a HEIF brand.
func isHEIC(head []byte) bool {
	if len(head) < 12 || string(head[4:8]) != "ftyp" {
		return false
	}
	switch string(head[8:12]) {
	case "heic", "heix", "hevc", "hevx", "heim", "heis", "mif1", "msf1":
		return true
	}
	return false
}
