package utils

import (
	"bufio"
	"bytes"
	"compress/zlib"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/s3rgeym/git-dump/internal/gitindex"
	"github.com/s3rgeym/git-dump/internal/logger"
)

var hashRegex = regexp.MustCompile(`\b(?:pack-)?[a-f0-9]{40}\b`)
var objectNameRegex = regexp.MustCompile(`/objects/[a-f0-9]{2}/[a-f0-9]{38}$`)
var refsRegex = regexp.MustCompile(`\brefs(?:/[a-z0-9_.-]+)+`)
var htmlContentRegex = regexp.MustCompile(`(?i)<html`)

func ExtractGitPaths(fileName string) ([]string, error) {
	file, err := os.Open(fileName)
	if err != nil {
		return nil, fmt.Errorf("failed to open file %s: %w", fileName, err)
	}
	defer file.Close()

	if strings.HasSuffix(fileName, "/index") {
		entries, err := gitindex.ParseGitIndex(file)
		if err != nil {
			os.Remove(fileName)
			return nil, fmt.Errorf("failed to parse Git index file %s: %w", fileName, err)
		}
		res := make([]string, 0, len(entries))
		for _, entry := range entries {
			logger.Debugf("Found entry in %s: %s => %s", fileName, entry.Sha1, entry.FileName)
			res = append(res, sha1ToPath(entry.Sha1))
		}
		return res, nil
	} else if objectNameRegex.MatchString(fileName) {
		data, err := decompressObjectFile(file)
		if err != nil {
			os.Remove(fileName)
			return nil, fmt.Errorf("failed to decompress object file %s: %w", fileName, err)
		}

		objectType, _, err := parseObjectType(data)
		if err != nil {
			return nil, fmt.Errorf("failed to parse object type for file %s: %w", fileName, err)
		}

		if objectType == "blob" {
			logger.Debugf("Skipping parsing blob file: %s", fileName)
			return nil, nil
		}

		return parseHashesAndRefs(data), nil
	}
	data, err := ReadFile(file)
	if err != nil {
		return nil, fmt.Errorf("failed to read file %s: %w", fileName, err)
	}

	if htmlContentRegex.Match(data) {
		os.Remove(fileName)
		return nil, fmt.Errorf("file %s contains HTML, removing", fileName)
	}
	return parseHashesAndRefs(data), nil
}

func sha1ToPath(hash string) string {
	return fmt.Sprintf("objects/%s/%s", hash[:2], hash[2:])
}

func ReadFile(file *os.File) ([]byte, error) {
	var buffer bytes.Buffer
	if _, err := io.Copy(&buffer, file); err != nil {
		return nil, fmt.Errorf("failed to read file: %w", err)
	}

	return buffer.Bytes(), nil
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

func parseObjectType(data []byte) (string, int, error) {
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

func parseHashesAndRefs(data []byte) []string {
	ret := make([]string, 0)
	matches := hashRegex.FindAllString(string(data), -1)
	for _, hash := range matches {
		if strings.HasPrefix(hash, "pack-") {
			for _, extension := range []string{"pack", "idx"} {
				ret = append(ret, fmt.Sprintf("objects/pack/%s.%s", hash, extension))
			}
		} else if hash != "0000000000000000000000000000000000000000" {
			ret = append(ret, sha1ToPath(hash))
		}
	}
	ret = append(ret, refsRegex.FindAllString(string(data), -1)...)
	return ret
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

func UrlJoin(baseURL, additionalPath string) (string, error) {
	base, err := url.Parse(baseURL)
	if err != nil {
		return "", fmt.Errorf("error parsing base URL: %w", err)
	}

	additional, err := url.Parse(additionalPath)
	if err != nil {
		return "", fmt.Errorf("error parsing additional path: %w", err)
	}

	joinedPath := base.Path
	if !strings.HasSuffix(base.Path, "/") && !strings.HasPrefix(additional.Path, "/") {
		joinedPath += "/"
	}
	joinedPath += strings.TrimPrefix(additional.Path, "/")

	base.Path = joinedPath
	newURL := base.String()

	return newURL, nil
}

func UrlToLocalPath(targetUrl string, outputDir string) (string, error) {
	u, err := url.Parse(targetUrl)
	if err != nil {
		return "", fmt.Errorf("failed to parse target URL %s: %w", targetUrl, err)
	}
	host := u.Hostname()
	return filepath.Join(outputDir, host, strings.TrimLeft(u.Path, "/")), nil
}
