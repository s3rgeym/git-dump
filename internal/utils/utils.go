package utils

import (
	"bufio"
	"bytes"
	"compress/zlib"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/s3rgeym/git-dump/internal/logger"
)

var objectNameRegex = regexp.MustCompile(`/objects/[a-f0-9]{2}/[a-f0-9]{38}$`)
var hashRegex = regexp.MustCompile(`\b(?:pack-)?[a-f0-9]{40}\b`)
var refsRegex = regexp.MustCompile(`\brefs(?:/[a-z0-9_.-]+)+`)
var htmlContentRegex = regexp.MustCompile(`(?i)<html`)
var linkRegex = regexp.MustCompile(`<a href="([^"]+)`)

func GetHashesAndRefs(fileName string) ([]string, error) {
	data, err := os.ReadFile(fileName)
	if err != nil {
		return nil, fmt.Errorf("failed to read file %s: %w", fileName, err)
	}

	if objectNameRegex.MatchString(fileName) {
		data, err = decompressObjectFile(bytes.NewReader(data))
		if err != nil {
			return nil, fmt.Errorf("failed to decompress object file %s: %w", fileName, err)
		}

		objectType, _, err := parseObjectHeader(data)
		if err != nil {
			return nil, fmt.Errorf("failed to parse object type for file %s: %w", fileName, err)
		}

		if objectType == "blob" {
			logger.Debugf("Skipping parsing blob file: %s", fileName)
			return nil, nil
		}
	}

	if htmlContentRegex.Match(data) {
		return nil, fmt.Errorf("file %s contains HTML, removing", fileName)
	}

	return extractObjectsAndRefs(data), nil
}

func extractObjectsAndRefs(data []byte) []string {
	ret := make([]string, 0)
	matches := hashRegex.FindAllString(string(data), -1)
	for _, hash := range matches {
		if strings.HasPrefix(hash, "pack-") {
			for _, extension := range []string{"pack", "idx"} {
				ret = append(ret, fmt.Sprintf("objects/pack/%s.%s", hash, extension))
			}
		} else if hash != "0000000000000000000000000000000000000000" {
			ret = append(ret, Sha1ToPath(hash))
		}
	}
	ret = append(ret, refsRegex.FindAllString(string(data), -1)...)
	return ret
}

func Sha1ToPath(hash string) string {
	return fmt.Sprintf("objects/%s/%s", hash[:2], hash[2:])
}

func decompressObjectFile(reader io.Reader) ([]byte, error) {
	zlibReader, err := zlib.NewReader(reader)
	if err != nil {
		return nil, fmt.Errorf("failed to create zlib reader: %w", err)
	}
	defer zlibReader.Close()

	var buffer bytes.Buffer
	if _, err := io.Copy(&buffer, zlibReader); err != nil {
		return nil, fmt.Errorf("failed to read zlib-compressed object: %w", err)
	}
	return buffer.Bytes(), nil
}

func parseObjectHeader(data []byte) (string, int, error) {
	spaceIndex := bytes.IndexByte(data, ' ')
	if spaceIndex == -1 {
		return "", 0, fmt.Errorf("invalid object header")
	}
	objectType := string(data[:spaceIndex])
	sizeIndex := bytes.IndexByte(data[spaceIndex+1:], 0)
	if sizeIndex == -1 {
		return "", 0, fmt.Errorf("invalid object header")
	}
	size, err := strconv.Atoi(string(data[spaceIndex+1 : spaceIndex+1+sizeIndex]))
	if err != nil {
		return "", 0, fmt.Errorf("invalid object size: %w", err)
	}
	return objectType, size, nil
}

func ReadLines(filePath string) ([]string, error) {
	file, err := openFile(filePath)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var lines []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	return lines, scanner.Err()
}

func openFile(filePath string) (*os.File, error) {
	if filePath == "-" {
		return os.Stdin, nil
	}
	return os.Open(filePath)
}

func NormalizeUrl(u string) (string, error) {
	if !strings.Contains(u, "://") {
		u = "http://" + u
	}

	if !strings.HasSuffix(u, "/.git/") {
		var err error
		u, err = UrlJoin(u, "/.git/")
		if err != nil {
			return "", fmt.Errorf("failed to normalize URL %s: %w", u, err)
		}
	}

	return u, nil
}

// func UrlJoin(baseURL, additionalPath string) (string, error) {
// 	base, err := url.Parse(baseURL)
// 	if err != nil {
// 		return "", fmt.Errorf("error parsing base URL: %w", err)
// 	}

// 	additional, err := url.Parse(additionalPath)
// 	if err != nil {
// 		return "", fmt.Errorf("error parsing additional path: %w", err)
// 	}

// 	joinedPath := base.Path
// 	if !strings.HasSuffix(base.Path, "/") && !strings.HasPrefix(additional.Path, "/") {
// 		joinedPath += "/"
// 	}
// 	joinedPath += strings.TrimPrefix(additional.Path, "/")

// 	base.Path = joinedPath
// 	newURL := base.String()

// 	return newURL, nil
// }

func UrlJoin(baseURL string, paths ...string) (string, error) {
	base, err := url.Parse(baseURL)
	if err != nil {
		return "", fmt.Errorf("error parsing base URL: %w", err)
	}

	for _, path := range paths {
		additional, err := url.Parse(path)
		if err != nil {
			return "", fmt.Errorf("error parsing additional path: %w", err)
		}

		// Нормализуем относительный путь относительно базового URL
		base = base.ResolveReference(additional)
	}

	return base.String(), nil
}

func UrlToLocalPath(targetUrl string, outputDir string) (string, error) {
	u, err := url.Parse(targetUrl)
	if err != nil {
		return "", fmt.Errorf("failed to parse target URL %s: %w", targetUrl, err)
	}
	host := u.Hostname()
	return filepath.Join(outputDir, host, strings.TrimLeft(u.Path, "/")), nil
}

func ExtractLinks(htmlContent string) []string {
	matches := linkRegex.FindAllStringSubmatch(htmlContent, -1)
	var links []string
	for _, match := range matches {
		if len(match) > 1 {
			links = append(links, match[1])
		}
	}
	return links
}

func GetMimeType(ct string) (string, error) {
	mime := strings.Split(ct, ";")[0]
	parts := strings.Split(mime, "/")
	if len(parts) != 2 {
		return "", errors.New("invalid mime type")
	}
	return strings.ToLower(parts[0]) + "/" + strings.ToLower(parts[1]), nil
}

func FileExists(fileName string) bool {
	_, err := os.Stat(fileName)
	return err == nil
}
