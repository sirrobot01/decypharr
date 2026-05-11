package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/rs/zerolog"
	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/internal/nntp"
	"github.com/sirrobot01/decypharr/pkg/usenet/parser"
)

func main() {
	// Setup logger
	output := zerolog.ConsoleWriter{Out: os.Stdout, TimeFormat: time.RFC3339}
	logger := zerolog.New(output).With().Timestamp().Logger()
	zerolog.SetGlobalLevel(zerolog.DebugLevel)

	// Check arguments
	if len(os.Args) < 2 {
		fmt.Println("Usage: test-parser <nzb-file>")
		fmt.Println("Example: test-parser test.nzb")
		fmt.Println("")
		fmt.Println("This tool parses an NZB file and displays:")
		fmt.Println("  - File structure and sizes")
		fmt.Println("  - RAR detection and compression method")
		fmt.Println("  - M0 (stored) validation")
		fmt.Println("  - Segment information")
		os.Exit(1)
	}

	nzbFile := os.Args[1]

	logger.Info().
		Str("file", nzbFile).
		Msg("Reading NZB file")

	// Read NZB file
	nzbContent, err := os.ReadFile(nzbFile)
	if err != nil {
		logger.Fatal().
			Err(err).
			Str("file", nzbFile).
			Msg("Failed to read NZB file")
	}

	logger.Info().
		Int("size", len(nzbContent)).
		Msg("NZB file read successfully")

	// Create NNTP client (10 max connections for test)
	config.SetConfigPath("data/")
	cfg := config.Get()
	nntpClient, err := nntp.NewClient(cfg)
	if err != nil {
		logger.Fatal().
			Err(err).
			Msg("Failed to create NNTP client")
	}
	defer nntpClient.Close()

	logger.Info().Msg("NNTP client created successfully")

	// Create NZB parser with manager
	p := parser.NewParser(nntpClient, 10, logger)

	logger.Info().Msg("Parsing NZB file...")

	// Parse NZB
	ctx := context.Background()
	nzb, groups, err := p.Parse(ctx, nzbFile, nzbContent)
	if err != nil {
		logger.Fatal().
			Err(err).
			Msg("Failed to parse NZB")
	}
	updatedNZB, err := p.Process(ctx, nzb, groups)
	if err != nil {
		logger.Fatal().
			Err(err).
			Msg("Failed to process NZB")
	}
	nzb = updatedNZB

	logger.Info().
		Str("id", nzb.ID).
		Str("name", nzb.Name).
		Int64("total_size", nzb.TotalSize).
		Int("logical_files", len(nzb.Files)).
		Int("source_files", len(nzb.Files)).
		Msg("NZB parsed successfully")

	// Print detailed file summary
	fmt.Println("\n" + strings.Repeat("=", 80))
	fmt.Println("FILE SUMMARY")
	fmt.Println(strings.Repeat("=", 80))
	fmt.Printf("NZB ID:     %s\n", nzb.ID)
	fmt.Printf("Name:       %s\n", nzb.Name)
	fmt.Printf("Total Size: %.2f GB\n", float64(nzb.TotalSize)/(1024*1024*1024))
	fmt.Printf("Logical Files: %d\n", len(nzb.Files))
	fmt.Printf("Source Parts:  %d\n", len(nzb.Files))
	fmt.Println(strings.Repeat("=", 80))

	for i, file := range nzb.Files {
		fmt.Printf("\n[%d] %s\n", i+1, file.Name)
		fmt.Printf("    Size:         %.2f MB (%d bytes)\n", float64(file.Size)/(1024*1024), file.Size)
		fmt.Printf("    Segments:     %d\n", len(file.Segments))

		if file.SegmentSize > 0 {
			fmt.Printf("    Segment Size: %.2f KB\n", float64(file.SegmentSize)/1024)
		}

		fmt.Printf("      Password:     %s\n", getPasswordStatus(file.Password))
		fmt.Printf("      Entry:        %s (%d bytes)\n", file.Name, file.Size)
		if file.InternalPath != "" {
			fmt.Printf("      Internal:     %s\n", file.InternalPath)
		}
		if file.IsStored {
			fmt.Printf("      Compression:  ✅ Stored (seekable)\n")
		} else {
			fmt.Printf("      Compression:  ⚠️  Compressed\n")
		}

		// Groups
		if len(file.Groups) > 0 {
			fmt.Printf("    Groups:       %v\n", file.Groups[:min(3, len(file.Groups))])
		}

		// Check for zero-byte segments
		zeroByteCount := 0
		for segIdx, seg := range file.Segments {
			if seg.Bytes <= 0 {
				zeroByteCount++
				if zeroByteCount <= 5 {
					fmt.Printf("    ⚠️  ZERO BYTE SEG[%d]: Bytes=%d, StartOffset=%d, EndOffset=%d, DataStart=%d\n",
						segIdx, seg.Bytes, seg.StartOffset, seg.EndOffset, seg.SegmentDataStart)
				}
			}
		}
		if zeroByteCount > 5 {
			fmt.Printf("    ⚠️  ... and %d more zero-byte segments\n", zeroByteCount-5)
		}
		if zeroByteCount > 0 {
			fmt.Printf("    ⚠️  TOTAL ZERO-BYTE SEGMENTS: %d (this breaks seeking!)\n", zeroByteCount)
		}

		// Show first few and any problematic segment offsets
		if len(file.Segments) > 0 {
			fmt.Printf("    First segment: Bytes=%d, StartOff=%d, EndOff=%d, DataStart=%d\n",
				file.Segments[0].Bytes, file.Segments[0].StartOffset, file.Segments[0].EndOffset, file.Segments[0].SegmentDataStart)
		}
		if len(file.Segments) > 3 {
			fmt.Printf("    Seg[3]: Bytes=%d, StartOff=%d, EndOff=%d, DataStart=%d\n",
				file.Segments[3].Bytes, file.Segments[3].StartOffset, file.Segments[3].EndOffset, file.Segments[3].SegmentDataStart)
		}

		fmt.Println("    " + strings.Repeat("-", 76))
	}

	// Summary statistics
	fmt.Println("\n" + strings.Repeat("=", 80))
	fmt.Println("SUMMARY")
	fmt.Println(strings.Repeat("=", 80))
	fmt.Println(strings.Repeat("=", 80))

	logger.Info().Msg("Test completed successfully")
}

func getPasswordStatus(password string) string {
	if password == "" {
		return "None"
	}
	return "Protected (***)"
}
