package litmus

import (
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

func TestBugCropKeepsContextAndScalesCSSCoordinates(t *testing.T) {
	bounds := image.Rect(0, 0, 1320, 780)
	box := cropBox{x: 1220, y: 690, w: 50, h: 30, vw: 1320, vh: 780}
	got := cropRect(bounds, box)
	if got.Dx() != 600 || got.Dy() != 400 || !got.In(bounds) || !image.Pt(1245, 705).In(got) {
		t.Fatalf("crop %v does not keep the target and 600x400 context", got)
	}
	box.x, box.y, box.w, box.h, box.vw, box.vh = 610, 345, 25, 15, 660, 390
	if scaled := cropRect(bounds, box); scaled != got {
		t.Fatalf("CSS coordinates yielded %v, want %v", scaled, got)
	}
}

func TestBugComparisonKeepsBaseLeftAndDoesNotInventMissingEvidence(t *testing.T) {
	dir := t.TempDir()
	base, head, out := filepath.Join(dir, "base.png"), filepath.Join(dir, "head.png"), filepath.Join(dir, "bug.png")
	writeImage := func(path string, c color.RGBA) {
		t.Helper()
		img := image.NewRGBA(image.Rect(0, 0, 800, 600))
		for y := 0; y < 600; y++ {
			for x := 0; x < 800; x++ {
				img.SetRGBA(x, y, c)
			}
		}
		f, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		err = png.Encode(f, img)
		if closeErr := f.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	red, blue := color.RGBA{R: 255, A: 255}, color.RGBA{B: 255, A: 255}
	writeImage(base, red)
	writeImage(head, blue)
	b := bugComment{Check: Check{Receipts: []Receipt{{AfterPath: head}}}, Base: &Check{Receipts: []Receipt{{Leg: "base", AfterPath: base}}}}
	if err := makeBugImage(b, out); err != nil {
		t.Fatal(err)
	}
	if color.RGBAModel.Convert(pixelAt(t, out, 300, 200)) != red || color.RGBAModel.Convert(pixelAt(t, out, 900, 200)) != blue {
		t.Fatal("comparison did not put base left and head right")
	}
	b.Base = nil
	missing := filepath.Join(dir, "missing.png")
	if err := makeBugImage(b, missing); err == nil {
		t.Fatal("missing base shot accepted")
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatalf("missing base created a comparison: %v", err)
	}
}
