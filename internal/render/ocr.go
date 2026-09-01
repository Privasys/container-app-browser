// Copyright (c) Privasys. All rights reserved.
// Licensed under the GNU Affero General Public License v3.0. See LICENSE.

package render

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// Text recognition over a screenshot.
//
// It is here rather than in the caller for one reason: determinism. The
// recogniser is a binary at a fixed version inside a measured image, so
// the same screenshot yields the same text, and a caller that records
// both can have the result re-derived later by anyone holding the
// image. A recogniser pulled at runtime, or a service that improves
// over time, would make yesterday's reading unreproducible.
//
// It earns its place only for words a page draws rather than writes:
// canvas, charts, an error rendered into an image. For everything else
// the document's own text is better, free, and exact.

// Tesseract returns an OCR function backed by the binary in the image,
// or nil when it is not present.
func Tesseract(binary string) func([]byte) (string, error) {
	if binary == "" {
		binary = "tesseract"
	}
	path, err := exec.LookPath(binary)
	if err != nil {
		return nil
	}
	return func(png []byte) (string, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()

		// Read the image on standard input and write plain text to
		// standard output, so nothing touches the filesystem.
		cmd := exec.CommandContext(ctx, path, "stdin", "stdout", "--psm", "6")
		cmd.Stdin = bytes.NewReader(png)
		var out, errBuf bytes.Buffer
		cmd.Stdout = &out
		cmd.Stderr = &errBuf
		if err := cmd.Run(); err != nil {
			return "", fmt.Errorf("%w: %s", err, strings.TrimSpace(errBuf.String()))
		}
		text := strings.TrimSpace(out.String())
		if len(text) > MaxText {
			text = text[:MaxText]
		}
		return text, nil
	}
}

// TesseractVersion reports the recogniser's version, which belongs in
// the record beside anything it produced: the text is only reproducible
// against the version that produced it.
func TesseractVersion(binary string) string {
	if binary == "" {
		binary = "tesseract"
	}
	path, err := exec.LookPath(binary)
	if err != nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, path, "--version").CombinedOutput()
	if err != nil {
		return ""
	}
	line := strings.SplitN(strings.TrimSpace(string(out)), "\n", 2)[0]
	return strings.TrimSpace(line)
}
