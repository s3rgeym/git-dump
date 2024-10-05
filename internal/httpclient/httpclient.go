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
	"golang.org/x/time/rate"
)

type HttpClient struct {
	*retryablehttp.Client
	config     config.Config
	mutex      *sync.Mutex
	hostErrors map[string]int
	rl         *rate.Limiter
}

func NewHttpClient(config config.Config) *HttpClient {
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

	rl := rate.NewLimiter(rate.Limit(config.MaxRPS), config.MaxRPS)

	return &HttpClient{
		Client:     client,
		config:     config,
		mutex:      &sync.Mutex{},
		hostErrors: make(map[string]int),
		rl:         rl,
	}
}

func (c *HttpClient) Fetch(targetUrl string) (*http.Response, context.CancelFunc, error) {
	host, err := extractHost(targetUrl)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to extract host: %w", err)
	}

	c.mutex.Lock()
	if value, ok := c.hostErrors[host]; ok && value >= c.config.MaxHostErrors {
		c.mutex.Unlock()
		return nil, nil, fmt.Errorf("skipping host %s due to too many errors", host)
	}
	c.mutex.Unlock()

	if err := c.rl.Wait(context.TODO()); err != nil {
		return nil, nil, fmt.Errorf("error waiting for rate limiter: %w", err)
	}

	logger.Debugf("Fetching URL: %s", targetUrl)

	req, err := retryablehttp.NewRequest("GET", targetUrl, nil)
	if err != nil {
		c.mutex.Lock()
		c.hostErrors[host]++
		c.mutex.Unlock()
		return nil, nil, fmt.Errorf("failed to create request for URL %s: %w", targetUrl, err)
	}

	headers := map[string]string{
		"Accept-Language": "en-US,en;q=0.9",
		"Accept":          "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8",
		"Referer":         "https://www.google.com/",
		"User-Agent":      c.config.UserAgent,
	}

	for key, value := range headers {
		req.Header.Set(key, value)
	}

	ctx, cancel := context.WithTimeout(req.Context(), c.config.RequestTimeout)
	req = req.WithContext(ctx)

	resp, err := c.Do(req)
	if err != nil {
		c.mutex.Lock()
		c.hostErrors[host]++
		c.mutex.Unlock()
		cancel()
		return nil, nil, fmt.Errorf("failed to fetch URL %s: %w", targetUrl, err)
	}

	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		cancel()
		return nil, nil, fmt.Errorf("received bad HTTP status %d for URL %s", resp.StatusCode, targetUrl)
	}

	return resp, cancel, nil
}

func (c *HttpClient) SaveResponse(resp *http.Response, fileName string) error {
	defer resp.Body.Close()

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

func (c *HttpClient) FetchFile(targetUrl, fileName string) (bool, error) {
	resp, cancel, err := c.Fetch(targetUrl)
	if err != nil {
		return false, err
	}
	defer cancel()
	defer resp.Body.Close()
	if err := c.SaveResponse(resp, fileName); err != nil {
		return false, fmt.Errorf("failed to save file %s: %w", fileName, err)
	}

	return true, nil
}

func extractHost(urlStr string) (string, error) {
	u, err := url.Parse(urlStr)
	if err != nil {
		return "", err
	}
	return u.Host, nil
}
