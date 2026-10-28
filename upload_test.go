package main

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func pngBytes(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.RGBA{255, 0, 0, 255})
	var b bytes.Buffer
	png.Encode(&b, img)
	return b.Bytes()
}

func uploadRequest(t *testing.T, files map[string][]byte, origin string) *http.Request {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	for name, data := range files {
		fw, _ := mw.CreateFormFile("files", name)
		fw.Write(data)
	}
	mw.Close()
	req := httptest.NewRequest(http.MethodPost, "http://claude.test/api/upload", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	return req
}

func uploadServer(t *testing.T) *Server {
	return &Server{config: &Config{DataDir: t.TempDir()}}
}

func TestUploadSavesImage(t *testing.T) {
	s := uploadServer(t)
	rec := httptest.NewRecorder()
	// The client's filename and extension are ignored; the bytes decide.
	s.handleUpload(rec, uploadRequest(t, map[string][]byte{"photo.jpeg": pngBytes(t)}, "http://claude.test"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	var out struct{ Paths []string }
	json.Unmarshal(rec.Body.Bytes(), &out)
	if len(out.Paths) != 1 || filepath.Ext(out.Paths[0]) != ".png" {
		t.Fatalf("paths = %v, want one .png", out.Paths)
	}
	if filepath.Dir(out.Paths[0]) != uploadsDir(s.config) {
		t.Errorf("saved in %s, want %s", filepath.Dir(out.Paths[0]), uploadsDir(s.config))
	}
	got, _ := os.ReadFile(out.Paths[0])
	if !bytes.Equal(got, pngBytes(t)) {
		t.Error("saved bytes differ from the upload")
	}
}

func TestUploadRejectsNonImage(t *testing.T) {
	s := uploadServer(t)
	rec := httptest.NewRecorder()
	s.handleUpload(rec, uploadRequest(t, map[string][]byte{"evil.png": []byte("#!/bin/sh\nrm -rf ~\n")}, "http://claude.test"))
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "not a supported image") {
		t.Errorf("status = %d, body %s", rec.Code, rec.Body)
	}
}

func TestUploadRejectsForeignOrigin(t *testing.T) {
	s := uploadServer(t)
	for _, origin := range []string{"", "https://evil.example"} {
		rec := httptest.NewRecorder()
		s.handleUpload(rec, uploadRequest(t, map[string][]byte{"a.png": pngBytes(t)}, origin))
		if rec.Code != http.StatusForbidden {
			t.Errorf("origin %q: status = %d, want 403", origin, rec.Code)
		}
	}
}

func TestUploadConvertsHEIC(t *testing.T) {
	// Fake sips: "convert" by copying a PNG to the --out path.
	dir := t.TempDir()
	pngFile := filepath.Join(dir, "src.png")
	os.WriteFile(pngFile, pngBytes(t), 0o600)
	fake := filepath.Join(dir, "sips")
	os.WriteFile(fake, []byte("#!/bin/sh\nwhile [ $# -gt 0 ]; do [ \"$1\" = --out ] && cp "+pngFile+" \"$2\"; shift; done\n"), 0o755)
	old := sipsPath
	sipsPath = fake
	t.Cleanup(func() { sipsPath = old })

	heic := append([]byte{0, 0, 0, 0x18}, []byte("ftypheic\x00\x00\x00\x00mif1heic")...)
	s := uploadServer(t)
	rec := httptest.NewRecorder()
	s.handleUpload(rec, uploadRequest(t, map[string][]byte{"IMG_0001.HEIC": heic}, "http://claude.test"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	var out struct{ Paths []string }
	json.Unmarshal(rec.Body.Bytes(), &out)
	if len(out.Paths) != 1 || filepath.Ext(out.Paths[0]) != ".jpg" {
		t.Fatalf("paths = %v, want one .jpg", out.Paths)
	}
	if matches, _ := filepath.Glob(filepath.Join(uploadsDir(s.config), "*.heic")); len(matches) != 0 {
		t.Errorf("original HEIC left behind: %v", matches)
	}
}
