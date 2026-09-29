package clientconf

import (
	"io"
	"strings"

	"rsc.io/qr"
)

// quietZone is the light border QR codes need around them, in modules.
const quietZone = 4

// ANSI colors: bright white and black, as foreground and background.
const (
	fgLight, fgDark = "\x1b[97m", "\x1b[30m"
	bgLight, bgDark = "\x1b[107m", "\x1b[40m"
	reset           = "\x1b[0m"
)

// WriteQR writes text as a QR code for a terminal. Each character cell shows two rows of
// modules with an upper half block, and the colors are set explicitly, so the code
// scans the same on light and dark terminal themes.
func WriteQR(w io.Writer, text string) error {
	code, err := qr.Encode(text, qr.M)
	if err != nil {
		return err
	}
	size := code.Size + 2*quietZone
	dark := func(x, y int) bool {
		return code.Black(x-quietZone, y-quietZone)
	}

	var b strings.Builder
	for y := 0; y < size; y += 2 {
		for x := 0; x < size; x++ {
			if dark(x, y) {
				b.WriteString(fgDark)
			} else {
				b.WriteString(fgLight)
			}
			if y+1 < size && dark(x, y+1) {
				b.WriteString(bgDark)
			} else {
				b.WriteString(bgLight)
			}
			b.WriteString("▀")
		}
		b.WriteString(reset + "\n")
	}
	_, err = io.WriteString(w, b.String())
	return err
}
