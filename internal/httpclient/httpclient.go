package httpclient

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sync"

	"github.com/hashicorp/go-retryablehttp"
	"github.com/s3rgeym/git-dump/internal/config"
	"github.com/s3rgeym/git-dump/internal/logger"
)

type RetryableHttpClient struct {
	*retryablehttp.Client
}

func CreateHttpClient(config config.Config) *RetryableHttpClient {
	client := retryablehttp.NewClient()
	client.RetryMax = config.MaxRetries
	client.HTTPClient.Timeout = config.ConnTimeout
	client.HTTPClient.Transport = &http.Transport{
		ResponseHeaderTimeout: config.HeaderTimeout,
		IdleConnTimeout:       config.KeepAliveTimeout,
		Proxy:                 http.ProxyFromEnvironment,
	}
	client.Logger = nil
	client.CheckRetry = func(ctx context.Context, resp *http.Response, err error) (bool, error) {
		if resp != nil && resp.StatusCode == http.StatusMovedPermanently {
			return false, nil
		}
		return retryablehttp.DefaultRetryPolicy(ctx, resp, err)
	}

	if config.ProxyUrl != "" {
		proxyUrlParsed, err := url.Parse(config.ProxyUrl)
		if err != nil {
			logger.Fatalf("Failed to parse proxy URL: %v", err)
		}
		client.HTTPClient.Transport.(*http.Transport).Proxy = http.ProxyURL(proxyUrlParsed)
	}

	return &RetryableHttpClient{client}
}

func FetchFile(client *RetryableHttpClient, targetUrl, fileName string, mutex *sync.Mutex, seen *sync.Map, hostErrors map[string]int, config config.Config) error {
	if !config.ForceFetch {
		if _, err := os.Stat(fileName); err == nil {
			logger.Warnf("File %s already exists, skipping fetch", fileName)
			return nil
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("failed to check file existence: %w", err)
		}
	}

	if _, ok := seen.Load(targetUrl); ok {
		logger.Debugf("URL %s is already seen, skipping", targetUrl)
		return nil
	}

	host, err := extractHost(targetUrl)
	if err != nil {
		return fmt.Errorf("failed to extract host: %w", err)
	}

	mutex.Lock()
	if value, ok := hostErrors[host]; ok && value >= config.MaxHostErrors {
		mutex.Unlock()
		logger.Warnf("Skipping host %s due to too many errors", host)
		return nil
	}
	mutex.Unlock()

	logger.Infof("Fetching URL: %s", targetUrl)

	req, err := retryablehttp.NewRequest("GET", targetUrl, nil)
	if err != nil {
		mutex.Lock()
		hostErrors[host]++
		mutex.Unlock()
		return fmt.Errorf("failed to create request for URL %s: %w", targetUrl, err)
	}

	headers := map[string]string{
		"Accept-Language": "en-US,en;q=0.9",
		"Accept":          "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8",
		"Referer":         "https://www.google.com/",
		"User-Agent":      config.UserAgent,
	}

	for key, value := range headers {
		req.Header.Set(key, value)
	}

	ctx, cancel := context.WithTimeout(req.Context(), config.RequestTimeout)
	defer cancel()

	req = req.WithContext(ctx)

	resp, err := client.Do(req)
	if err != nil {
		mutex.Lock()
		hostErrors[host]++
		mutex.Unlock()
		return fmt.Errorf("failed to fetch URL %s: %w", targetUrl, err)
	}
	defer resp.Body.Close()
	seen.Store(targetUrl, true)

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("received bad HTTP status %d for URL %s", resp.StatusCode, targetUrl)
	}

	if err := saveResponse(resp, fileName); err != nil {
		return fmt.Errorf("failed to save file %s: %w", fileName, err)
	}

	logger.Infof("File %s saved successfully", fileName)
	return nil
}

func saveResponse(resp *http.Response, fileName string) error {
	err := os.MkdirAll(filepath.Dir(fileName), 0755)
	if err != nil {
		return fmt.Errorf("failed to create directory for file %s: %w", fileName, err)
	}

	file, err := os.Create(fileName)
	if err != nil {
		return fmt.Errorf("failed to create file %s: %w", fileName, err)
	}
	defer file.Close()

	_, err = io.Copy(file, resp.Body)
	if err != nil {
		return fmt.Errorf("failed to save file %s: %w", fileName, err)
	}
	return nil
}

func extractHost(urlStr string) (string, error) {
	u, err := url.Parse(urlStr)
	if err != nil {
		return "", err
	}
	return u.Host, nil
}
