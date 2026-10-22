package og

import (
	"bytes"
	"image/png"
	"os"
	"testing"
)

func TestPNG(t *testing.T) {
	b, err := PNG(Card{Title: "JungleTV", Mono: "ban_1jung1eb…uzpca", Big: "13,820.25 BAN", Facts: []string{"2,734,558 blocks", "Faucet", "Rep: node.jungletv.live · C"}})
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(bytes.NewReader(b))
	if err != nil || img.Bounds().Dx() != W || img.Bounds().Dy() != H {
		t.Fatal("bad png", err)
	}
	if p := os.Getenv("OG_OUT"); p != "" {
		os.WriteFile(p, b, 0o644)
	}
}
