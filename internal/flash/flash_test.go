package flash

import (
	"os"
	"testing"
)

func TestParseHex(t *testing.T) {
	// two records at 0x0000 and, past a 64K boundary, 0x10000 (extended linear address), then EOF
	hex := ":0400000001020304F2\n:020000040001F9\n:02000000AABB99\n:00000001FF\n"
	img, err := ParseHex([]byte(hex))
	if err != nil {
		t.Fatal(err)
	}
	if len(img) != 0x10002 || img[0] != 1 || img[3] != 4 || img[4] != 0xFF || img[0x10000] != 0xAA || img[0x10001] != 0xBB {
		t.Fatalf("image: %d bytes", len(img))
	}
	for _, bad := range []string{":0400000001020304F3\n:00000001FF\n", ":04000000010203\n", ":0400000001020304F2\n", "garbage"} {
		if _, err := ParseHex([]byte(bad)); err == nil {
			t.Errorf("%q must fail", bad)
		}
	}
	if b, err := os.ReadFile("../../.pio/build/megaatmega2560/firmware.hex"); err == nil { // the real build, when there is one
		img, err := ParseHex(b)
		if err != nil || len(img) < 10000 {
			t.Fatalf("firmware.hex: %d bytes, %v", len(img), err)
		}
		t.Logf("firmware.hex: %s", Size(img))
	}
}
