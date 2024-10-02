package config

import (
	"flag"
	"fmt"
	"strings"
	"time"

	"github.com/common-nighthawk/go-figure"
)

type Config struct {
	InputFile        string
	OutputDir        string
	LogLevel         string
	UserAgent        string
	ConnTimeout      time.Duration
	HeaderTimeout    time.Duration
	KeepAliveTimeout time.Duration
	RequestTimeout   time.Duration
	MaxRetries       int
	MaxHostErrors    int
	WorkersNum       int
	MaxRPS           int
	ProxyUrl         string
	ForceFetch       bool
	CommonGitFiles   []string
	NoBanner         bool
}

func ParseFlags() Config {
	var config Config

	// Добавляем флаг для отключения баннера
	flag.BoolVar(&config.NoBanner, "no-banner", false, "Disable banner output")

	flag.StringVar(&config.InputFile, "i", "-", "Path to the file containing a list of URLs to dump (default is stdin)")
	flag.StringVar(&config.OutputDir, "o", "output", "Directory to store the dumped files (default is 'output')")
	flag.StringVar(&config.LogLevel, "log", "fatal", "Logging level (options: debug, info, warn, error, fatal, panic)")
	flag.StringVar(&config.UserAgent, "ua", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/129.0.0.0 Safari/537.36", "User-Agent string to use in HTTP requests")
	flag.DurationVar(&config.ConnTimeout, "connect-timeout", 10*time.Second, "Connection timeout duration")
	flag.DurationVar(&config.HeaderTimeout, "header-timeout", 5*time.Second, "Read Header timeout duration")
	flag.DurationVar(&config.KeepAliveTimeout, "keepalive-timeout", 90*time.Second, "Keep-Alive timeout duration")
	flag.DurationVar(&config.RequestTimeout, "request-timeout", 60*time.Second, "Total request timeout duration")
	flag.IntVar(&config.MaxRetries, "retries", 3, "Maximum number of retries for each request")
	flag.IntVar(&config.MaxHostErrors, "maxhe", 5, "Maximum number of errors per host before skipping")
	flag.IntVar(&config.WorkersNum, "w", 50, "Number of worker goroutines")
	flag.IntVar(&config.MaxRPS, "rps", 150, "Maximum number of requests per second")
	flag.StringVar(&config.ProxyUrl, "proxy", "", "Proxy URL (e.g., socks5://localhost:1080)")
	flag.BoolVar(&config.ForceFetch, "f", false, "Force fetch URLs, even if files already exist")
	flag.Parse()

	// Выводим баннер, если флаг --no-banner не установлен
	if !config.NoBanner {
		printBanner()
	}

	return config
}

func printBanner() {
	banner := figure.NewFigure("Git Dump", "doom", true)
	banner.Print()
	fmt.Println(strings.Repeat("-", 40))
	fmt.Println("This tool fetches Git repository files from a list of URLs and stores them locally.")
	fmt.Println("It supports rate limiting, retries, and parallel processing.")
	fmt.Println()
}
