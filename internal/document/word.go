package document

import (
	"archive/zip"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
)

// WordReader streams plain text from a .docx file.
type WordReader struct{}

// NewWordReader creates a WordReader.
func NewWordReader() *WordReader {
	return &WordReader{}
}

// ReadToWriter parses the main document body in a .docx file and streams text to w.
func (r *WordReader) ReadToWriter(ctx context.Context, path string, w io.Writer) error {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".docx", ".docm":
	default:
		if ext == ".doc" {
			return errors.New(".doc is not supported; please convert it to .docx first")
		}
		return fmt.Errorf("unsupported word file: %s", path)
	}

	archive, err := zip.OpenReader(filepath.Clean(path))
	if err != nil {
		return err
	}
	defer archive.Close()

	documentFile := findDocxEntry(archive.File, "word/document.xml")
	if documentFile == nil {
		return errors.New("word/document.xml not found in docx archive")
	}

	rc, err := documentFile.Open()
	if err != nil {
		return err
	}
	defer rc.Close()

	return streamDocumentXML(ctx, rc, w)
}

func findDocxEntry(files []*zip.File, name string) *zip.File {
	for _, file := range files {
		if file.Name == name {
			return file
		}
	}
	return nil
}

func streamDocumentXML(ctx context.Context, src io.Reader, w io.Writer) error {
	decoder := xml.NewDecoder(src)
	var textDepth int

	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		token, err := decoder.Token()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}

		switch elem := token.(type) {
		case xml.StartElement:
			switch elem.Name.Local {
			case "t":
				textDepth++
			case "tab":
				if _, err := io.WriteString(w, "\t"); err != nil {
					return err
				}
			case "br", "cr":
				if _, err := io.WriteString(w, "\n"); err != nil {
					return err
				}
			}
		case xml.EndElement:
			switch elem.Name.Local {
			case "t":
				if textDepth > 0 {
					textDepth--
				}
			case "p":
				if _, err := io.WriteString(w, "\n"); err != nil {
					return err
				}
			case "tc":
				if _, err := io.WriteString(w, "\t"); err != nil {
					return err
				}
			case "tr":
				if _, err := io.WriteString(w, "\n"); err != nil {
					return err
				}
			}
		case xml.CharData:
			if textDepth == 0 {
				continue
			}
			if _, err := w.Write([]byte(elem)); err != nil {
				return err
			}
		}
	}
}
