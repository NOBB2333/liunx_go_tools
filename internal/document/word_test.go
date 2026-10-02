package document

import (
	"archive/zip"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestWordReaderReadToWriter(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	docxPath := filepath.Join(tempDir, "demo.docx")
	documentXML := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">
  <w:body>
    <w:p><w:r><w:t>Hello</w:t></w:r><w:r><w:t xml:space="preserve"> World</w:t></w:r></w:p>
    <w:p><w:r><w:t>Second</w:t></w:r><w:r><w:tab/></w:r><w:r><w:t>Line</w:t></w:r></w:p>
    <w:tbl>
      <w:tr>
        <w:tc><w:p><w:r><w:t>A1</w:t></w:r></w:p></w:tc>
        <w:tc><w:p><w:r><w:t>B1</w:t></w:r><w:r><w:br/></w:r><w:r><w:t>B2</w:t></w:r></w:p></w:tc>
      </w:tr>
    </w:tbl>
  </w:body>
</w:document>`

	if err := writeMinimalDocx(docxPath, documentXML); err != nil {
		t.Fatalf("write docx: %v", err)
	}

	var out bytes.Buffer
	reader := NewWordReader()
	if err := reader.ReadToWriter(context.Background(), docxPath, &out); err != nil {
		t.Fatalf("read word: %v", err)
	}

	want := "Hello World\nSecond\tLine\nA1\n\tB1\nB2\n\t\n"
	if out.String() != want {
		t.Fatalf("unexpected output:\n got: %q\nwant: %q", out.String(), want)
	}
}

func TestWordReaderRejectLegacyDoc(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	docPath := filepath.Join(tempDir, "demo.doc")
	if err := os.WriteFile(docPath, []byte("legacy"), 0644); err != nil {
		t.Fatalf("write doc: %v", err)
	}

	reader := NewWordReader()
	err := reader.ReadToWriter(context.Background(), docPath, &bytes.Buffer{})
	if err == nil {
		t.Fatal("expected error for .doc")
	}
}

func writeMinimalDocx(path, documentXML string) error {
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()

	writer := zip.NewWriter(file)
	if err := writeZipEntry(writer, "[Content_Types].xml", `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">
  <Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>
  <Default Extension="xml" ContentType="application/xml"/>
  <Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/>
</Types>`); err != nil {
		return err
	}
	if err := writeZipEntry(writer, "_rels/.rels", `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
  <Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/document.xml"/>
</Relationships>`); err != nil {
		return err
	}
	if err := writeZipEntry(writer, "word/document.xml", documentXML); err != nil {
		return err
	}
	return writer.Close()
}

func writeZipEntry(writer *zip.Writer, name, content string) error {
	entry, err := writer.Create(name)
	if err != nil {
		return err
	}
	_, err = entry.Write([]byte(content))
	return err
}
