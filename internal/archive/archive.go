package archive

import (
	"archive/zip"
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const (
	base64ArchiveHeader  = "GOLANGTOOLS_BASE64_V1"
	base64ArchiveVersion = 1
)

// Base64ArchiveMetadata describes the encoded payload stored in a text file.
type Base64ArchiveMetadata struct {
	Version int    `json:"version"`
	Kind    string `json:"kind"`
	Name    string `json:"name"`
	Mode    string `json:"mode"`
}

// Base64ArchiveService encodes files/directories into base64 text and restores them back.
type Base64ArchiveService struct{}

// NewBase64ArchiveService creates a new archive codec service.
func NewBase64ArchiveService() *Base64ArchiveService {
	return &Base64ArchiveService{}
}

// InspectEncodedFile reads only the metadata header of an encoded text file.
func (s *Base64ArchiveService) InspectEncodedFile(inputPath string) (Base64ArchiveMetadata, error) {
	file, err := os.Open(filepath.Clean(inputPath))
	if err != nil {
		return Base64ArchiveMetadata{}, err
	}
	defer file.Close()

	reader := bufio.NewReader(file)
	return readArchiveMetadata(reader)
}

// EncodePath writes a source file or directory to an encoded text file.
func (s *Base64ArchiveService) EncodePath(ctx context.Context, sourcePath, outputPath string) (Base64ArchiveMetadata, error) {
	sourcePath = filepath.Clean(sourcePath)
	info, err := os.Stat(sourcePath)
	if err != nil {
		return Base64ArchiveMetadata{}, err
	}

	outputPath = filepath.Clean(outputPath)
	if err := os.MkdirAll(filepath.Dir(outputPath), 0755); err != nil {
		return Base64ArchiveMetadata{}, err
	}

	outFile, err := os.Create(outputPath)
	if err != nil {
		return Base64ArchiveMetadata{}, err
	}
	defer outFile.Close()

	writer := bufio.NewWriterSize(outFile, 1<<20)
	meta := Base64ArchiveMetadata{
		Version: base64ArchiveVersion,
		Name:    filepath.Base(sourcePath),
	}
	if info.IsDir() {
		meta.Kind = "dir"
		meta.Mode = "zip"
	} else {
		meta.Kind = "file"
		meta.Mode = "raw"
	}

	if err := writeArchiveMetadata(writer, meta); err != nil {
		return Base64ArchiveMetadata{}, err
	}

	encoder := base64.NewEncoder(base64.StdEncoding, writer)
	if info.IsDir() {
		if err := s.encodeDirectory(ctx, sourcePath, encoder); err != nil {
			_ = encoder.Close()
			return Base64ArchiveMetadata{}, err
		}
	} else {
		if err := s.encodeFile(ctx, sourcePath, encoder); err != nil {
			_ = encoder.Close()
			return Base64ArchiveMetadata{}, err
		}
	}
	if err := encoder.Close(); err != nil {
		return Base64ArchiveMetadata{}, err
	}
	if err := writer.Flush(); err != nil {
		return Base64ArchiveMetadata{}, err
	}
	return meta, nil
}

// DecodeFile restores the encoded text file to the requested output path.
func (s *Base64ArchiveService) DecodeFile(ctx context.Context, inputPath, outputPath string) (Base64ArchiveMetadata, error) {
	inFile, err := os.Open(filepath.Clean(inputPath))
	if err != nil {
		return Base64ArchiveMetadata{}, err
	}
	defer inFile.Close()

	reader := bufio.NewReaderSize(inFile, 1<<20)
	meta, err := readArchiveMetadata(reader)
	if err != nil {
		return Base64ArchiveMetadata{}, err
	}

	decoder := base64.NewDecoder(base64.StdEncoding, reader)
	switch meta.Kind {
	case "file":
		if err := s.decodeToFile(ctx, decoder, outputPath); err != nil {
			return Base64ArchiveMetadata{}, err
		}
	case "dir":
		if err := s.decodeToDirectory(ctx, decoder, outputPath); err != nil {
			return Base64ArchiveMetadata{}, err
		}
	default:
		return Base64ArchiveMetadata{}, fmt.Errorf("unsupported payload kind: %s", meta.Kind)
	}
	return meta, nil
}

func writeArchiveMetadata(w io.Writer, meta Base64ArchiveMetadata) error {
	payload, err := json.Marshal(meta)
	if err != nil {
		return err
	}
	if _, err := io.WriteString(w, base64ArchiveHeader+"\n"); err != nil {
		return err
	}
	if _, err := io.WriteString(w, string(payload)+"\n"); err != nil {
		return err
	}
	return nil
}

func readArchiveMetadata(r *bufio.Reader) (Base64ArchiveMetadata, error) {
	header, err := r.ReadString('\n')
	if err != nil {
		return Base64ArchiveMetadata{}, err
	}
	if strings.TrimSpace(header) != base64ArchiveHeader {
		return Base64ArchiveMetadata{}, errors.New("invalid base64 archive header")
	}

	metaLine, err := r.ReadString('\n')
	if err != nil {
		return Base64ArchiveMetadata{}, err
	}

	var meta Base64ArchiveMetadata
	if err := json.Unmarshal([]byte(strings.TrimSpace(metaLine)), &meta); err != nil {
		return Base64ArchiveMetadata{}, err
	}
	if meta.Version != base64ArchiveVersion {
		return Base64ArchiveMetadata{}, fmt.Errorf("unsupported payload version: %d", meta.Version)
	}
	if strings.TrimSpace(meta.Name) == "" {
		return Base64ArchiveMetadata{}, errors.New("payload name is empty")
	}
	return meta, nil
}

func (s *Base64ArchiveService) encodeFile(ctx context.Context, sourcePath string, w io.Writer) error {
	file, err := os.Open(sourcePath)
	if err != nil {
		return err
	}
	defer file.Close()

	_, err = copyWithContext(ctx, w, file)
	return err
}

func (s *Base64ArchiveService) encodeDirectory(ctx context.Context, sourcePath string, w io.Writer) error {
	zipWriter := zip.NewWriter(w)
	err := filepath.Walk(sourcePath, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if path == sourcePath {
			return nil
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlink is not supported: %s", path)
		}

		relPath, err := filepath.Rel(sourcePath, path)
		if err != nil {
			return err
		}
		relPath = filepath.ToSlash(relPath)

		header, err := zip.FileInfoHeader(info)
		if err != nil {
			return err
		}
		header.Name = relPath
		if info.IsDir() {
			header.Name += "/"
		} else {
			header.Method = zip.Deflate
		}
		header.SetMode(info.Mode())

		entryWriter, err := zipWriter.CreateHeader(header)
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}

		file, err := os.Open(path)
		if err != nil {
			return err
		}
		_, err = copyWithContext(ctx, entryWriter, file)
		closeErr := file.Close()
		if err != nil {
			return err
		}
		return closeErr
	})
	if err != nil {
		_ = zipWriter.Close()
		return err
	}
	return zipWriter.Close()
}

func (s *Base64ArchiveService) decodeToFile(ctx context.Context, r io.Reader, outputPath string) error {
	outputPath = filepath.Clean(outputPath)
	if err := os.MkdirAll(filepath.Dir(outputPath), 0755); err != nil {
		return err
	}

	file, err := os.Create(outputPath)
	if err != nil {
		return err
	}
	defer file.Close()

	_, err = copyWithContext(ctx, file, r)
	return err
}

func (s *Base64ArchiveService) decodeToDirectory(ctx context.Context, r io.Reader, outputDir string) error {
	outputDir = filepath.Clean(outputDir)
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return err
	}

	tempFile, err := os.CreateTemp("", "golangtools-base64-*.zip")
	if err != nil {
		return err
	}
	tempName := tempFile.Name()
	defer os.Remove(tempName)

	if _, err := copyWithContext(ctx, tempFile, r); err != nil {
		tempFile.Close()
		return err
	}
	if err := tempFile.Close(); err != nil {
		return err
	}

	reader, err := zip.OpenReader(tempName)
	if err != nil {
		return err
	}
	defer reader.Close()

	for _, file := range reader.File {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := extractZipEntry(file, outputDir); err != nil {
			return err
		}
	}
	return nil
}

func extractZipEntry(file *zip.File, outputDir string) error {
	entryName := filepath.Clean(filepath.FromSlash(file.Name))
	if entryName == "." {
		return nil
	}

	targetPath := filepath.Join(outputDir, entryName)
	if err := ensurePathWithinRoot(outputDir, targetPath); err != nil {
		return err
	}

	if file.FileInfo().IsDir() {
		return os.MkdirAll(targetPath, file.Mode().Perm())
	}

	if err := os.MkdirAll(filepath.Dir(targetPath), 0755); err != nil {
		return err
	}

	in, err := file.Open()
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(targetPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, file.Mode().Perm())
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, in)
	return err
}

func ensurePathWithinRoot(root, target string) error {
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(target))
	if err != nil {
		return err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("invalid archive path: %s", target)
	}
	return nil
}

func copyWithContext(ctx context.Context, dst io.Writer, src io.Reader) (int64, error) {
	buf := make([]byte, 1<<20)
	var written int64
	for {
		if err := ctx.Err(); err != nil {
			return written, err
		}
		nr, er := src.Read(buf)
		if nr > 0 {
			nw, ew := dst.Write(buf[:nr])
			written += int64(nw)
			if ew != nil {
				return written, ew
			}
			if nw != nr {
				return written, io.ErrShortWrite
			}
		}
		if er != nil {
			if errors.Is(er, io.EOF) {
				return written, nil
			}
			return written, er
		}
	}
}
