package main

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/s3rgeym/git-dump/internal/config"
	"github.com/s3rgeym/git-dump/internal/gitindex"
	"github.com/s3rgeym/git-dump/internal/httpclient"
	"github.com/s3rgeym/git-dump/internal/logger"
	"github.com/s3rgeym/git-dump/internal/utils"
)

var (
	commonGitFiles = []string{
		".", // Проверка на directory listing
		"COMMIT_EDITMSG",
		"config",
		"description",
		"FETCH_HEAD",
		"HEAD",
		"index",
		"info/exclude",
		"info/refs",
		"logs/HEAD",
		"objects/info/packs",
		"ORIG_HEAD",
		"packed-refs",
		"refs/remotes/origin/HEAD",
	}

	nonDownloadableExtensions = []string{".php", ".php4", ".php5"}
)

func main() {
	config := config.ParseFlags()
	logger.SetupLogger(config.LogLevel)

	urlList, err := utils.ReadLines(config.InputFile)
	if err != nil {
		logger.Fatalf("Failed to read URLs from file: %v", err)
	}

	client := httpclient.NewHttpClient(config)

	var seen sync.Map
	sem := make(chan struct{}, config.WorkersNum)
	var wg sync.WaitGroup
	repos := make([]string, 0)
	downloadUrls := make([]string, 0)
	var mu sync.Mutex // Мьютекс для защиты доступа к downloadUrls

	logger.Info("Starting to download Git files...")

	for _, url := range urlList {
		baseUrl, err := utils.NormalizeUrl(url)
		if err != nil {
			logger.Errorf("Failed to normalize URL %s: %v", url, err)
			continue
		}
		repoPath, err := utils.UrlToLocalPath(baseUrl, config.OutputDir)
		if err != nil {
			logger.Errorf("Failed to convert URL %s to local repo path: %v", baseUrl, err)
			continue
		}
		repos = append(repos, repoPath)
		for _, file := range commonGitFiles {
			targetUrl, err := utils.UrlJoin(baseUrl, file)
			if err != nil {
				logger.Errorf("Failed to convert URL %s to target URL for file %s: %v", baseUrl, file, err)
				continue
			}

			sem <- struct{}{}
			wg.Add(1)
			go processGitUrl(client, targetUrl, baseUrl, &downloadUrls, &mu, &seen, sem, &wg, config)
		}
	}

	wg.Wait()

	logger.Info("Finished downloading Git files. Restoring repositories...")

	if err := restoreRepositories(repos); err != nil {
		logger.Errorf("Failed to restore repositories: %v", err)
	}

	logger.Info("Finished restoring repositories. Downloading found files...")

	downloadFiles(client, downloadUrls, sem, &wg, &config)

	logger.Info("🎉 Finished!")
}

func processGitUrl(client *httpclient.HttpClient, targetUrl, baseUrl string, downloadUrls *[]string, mu *sync.Mutex, seen *sync.Map, sem chan struct{}, wg *sync.WaitGroup, config config.Config) {
	defer func() {
		<-sem
		wg.Done()
	}()

	if _, ok := seen.LoadOrStore(targetUrl, true); ok {
		logger.Warnf("URL already seen: %s", targetUrl)
		return
	}

	fileName, err := utils.UrlToLocalPath(targetUrl, config.OutputDir)
	if err != nil {
		logger.Errorf("Failed to convert URL to save path: %v", err)
		return
	}

	needFetch := true
	if !config.ForceFetch && utils.FileExists(fileName) {
		logger.Debugf("File %s already exists, skipping fetch", fileName)
		needFetch = false
	}

	if needFetch {
		resp, cancel, err := client.Fetch(targetUrl)
		if err != nil {
			logger.Errorf("Failed to fetch URL %s: %v", targetUrl, err)
			return
		}
		defer cancel()
		defer resp.Body.Close()

		contentType := resp.Header.Get("Content-Type")
		mimeType, err := utils.GetMimeType(contentType)

		if err != nil {
			logger.Errorf("Invalid Content-Type for %s: %v", targetUrl, err)
			return
		}

		logger.Debugf("MIME Type for %s: %s", targetUrl, mimeType)

		if mimeType == "text/html" {
			handleHTMLContent(client, resp, targetUrl, baseUrl, downloadUrls, mu, seen, sem, wg, config)
			return
		}

		if err := client.SaveResponse(resp, fileName); err != nil {
			logger.Errorf("Failed to save response %s: %v", fileName, err)
			return
		} else {
			logger.Debugf("Saved %s", fileName)
		}
	}

	gitUrls, additionalUrls, err := extractUrls(fileName, baseUrl)
	if err != nil {
		logger.Errorf("Error extracting URLs from file %s: %v", fileName, err)
		os.Remove(fileName)
		return
	}

	processGitUrls(client, gitUrls, baseUrl, downloadUrls, mu, seen, sem, wg, config)

	mu.Lock()
	*downloadUrls = append(*downloadUrls, additionalUrls...)
	mu.Unlock()
}

func handleHTMLContent(client *httpclient.HttpClient, resp *http.Response, targetUrl, baseUrl string, downloadUrls *[]string, mu *sync.Mutex, seen *sync.Map, sem chan struct{}, wg *sync.WaitGroup, config config.Config) {
	buf := new(bytes.Buffer)
	_, err := io.Copy(buf, resp.Body)
	if err != nil {
		logger.Errorf("Failed to read response %s: %v", targetUrl, err)
		return
	}

	htmlContent := buf.String()
	///logger.Debugf("Content: %s", htmlContent)

	if strings.Contains(htmlContent, "Index of /") || strings.Contains(htmlContent, "Directory listing for /") {
		logger.Infof("Found directory listing: %s", targetUrl)
		links := utils.ExtractLinks(htmlContent)
		for _, link := range links {
			if strings.Contains(link, "?") {
				continue
			}
			newUrl, err := utils.UrlJoin(targetUrl, link)
			if err != nil {
				logger.Errorf("Failed to join URL %s with path %s: %v", baseUrl, link, err)
				continue
			}

			sem <- struct{}{}
			wg.Add(1)
			go processGitUrl(client, newUrl, baseUrl, downloadUrls, mu, seen, sem, wg, config)
		}
	} else {
		logger.Warnf("Skip URL: %s", targetUrl)
	}
}

func processGitUrls(client *httpclient.HttpClient, gitUrls []string, baseUrl string, downloadUrls *[]string, mu *sync.Mutex, seen *sync.Map, sem chan struct{}, wg *sync.WaitGroup, config config.Config) {
	for _, newUrl := range gitUrls {
		if _, ok := seen.Load(newUrl); ok {
			continue
		}

		sem <- struct{}{}
		wg.Add(1)
		go processGitUrl(client, newUrl, baseUrl, downloadUrls, mu, seen, sem, wg, config)
	}
}

func extractUrls(fileName, baseUrl string) ([]string, []string, error) {
	var gitPaths []string
	var additionalUrls []string

	if strings.HasSuffix(fileName, "/index") {
		gitIndex, err := gitindex.ParseGitIndex(fileName)
		if err != nil {
			return nil, nil, fmt.Errorf("error parsing git index %s: %w", fileName, err)
		}

		for _, entry := range gitIndex.Entries {
			gitPaths = append(gitPaths, utils.Sha1ToPath(entry.Sha1))
			if !isDownloadable(entry.FileName) {
				continue
			}
			downloadUrl, err := utils.UrlJoin(baseUrl, "../"+strings.TrimLeft(entry.FileName, "/"))
			if err != nil {
				logger.Errorf("Error joining URL: %v", err)
				continue
			}
			additionalUrls = append(additionalUrls, downloadUrl)
		}
	} else {
		var err error
		gitPaths, err = utils.GetHashesAndRefs(fileName)
		if err != nil {
			return nil, nil, fmt.Errorf("error getting object hashes and refs from file %s: %w", fileName, err)
		}
	}

	gitUrls := make([]string, 0, len(gitPaths))
	for _, path := range gitPaths {
		newUrl, err := utils.UrlJoin(baseUrl, path)
		if err != nil {
			logger.Errorf("Failed to join URL %s with path %s: %v", baseUrl, path, err)
			continue
		}
		gitUrls = append(gitUrls, newUrl)
	}

	return gitUrls, additionalUrls, nil
}

func restoreRepositories(repos []string) error {
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("failed to get current working directory: %v", err)
	}

	for _, repoPath := range repos {
		absRepoPath, err := filepath.Abs(repoPath)
		if err != nil {
			logger.Errorf("Error getting absolute path for %s: %v", repoPath, err)
			continue
		}

		parentDir := filepath.Dir(absRepoPath)

		if err := os.Chdir(parentDir); err != nil {
			logger.Errorf("Error changing directory to %s: %v", parentDir, err)
			continue
		}

		if err := restoreRepository(parentDir); err != nil {
			logger.Errorf("Error restoring repository in %s: %v", parentDir, err)
		}

		if err := os.Chdir(cwd); err != nil {
			logger.Errorf("Error changing directory to %s: %v", cwd, err)
			continue
		}
	}

	return nil
}

func restoreRepository(parentDir string) error {
	cmd := exec.Command("git", "checkout", ".")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("error restoring repository in %s: %v", parentDir, err)
	}
	logger.Infof("Restored repository in %s", parentDir)
	return nil
}

func downloadFiles(client *httpclient.HttpClient, downloadUrls []string, sem chan struct{}, wg *sync.WaitGroup, config *config.Config) {
	for _, url := range downloadUrls {
		fileName, err := utils.UrlToLocalPath(url, config.OutputDir)
		if err != nil {
			logger.Errorf("Failed to convert URL to save path: %v", err)
			continue
		}

		sem <- struct{}{}
		wg.Add(1)
		go func(url, fileName string) {
			defer func() {
				<-sem
				wg.Done()
			}()

			if _, err := client.FetchFile(url, fileName); err != nil {
				logger.Errorf("Failed to fetch file %s: %v", url, err)
			} else {
				logger.Infof("Downloaded file %s", fileName)
			}
		}(url, fileName)
	}

	wg.Wait()
}

func isDownloadable(fileName string) bool {
	for _, ext := range nonDownloadableExtensions {
		if strings.HasSuffix(fileName, ext) {
			return false
		}
	}

	return true
}
