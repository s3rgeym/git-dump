package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sync"

	"github.com/s3rgeym/git-dump/internal/config"
	"github.com/s3rgeym/git-dump/internal/httpclient"
	"github.com/s3rgeym/git-dump/internal/logger"
	"github.com/s3rgeym/git-dump/internal/utils"
	"golang.org/x/time/rate"
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

	client := httpclient.CreateHttpClient(config)
	rl := rate.NewLimiter(rate.Limit(config.MaxRPS), config.MaxRPS)

	sem := make(chan struct{}, config.WorkersNum)
	var wg sync.WaitGroup
	var mutex sync.Mutex
	seen := sync.Map{}
	hostErrors := make(map[string]int, 0)
	repos := make([]string, 0)

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
			go processUrl(targetUrl, baseUrl, sem, &wg, rl, &mutex, &seen, hostErrors, client, config)
		}
	}

	wg.Wait()

	cwd, err := os.Getwd()
	if err != nil {
		logger.Fatalf("Failed to get current working directory: %v", err)
	}

	for _, repoPath := range repos {
		absRepoPath, err := filepath.Abs(repoPath)
		if err != nil {
			logger.Fatalf("Error getting absolute path for %s: %v", repoPath, err)
		}

		parentDir := filepath.Dir(absRepoPath)
		if err := os.Chdir(parentDir); err != nil {
			logger.Fatalf("Error changing directory to %s: %v", parentDir, err)
		}

		cmd := exec.Command("git", "checkout", ".")
		if err := cmd.Run(); err != nil {
			logger.Errorf("Error restoring repository in %s: %v", parentDir, err)
		} else {
			logger.Infof("Restored repository in %s", parentDir)
		}

		if err := os.Chdir(cwd); err != nil {
			logger.Fatalf("Error changing directory to %s: %v", cwd, err)
		}
	}

	logger.Info("🎉 Finished!")
}

func processUrl(targetUrl, baseUrl string, sem chan struct{}, wg *sync.WaitGroup, rl *rate.Limiter, mutex *sync.Mutex, seen *sync.Map, hostErrors map[string]int, client *httpclient.RetryableHttpClient, config config.Config) {
	defer func() {
		<-sem
		wg.Done()
	}()

	if err := rl.Wait(context.TODO()); err != nil {
		logger.Errorf("Error waiting for rate limiter: %v", err)
		return
	}

	fileName, err := utils.UrlToLocalPath(targetUrl, config.OutputDir)
	if err != nil {
		logger.Errorf("Failed to convert URL to save path: %v", err)
		return
	}

	if err := httpclient.FetchFile(client, targetUrl, fileName, mutex, seen, hostErrors, config); err != nil {
		logger.Errorf("Failed to fetch file %s: %v", targetUrl, err)
		return
	}

	paths, err := utils.ExtractGitPaths(fileName)
	if err != nil {
		logger.Errorf("Failed to process file %s: %v", fileName, err)
		return
	}

	for _, newPath := range paths {
		newUrl, err := utils.UrlJoin(baseUrl, newPath)
		if err != nil {
			logger.Errorf("Failed to join URL %s with path %s: %v", baseUrl, newPath, err)
			continue
		}

		if _, ok := seen.Load(newUrl); !ok {
			sem <- struct{}{}
			wg.Add(1)
			go processUrl(newUrl, baseUrl, sem, wg, rl, mutex, seen, hostErrors, client, config)
		}
	}
}
