package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/rs/zerolog"
	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/internal/nntp"
	"github.com/sirrobot01/decypharr/pkg/usenet/manifest"
	"github.com/sirrobot01/decypharr/pkg/usenet/parser"
)

func main() {
	output := zerolog.ConsoleWriter{Out: os.Stdout, TimeFormat: time.RFC3339}
	log := zerolog.New(output).With().Timestamp().Logger()
	zerolog.SetGlobalLevel(zerolog.DebugLevel)

	localOnly := flag.Bool("local-only", false, "decode the local NZB manifest without connecting to NNTP")
	flag.Usage = func() {
		fmt.Fprintln(flag.CommandLine.Output(), "Usage: test-parser [-local-only] <nzb-file>")
		flag.PrintDefaults()
	}
	flag.Parse()

	if flag.NArg() != 1 {
		flag.Usage()
		os.Exit(2)
	}

	nzbFile := flag.Arg(0)
	content, err := os.ReadFile(nzbFile)
	if err != nil {
		log.Fatal().Err(err).Str("file", nzbFile).Msg("Failed to read NZB file")
	}
	if *localOnly {
		started := time.Now()
		decoded, err := manifest.Decode(bytes.NewReader(content))
		if err != nil {
			log.Fatal().Err(err).Str("file", nzbFile).Msg("Failed to decode local NZB manifest")
		}
		printManifestSummary(nzbFile, decoded, time.Since(started))
		return
	}

	config.SetConfigPath("data/")
	cfg := config.Get()
	client, err := nntp.NewClient(cfg)
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to create NNTP client")
	}
	defer func() {
		if err := client.Close(); err != nil {
			log.Warn().Err(err).Msg("Failed to close NNTP client")
		}
	}()

	maxConcurrent := cfg.Usenet.ProcessingMaxConnections
	if maxConcurrent <= 0 {
		maxConcurrent = cfg.Usenet.MaxConnections
	}
	if maxConcurrent <= 0 {
		maxConcurrent = 10
	}
	p := parser.NewParser(client, maxConcurrent, log)
	parseStarted := time.Now()
	nzb, groups, err := p.Parse(context.Background(), nzbFile, content)
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to parse NZB")
	}
	parseElapsed := time.Since(parseStarted)
	processStarted := time.Now()
	nzb, err = p.Process(context.Background(), nzb, groups)
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to process NZB")
	}
	processElapsed := time.Since(processStarted)

	fmt.Println(strings.Repeat("=", 80))
	fmt.Println("FILE SUMMARY")
	fmt.Println(strings.Repeat("=", 80))
	fmt.Printf("NZB ID:        %s\n", nzb.ID)
	fmt.Printf("Name:          %s\n", nzb.Name)
	fmt.Printf("Total Size:    %.2f GB\n", float64(nzb.TotalSize)/(1024*1024*1024))
	fmt.Printf("Logical Files: %d\n", len(nzb.Files))
	fmt.Printf("Parse Phase:   %s\n", parseElapsed.Round(time.Microsecond))
	fmt.Printf("Process Phase: %s\n", processElapsed.Round(time.Microsecond))

	for i, file := range nzb.Files {
		fmt.Printf("\n[%d] %s\n", i+1, file.Name)
		fmt.Printf("    Size:         %.2f MB (%d bytes)\n", float64(file.Size)/(1024*1024), file.Size)
		fmt.Printf("    Segments:     %d\n", len(file.Segments))
		fmt.Printf("    Password:     %s\n", passwordStatus(file.Password))
		if file.InternalPath != "" {
			fmt.Printf("    Internal:     %s\n", file.InternalPath)
		}
		if file.IsStored {
			fmt.Println("    Compression:  Stored (seekable)")
		} else {
			fmt.Println("    Compression:  Compressed")
		}

		zeroBytes := 0
		for _, segment := range file.Segments {
			if segment.Bytes <= 0 {
				zeroBytes++
			}
		}
		if zeroBytes > 0 {
			fmt.Printf("    Zero-byte segments: %d\n", zeroBytes)
		}
	}

	metrics := p.Metrics()
	fmt.Printf("\nAnalyzer article traffic\n")
	fmt.Printf("    Header requests: %d\n", metrics.HeaderRequests)
	fmt.Printf("    Body requests:   %d\n", metrics.BodyRequests)
	fmt.Printf("    STAT requests:   %d\n", metrics.StatRequests)
	fmt.Printf("    Network BODY:    %d\n", metrics.NetworkBodies)
	fmt.Printf("    Network STAT:    %d\n", metrics.NetworkStats)
	fmt.Printf("    Cache hits:      %d\n", metrics.CacheHits)
	fmt.Printf("    Shared loads:    %d\n", metrics.SharedLoads)
	fmt.Printf("    Bytes fetched:   %d\n", metrics.BytesFetched)
	fmt.Printf("    Cached bodies:   %d (%d bytes)\n", metrics.CachedBodies, metrics.CachedBodyBytes)
	fmt.Printf("    Cached entries:  %d\n", metrics.CachedEntries)

	log.Info().Msg("Parser test completed successfully")
}

func printManifestSummary(filename string, decoded *manifest.Manifest, elapsed time.Duration) {
	fmt.Println(strings.Repeat("=", 80))
	fmt.Println("LOCAL MANIFEST SUMMARY")
	fmt.Println(strings.Repeat("=", 80))
	fmt.Printf("File:              %s\n", filename)
	fmt.Printf("Posted Files:      %d\n", len(decoded.Files))
	fmt.Printf("Available Segments: %d\n", decoded.Stats.AvailableSegments)
	fmt.Printf("Total Segments:    %d\n", decoded.Stats.TotalSegments)
	fmt.Printf("Reported Bytes:    %d\n", decoded.Stats.Bytes)
	fmt.Printf("Decode Time:       %s\n", elapsed.Round(time.Microsecond))
}

func passwordStatus(password string) string {
	if password == "" {
		return "None"
	}
	return "Protected (***)"
}
