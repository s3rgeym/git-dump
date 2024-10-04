package main

import (
	"fmt"
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

var commonGitFiles = []string{
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

	if err := restoreRepositories(repos); err != nil {
		logger.Errorf("Failed to restore repositories: %v", err)
	}

	// Fetch direct file URLs
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

	var needFetch bool = true
	if !config.ForceFetch {
		if _, err := os.Stat(fileName); err == nil {
			logger.Warnf("File %s already exists, skipping fetch", fileName)
			needFetch = false
		}
	}

	if needFetch {
		if err := client.FetchFile(targetUrl, fileName); err != nil {
			logger.Errorf("Failed to fetch file %s: %v", targetUrl, err)
			return
		} else {
			logger.Infof("Fetched file %s", targetUrl)
		}
	}

	var paths []string

	if strings.HasSuffix(fileName, "/index") {
		gitIndex, err := gitindex.ParseGitIndex(fileName)
		if err != nil {
			logger.Errorf("Error parsing git index %s: %v", fileName, err)
			os.Remove(fileName)
			return
		}

		for _, entry := range gitIndex.Entries {
			paths = append(paths, utils.Sha1ToPath(entry.Sha1))
			if !isDownloadable(entry.FileName) {
				continue
			}
			downloadUrl, err := utils.UrlJoin(baseUrl, "../"+strings.TrimLeft(entry.FileName, "/"))
			if err != nil {
				logger.Errorf("Error joining URL: %v", err)
				continue
			}
			mu.Lock()
			*downloadUrls = append(*downloadUrls, downloadUrl)
			mu.Unlock()
		}
	} else {
		paths, err = utils.GetObjectsAndRefs(fileName)
		if err != nil {
			os.Remove(fileName)
			return
		}
	}

	for _, newPath := range paths {
		newUrl, err := utils.UrlJoin(baseUrl, newPath)
		if err != nil {
			logger.Errorf("Failed to join URL %s with path %s: %v", baseUrl, newPath, err)
			continue
		}

		if _, ok := seen.Load(newUrl); ok {
			continue
		}

		sem <- struct{}{}
		wg.Add(1)
		go processGitUrl(client, newUrl, baseUrl, downloadUrls, mu, seen, sem, wg, config)
	}
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

		cmd := exec.Command("git", "checkout", ".")
		if err := cmd.Run(); err != nil {
			logger.Errorf("Error restoring repository in %s: %v", parentDir, err)
		} else {
			logger.Infof("Restored repository in %s", parentDir)
		}

		if err := os.Chdir(cwd); err != nil {
			logger.Errorf("Error changing directory to %s: %v", cwd, err)
			continue
		}
	}

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

			if err := client.FetchFile(url, fileName); err != nil {
				logger.Errorf("Failed to fetch file %s: %v", url, err)
			} else {
				logger.Infof("Downloaded file %s", fileName)
			}
		}(url, fileName)
	}

	wg.Wait()
}

func isDownloadable(fileName string) bool {
	// Список расширений, которые мы не хотим скачивать
	invalidExtensions := []string{".php", ".php4", ".php5"}

	for _, ext := range invalidExtensions {
		if strings.HasSuffix(fileName, ext) {
			return false
		}
	}

	return true
}
