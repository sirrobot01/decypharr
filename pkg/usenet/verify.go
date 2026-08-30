package usenet

import (
	"bytes"
	"context"
	"errors"
	"fmt"

	"github.com/sirrobot01/decypharr/internal/customerror"
	"github.com/sirrobot01/decypharr/internal/nntp"
	"github.com/sirrobot01/decypharr/internal/utils"
	"github.com/sirrobot01/decypharr/pkg/storage"
)

// verifyHeadBytes is how much of the file head is read for verification. It
// spans two full MPEG-TS packets (with room for the M2TS 4-byte prefix) so the
// sync-byte stride check works; every other signature sits in the first 16
// bytes. The whole range lands inside the first article, so verification costs
// one article download.
const verifyHeadBytes = 512

// headSignatureOK reports whether head starts with any known media container
// signature (video, audio, or playlist — everything utils.IsMediaFile
// covers). The extension is deliberately ignored: releases are sometimes
// mislabeled (an MP4 posted as .mkv plays fine), so any recognized container
// passes. Only a head that matches nothing is corrupt.
func headSignatureOK(head []byte) bool {
	if len(head) < 16 {
		return false
	}
	switch {
	case bytes.HasPrefix(head, []byte{0x1A, 0x45, 0xDF, 0xA3}), // EBML: mkv/webm
		bytes.HasPrefix(head, []byte("RIFF")), // avi/wav
		bytes.HasPrefix(head, []byte("OggS")), // ogv/ogm/ogg/opus
		bytes.HasPrefix(head, []byte("FLV\x01")),
		bytes.HasPrefix(head, []byte{0x30, 0x26, 0xB2, 0x75, 0x8E, 0x66, 0xCF, 0x11}), // ASF: wmv/wma
		bytes.HasPrefix(head, []byte{0x00, 0x00, 0x01, 0xBA}), // MPEG-PS: mpg/vob
		bytes.HasPrefix(head, []byte("ID3")),                  // MP3 with ID3v2 tag
		bytes.HasPrefix(head, []byte("fLaC")),
		bytes.HasPrefix(head, []byte("FORM")),     // AIFF
		bytes.HasPrefix(head, []byte("MAC ")),     // Monkey's Audio
		bytes.HasPrefix(head, []byte("wvpk")),     // WavPack
		bytes.HasPrefix(head, []byte("DVDVIDEO")), // ifo
		bytes.HasPrefix(head, []byte("#EXTM3U")):  // m3u playlist
		return true
	}
	// Bare MPEG audio / ADTS AAC frame sync: 0xFF plus three sync bits.
	if head[0] == 0xFF && head[1]&0xE0 == 0xE0 {
		return true
	}
	// ISO BMFF (mp4/m4v/mov/m4a): 4-byte box size, then a known top-level box type.
	switch string(head[4:8]) {
	case "ftyp", "styp", "moov", "mdat", "free", "skip", "wide", "pnot":
		return true
	}
	// MPEG-TS: sync byte 0x47 every 188 bytes. A single 0x47 is too weak a
	// signal (1/256 of garbage heads would pass), so require the stride.
	if len(head) >= 377 && head[0] == 0x47 && head[188] == 0x47 && head[376] == 0x47 {
		return true
	}
	// BDAV/M2TS: 4-byte timestamp prefix per 192-byte packet.
	if len(head) >= 389 && head[4] == 0x47 && head[196] == 0x47 && head[388] == 0x47 {
		return true
	}
	return false
}

// VerifyFileHead reads the head of one assembled file through the same
// reader stack that serves WebDAV/streams and requires a known container
// signature. This is the only check that sees the file as players do:
// article-availability probes pass NZBs whose articles all exist but were
// assembled in the wrong order (RAR volume misorder, misordered raw posts),
// which serve garbage from byte 0.
//
// Returns UsenetCorruptContentError when the head matches no signature,
// UsenetSegmentMissingError when the head article is gone, and the raw error
// for connection-level failures (callers treat those as "couldn't check").
func (u *Usenet) VerifyFileHead(ctx context.Context, file *storage.NZBFile) error {
	if file.Size > 0 && file.Size < verifyHeadBytes {
		return nil // too small to classify; not worth failing a grab over
	}
	entry, err := u.createEntry(file, 0)
	if err != nil {
		return err
	}
	defer entry.cleanup()

	readerAt, _, err := entry.getOrCreateReader()
	if err != nil {
		return err
	}
	cursor := readerAt.OpenCursor()
	defer cursor.Close()

	head := make([]byte, verifyHeadBytes)
	n, err := cursor.ReadAtContext(ctx, head, 0)
	if err != nil && n < 16 {
		if nntp.IsArticleNotFoundError(err) {
			return fmt.Errorf("head article of %q missing: %w", file.Name, customerror.UsenetSegmentMissingError)
		}
		return err
	}
	if headSignatureOK(head[:n]) {
		return nil
	}
	return fmt.Errorf("head of %q matches no media container signature: %w", file.Name, customerror.UsenetCorruptContentError)
}

// VerifyFile head-verifies one stored file of a completed NZB. Non-media
// files pass without a read. Used by the repair sweep's verify-content probe.
func (u *Usenet) VerifyFile(ctx context.Context, nzoID, filename string) error {
	if !utils.IsMediaFile(filename) {
		return nil
	}
	file, err := u.getFile(nzoID, filename)
	if err != nil {
		return err
	}
	return u.VerifyFileHead(ctx, file)
}

// verifyNZBContent is the import-time content gate: it head-verifies every
// media file of a just-processed NZB. Definitive failures (no
// container signature, head article missing) fail the NZB so the arr grabs a
// replacement release. Connection-level errors are non-fatal, matching
// checkNZBAvailability — a provider hiccup must not fail an import.
func (u *Usenet) verifyNZBContent(ctx context.Context, nzb *storage.NZB) error {
	for i := range nzb.Files {
		file := &nzb.Files[i]
		if file.IsDeleted || len(file.Segments) == 0 || !utils.IsMediaFile(file.Name) {
			continue
		}
		switch file.FileType {
		case storage.NZBFileTypePar2, storage.NZBFileTypeIgnore:
			continue
		}
		if ctx.Err() != nil {
			return nil // cancelled/timed out: not a content failure
		}
		err := u.VerifyFileHead(ctx, file)
		if err == nil {
			continue
		}
		if errors.Is(err, customerror.UsenetCorruptContentError) || errors.Is(err, customerror.UsenetSegmentMissingError) {
			u.logger.Warn().
				Err(err).
				Str("nzb_id", nzb.ID).
				Str("file", file.Name).
				Msg("Content verification failed; failing NZB")
			return err
		}
		u.logger.Warn().
			Err(err).
			Str("nzb_id", nzb.ID).
			Str("file", file.Name).
			Msg("Non-fatal error during content verification, ignoring")
	}
	return nil
}
