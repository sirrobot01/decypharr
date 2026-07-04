package usenet

import (
	"encoding/binary"
	"fmt"
	"math"
	"time"
	"unsafe"

	"github.com/klauspost/compress/zstd"
	"github.com/sirrobot01/decypharr/pkg/storage"
)

// On-disk NZB metadata format v2.
//
// Goals: small on disk, fast to decode, and low allocation pressure when
// loading. The format is split into three independently zstd-framed regions so
// that header-only reads (status/path/file-list, by far the most common access
// pattern) never decompress or allocate the multi-megabyte segment map:
//
//	[1]        magic byte (codecMagicV2)
//	[uvarint]  len(zstd(header))   + zstd(header)     -- NZB scalars + per-file meta (no segments)
//	[uvarint]  len(zstd(segMeta))  + zstd(segMeta)    -- columnar numeric/group columns for all segments
//	[...]      zstd(msgIDs)                            -- concatenated message-id bytes, length-prefixed
//
// Full decode produces a *storage.NZB whose segments live in ONE contiguous
// []NZBSegment backing array (each file takes a sub-slice, no per-file copy),
// whose Group strings are interned (one allocation per unique group), and whose
// MessageID strings alias the single decompressed msgIDs buffer via
// unsafe.String (one allocation for all ids instead of one per segment).
const codecMagicV2 = 0xB1

var (
	zstdEnc *zstd.Encoder
	zstdDec *zstd.Decoder
)

func init() {
	// EncodeAll/DecodeAll on these shared instances are safe for concurrent use.
	zstdEnc, _ = zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedDefault))
	zstdDec, _ = zstd.NewReader(nil, zstd.WithDecoderConcurrency(0))
}

// ---------------------------------------------------------------------------
// byte writer
// ---------------------------------------------------------------------------

type byteWriter struct {
	buf []byte
}

func (w *byteWriter) uvarint(v uint64) { w.buf = binary.AppendUvarint(w.buf, v) }
func (w *byteWriter) varint(v int64)   { w.buf = binary.AppendVarint(w.buf, v) }
func (w *byteWriter) str(s string) {
	w.uvarint(uint64(len(s)))
	w.buf = append(w.buf, s...)
}
func (w *byteWriter) raw(b []byte) {
	w.uvarint(uint64(len(b)))
	w.buf = append(w.buf, b...)
}
func (w *byteWriter) boolean(b bool) {
	if b {
		w.buf = append(w.buf, 1)
	} else {
		w.buf = append(w.buf, 0)
	}
}
func (w *byteWriter) f64(f float64) {
	w.buf = binary.LittleEndian.AppendUint64(w.buf, math.Float64bits(f))
}

// ---------------------------------------------------------------------------
// byte reader
// ---------------------------------------------------------------------------

type byteReader struct {
	buf []byte
	pos int
}

func (r *byteReader) uvarint() (uint64, error) {
	v, n := binary.Uvarint(r.buf[r.pos:])
	if n <= 0 {
		return 0, fmt.Errorf("nzbcodec: bad uvarint at %d", r.pos)
	}
	r.pos += n
	return v, nil
}

func (r *byteReader) varint() (int64, error) {
	v, n := binary.Varint(r.buf[r.pos:])
	if n <= 0 {
		return 0, fmt.Errorf("nzbcodec: bad varint at %d", r.pos)
	}
	r.pos += n
	return v, nil
}

func (r *byteReader) span() ([]byte, error) {
	n, err := r.uvarint()
	if err != nil {
		return nil, err
	}
	if r.pos+int(n) > len(r.buf) {
		return nil, fmt.Errorf("nzbcodec: span out of range")
	}
	b := r.buf[r.pos : r.pos+int(n)]
	r.pos += int(n)
	return b, nil
}

// strCopy returns an owned copy (use for small/long-lived header strings).
func (r *byteReader) strCopy() (string, error) {
	b, err := r.span()
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// strAlias returns a string aliasing r.buf without copying. The caller must
// keep r.buf alive for as long as the returned string is used.
func (r *byteReader) strAlias() (string, error) {
	b, err := r.span()
	if err != nil {
		return "", err
	}
	if len(b) == 0 {
		return "", nil
	}
	return unsafe.String(&b[0], len(b)), nil
}

func (r *byteReader) bytesCopy() ([]byte, error) {
	b, err := r.span()
	if err != nil {
		return nil, err
	}
	if len(b) == 0 {
		return nil, nil
	}
	out := make([]byte, len(b))
	copy(out, b)
	return out, nil
}

// skip advances past one length-prefixed span without materializing it.
func (r *byteReader) skip() error {
	n, err := r.uvarint()
	if err != nil {
		return err
	}
	if r.pos+int(n) > len(r.buf) {
		return fmt.Errorf("nzbcodec: skip out of range")
	}
	r.pos += int(n)
	return nil
}

func (r *byteReader) boolean() (bool, error) {
	if r.pos >= len(r.buf) {
		return false, fmt.Errorf("nzbcodec: bool out of range")
	}
	b := r.buf[r.pos]
	r.pos++
	return b != 0, nil
}

func (r *byteReader) f64() (float64, error) {
	if r.pos+8 > len(r.buf) {
		return 0, fmt.Errorf("nzbcodec: float out of range")
	}
	v := binary.LittleEndian.Uint64(r.buf[r.pos:])
	r.pos += 8
	return math.Float64frombits(v), nil
}

// ---------------------------------------------------------------------------
// encode
// ---------------------------------------------------------------------------

func encodeNZBV2(nzb *storage.NZB) ([]byte, error) {
	header := encodeHeader(nzb)
	segMeta, msgIDs := encodeSegments(nzb)

	hc := zstdEnc.EncodeAll(header, nil)
	sc := zstdEnc.EncodeAll(segMeta, nil)
	mc := zstdEnc.EncodeAll(msgIDs, nil)

	out := make([]byte, 0, 1+binary.MaxVarintLen64*2+len(hc)+len(sc)+len(mc))
	out = append(out, codecMagicV2)
	out = binary.AppendUvarint(out, uint64(len(hc)))
	out = append(out, hc...)
	out = binary.AppendUvarint(out, uint64(len(sc)))
	out = append(out, sc...)
	out = append(out, mc...)
	return out, nil
}

func encodeHeader(nzb *storage.NZB) []byte {
	w := &byteWriter{}
	w.str(nzb.ID)
	w.str(nzb.Name)
	w.str(nzb.Title)
	w.str(nzb.Path)
	w.varint(nzb.TotalSize)
	w.varint(nzb.DatePosted.Unix())
	w.str(nzb.Category)
	w.uvarint(uint64(len(nzb.Groups)))
	for _, g := range nzb.Groups {
		w.str(g)
	}
	w.boolean(nzb.Downloaded)
	w.varint(nzb.AddedOn.Unix())
	w.varint(nzb.LastActivity.Unix())
	w.str(nzb.Status)
	w.f64(nzb.Progress)
	w.f64(nzb.Percentage)
	w.varint(nzb.SizeDownloaded)
	w.varint(nzb.ETA)
	w.varint(nzb.Speed)
	w.varint(nzb.CompletedOn.Unix())
	w.boolean(nzb.IsBad)
	w.str(nzb.Storage)
	w.str(nzb.FailMessage)
	w.str(nzb.Password)

	w.uvarint(uint64(len(nzb.Files)))
	for i := range nzb.Files {
		f := &nzb.Files[i]
		// NzbID is omitted (filled from nzb.ID on decode).
		w.str(f.Name)
		w.str(f.InternalPath)
		w.varint(f.Size)
		w.varint(f.StartOffset)
		w.uvarint(uint64(len(f.Groups)))
		for _, g := range f.Groups {
			w.str(g)
		}
		w.str(string(f.FileType))
		w.str(f.Password)
		w.boolean(f.IsDeleted)
		w.boolean(f.IsStored)
		w.varint(f.SegmentSize)
		w.raw(f.EncryptionKey)
		w.raw(f.EncryptionIV)
		w.boolean(f.IsEncrypted)
		w.uvarint(uint64(len(f.Segments)))
	}
	return w.buf
}

// encodeSegments produces two buffers: segMeta (columnar numeric + group data
// for every segment across all files, in file order) and msgIDs (the
// concatenated, length-prefixed message ids).
func encodeSegments(nzb *storage.NZB) (segMeta, msgIDs []byte) {
	total := 0
	for i := range nzb.Files {
		total += len(nzb.Files[i].Segments)
	}

	mw := &byteWriter{buf: make([]byte, 0, total*48)}
	sw := &byteWriter{}

	// Group interning table.
	groupIdx := make(map[string]uint64)
	var groups []string
	idxOf := func(g string) uint64 {
		if id, ok := groupIdx[g]; ok {
			return id
		}
		id := uint64(len(groups))
		groupIdx[g] = id
		groups = append(groups, g)
		return id
	}

	// Pre-walk to build the group table and per-segment index column data.
	idxCol := make([]uint64, 0, total)
	for i := range nzb.Files {
		for j := range nzb.Files[i].Segments {
			idxCol = append(idxCol, idxOf(nzb.Files[i].Segments[j].Group))
		}
	}

	// Group table.
	sw.uvarint(uint64(len(groups)))
	for _, g := range groups {
		sw.str(g)
	}

	// Numeric columns (grouped by field for better compression).
	for i := range nzb.Files {
		for j := range nzb.Files[i].Segments {
			sw.varint(int64(nzb.Files[i].Segments[j].Number))
		}
	}
	for i := range nzb.Files {
		for j := range nzb.Files[i].Segments {
			sw.varint(nzb.Files[i].Segments[j].Bytes)
		}
	}
	for i := range nzb.Files {
		for j := range nzb.Files[i].Segments {
			sw.varint(nzb.Files[i].Segments[j].StartOffset)
		}
	}
	for i := range nzb.Files {
		for j := range nzb.Files[i].Segments {
			sw.varint(nzb.Files[i].Segments[j].EndOffset)
		}
	}
	for i := range nzb.Files {
		for j := range nzb.Files[i].Segments {
			sw.varint(nzb.Files[i].Segments[j].SegmentDataStart)
		}
	}
	for _, idx := range idxCol {
		sw.uvarint(idx)
	}

	// Message id region (its own buffer so a full decode retains only these
	// bytes, not the numeric columns).
	for i := range nzb.Files {
		for j := range nzb.Files[i].Segments {
			mw.str(nzb.Files[i].Segments[j].MessageID)
		}
	}

	return sw.buf, mw.buf
}

// ---------------------------------------------------------------------------
// decode
// ---------------------------------------------------------------------------

// isCodecV2 reports whether data uses the v2 format (vs legacy protobuf).
func isCodecV2(data []byte) bool {
	return len(data) > 0 && data[0] == codecMagicV2
}

// splitRegions returns the three compressed regions of a v2 blob.
func splitRegions(data []byte) (hc, sc, mc []byte, err error) {
	if !isCodecV2(data) {
		return nil, nil, nil, fmt.Errorf("nzbcodec: not a v2 blob")
	}
	r := &byteReader{buf: data, pos: 1}
	hLen, err := r.uvarint()
	if err != nil {
		return nil, nil, nil, err
	}
	if r.pos+int(hLen) > len(data) {
		return nil, nil, nil, fmt.Errorf("nzbcodec: header region out of range")
	}
	hc = data[r.pos : r.pos+int(hLen)]
	r.pos += int(hLen)

	sLen, err := r.uvarint()
	if err != nil {
		return nil, nil, nil, err
	}
	if r.pos+int(sLen) > len(data) {
		return nil, nil, nil, fmt.Errorf("nzbcodec: seg region out of range")
	}
	sc = data[r.pos : r.pos+int(sLen)]
	r.pos += int(sLen)

	mc = data[r.pos:]
	return hc, sc, mc, nil
}

// decodeNZBV2Header decodes only the NZB scalars and per-file metadata. The
// returned files have nil Segments. It never decompresses the segment regions.
func decodeNZBV2Header(data []byte) (*storage.NZB, error) {
	hc, _, _, err := splitRegions(data)
	if err != nil {
		return nil, err
	}
	header, err := zstdDec.DecodeAll(hc, nil)
	if err != nil {
		return nil, fmt.Errorf("nzbcodec: decompress header: %w", err)
	}
	nzb, _, err := decodeHeader(header)
	return nzb, err
}

// decodeNZBV2 fully decodes an NZB including its segment map.
func decodeNZBV2(data []byte) (*storage.NZB, error) {
	hc, sc, mc, err := splitRegions(data)
	if err != nil {
		return nil, err
	}
	header, err := zstdDec.DecodeAll(hc, nil)
	if err != nil {
		return nil, fmt.Errorf("nzbcodec: decompress header: %w", err)
	}
	nzb, counts, err := decodeHeader(header)
	if err != nil {
		return nil, err
	}

	segMeta, err := zstdDec.DecodeAll(sc, nil)
	if err != nil {
		return nil, fmt.Errorf("nzbcodec: decompress seg meta: %w", err)
	}
	// msgIDs is retained (aliased by MessageID strings); keep this buffer alive.
	msgIDs, err := zstdDec.DecodeAll(mc, nil)
	if err != nil {
		return nil, fmt.Errorf("nzbcodec: decompress msg ids: %w", err)
	}

	if err := decodeSegments(nzb, counts, segMeta, msgIDs); err != nil {
		return nil, err
	}
	return nzb, nil
}

// decodeHeader returns the NZB (segments nil) and the per-file segment counts.
func decodeHeader(buf []byte) (*storage.NZB, []int, error) {
	r := &byteReader{buf: buf}
	nzb := &storage.NZB{}

	var err error
	get := func(dst *string, alias bool) bool {
		var s string
		if alias {
			s, err = r.strAlias()
		} else {
			s, err = r.strCopy()
		}
		if err != nil {
			return false
		}
		*dst = s
		return true
	}

	// Header strings are long-lived and few; copy them so the (small) header
	// buffer can be freed.
	if !get(&nzb.ID, false) || !get(&nzb.Name, false) || !get(&nzb.Title, false) || !get(&nzb.Path, false) {
		return nil, nil, err
	}
	if nzb.TotalSize, err = r.varint(); err != nil {
		return nil, nil, err
	}
	if nzb.DatePosted, err = readTime(r); err != nil {
		return nil, nil, err
	}
	if !get(&nzb.Category, false) {
		return nil, nil, err
	}
	if nzb.Groups, err = readStrings(r); err != nil {
		return nil, nil, err
	}
	if nzb.Downloaded, err = r.boolean(); err != nil {
		return nil, nil, err
	}
	if nzb.AddedOn, err = readTime(r); err != nil {
		return nil, nil, err
	}
	if nzb.LastActivity, err = readTime(r); err != nil {
		return nil, nil, err
	}
	if !get(&nzb.Status, false) {
		return nil, nil, err
	}
	if nzb.Progress, err = r.f64(); err != nil {
		return nil, nil, err
	}
	if nzb.Percentage, err = r.f64(); err != nil {
		return nil, nil, err
	}
	if nzb.SizeDownloaded, err = r.varint(); err != nil {
		return nil, nil, err
	}
	if nzb.ETA, err = r.varint(); err != nil {
		return nil, nil, err
	}
	if nzb.Speed, err = r.varint(); err != nil {
		return nil, nil, err
	}
	if nzb.CompletedOn, err = readTime(r); err != nil {
		return nil, nil, err
	}
	if nzb.IsBad, err = r.boolean(); err != nil {
		return nil, nil, err
	}
	if !get(&nzb.Storage, false) || !get(&nzb.FailMessage, false) || !get(&nzb.Password, false) {
		return nil, nil, err
	}

	nFiles, err := r.uvarint()
	if err != nil {
		return nil, nil, err
	}
	nzb.Files = make([]storage.NZBFile, nFiles)
	counts := make([]int, nFiles)
	for i := uint64(0); i < nFiles; i++ {
		f := &nzb.Files[i]
		f.NzbID = nzb.ID
		var ft string
		if !get(&f.Name, false) || !get(&f.InternalPath, false) {
			return nil, nil, err
		}
		if f.Size, err = r.varint(); err != nil {
			return nil, nil, err
		}
		if f.StartOffset, err = r.varint(); err != nil {
			return nil, nil, err
		}
		if f.Groups, err = readStrings(r); err != nil {
			return nil, nil, err
		}
		if !get(&ft, false) {
			return nil, nil, err
		}
		f.FileType = storage.NZBFileType(ft)
		if !get(&f.Password, false) {
			return nil, nil, err
		}
		if f.IsDeleted, err = r.boolean(); err != nil {
			return nil, nil, err
		}
		if f.IsStored, err = r.boolean(); err != nil {
			return nil, nil, err
		}
		if f.SegmentSize, err = r.varint(); err != nil {
			return nil, nil, err
		}
		if f.EncryptionKey, err = r.bytesCopy(); err != nil {
			return nil, nil, err
		}
		if f.EncryptionIV, err = r.bytesCopy(); err != nil {
			return nil, nil, err
		}
		if f.IsEncrypted, err = r.boolean(); err != nil {
			return nil, nil, err
		}
		c, err := r.uvarint()
		if err != nil {
			return nil, nil, err
		}
		counts[i] = int(c)
	}
	return nzb, counts, nil
}

// decodeSegments fills nzb.Files[*].Segments from the columnar segMeta and the
// aliased msgIDs buffer. All segments share one backing array; each file takes
// a sub-slice. msgIDs must remain alive for the lifetime of the NZB.
func decodeSegments(nzb *storage.NZB, counts []int, segMeta, msgIDs []byte) error {
	total := 0
	for _, c := range counts {
		total += c
	}

	r := &byteReader{buf: segMeta}

	// Group table.
	groups, err := readStrings(r)
	if err != nil {
		return err
	}

	segs := make([]storage.NZBSegment, total)

	for i := 0; i < total; i++ {
		v, err := r.varint()
		if err != nil {
			return err
		}
		segs[i].Number = int(v)
	}
	for i := 0; i < total; i++ {
		if segs[i].Bytes, err = r.varint(); err != nil {
			return err
		}
	}
	for i := 0; i < total; i++ {
		if segs[i].StartOffset, err = r.varint(); err != nil {
			return err
		}
	}
	for i := 0; i < total; i++ {
		if segs[i].EndOffset, err = r.varint(); err != nil {
			return err
		}
	}
	for i := 0; i < total; i++ {
		if segs[i].SegmentDataStart, err = r.varint(); err != nil {
			return err
		}
	}
	for i := 0; i < total; i++ {
		idx, err := r.uvarint()
		if err != nil {
			return err
		}
		if int(idx) >= len(groups) {
			return fmt.Errorf("nzbcodec: group index %d out of range", idx)
		}
		segs[i].Group = groups[idx]
	}

	// Message ids alias the msgIDs buffer (no per-id allocation).
	mr := &byteReader{buf: msgIDs}
	for i := 0; i < total; i++ {
		if segs[i].MessageID, err = mr.strAlias(); err != nil {
			return err
		}
	}

	// Hand out sub-slices to each file (no copy).
	off := 0
	for i := range nzb.Files {
		c := counts[i]
		nzb.Files[i].Segments = segs[off : off+c : off+c]
		off += c
	}
	return nil
}

// decodeFileMessageIDsSampled decodes only the sampled message ids of a single
// file. It decompresses just the header and the message-id region (never the
// numeric segMeta), builds no NZBSegment structs, and returns owned copies of
// only the sampled ids so the large decompressed buffer is freed immediately.
// This is the low-memory path used by repair availability probes.
//
// It returns (nil, -1, nil) when the file is not found or has no segments.
func decodeFileMessageIDsSampled(data []byte, filename string, percent int) (ids []string, segCount int, err error) {
	hc, _, mc, err := splitRegions(data)
	if err != nil {
		return nil, 0, err
	}
	header, err := zstdDec.DecodeAll(hc, nil)
	if err != nil {
		return nil, 0, fmt.Errorf("nzbcodec: decompress header: %w", err)
	}
	nzb, counts, err := decodeHeader(header)
	if err != nil {
		return nil, 0, err
	}

	// Locate the requested (non-deleted) file and its segment range.
	target := -1
	before := 0
	for i := range nzb.Files {
		if nzb.Files[i].Name == filename && !nzb.Files[i].IsDeleted {
			target = i
			break
		}
		before += counts[i]
	}
	if target == -1 {
		return nil, -1, nil
	}
	c := counts[target]
	if c == 0 {
		return nil, 0, nil
	}

	want := sampleIndices(c, percent)
	wantSet := make(map[int]struct{}, len(want))
	for _, idx := range want {
		wantSet[idx] = struct{}{}
	}

	msgIDs, err := zstdDec.DecodeAll(mc, nil)
	if err != nil {
		return nil, 0, fmt.Errorf("nzbcodec: decompress msg ids: %w", err)
	}
	mr := &byteReader{buf: msgIDs}

	// Skip earlier files' ids without allocating.
	for i := 0; i < before; i++ {
		if err := mr.skip(); err != nil {
			return nil, 0, err
		}
	}

	out := make([]string, 0, len(want))
	for j := 0; j < c; j++ {
		if _, ok := wantSet[j]; ok {
			// Owned copy: lets the decompressed buffer be collected.
			s, err := mr.strCopy()
			if err != nil {
				return nil, 0, err
			}
			out = append(out, s)
			continue
		}
		if err := mr.skip(); err != nil {
			return nil, 0, err
		}
	}
	return out, c, nil
}

// sampleIndices returns the segment indices to probe for availability: always
// the first and last, plus a uniform sample of the middle. Mirrors the
// distribution of sampleSegments but works on indices alone.
func sampleIndices(total, percent int) []int {
	if total == 0 {
		return nil
	}
	if percent >= 100 || total <= 3 {
		out := make([]int, total)
		for i := range out {
			out[i] = i
		}
		return out
	}

	targetCount := (total * percent) / 100
	if targetCount < 2 {
		targetCount = 2
	}
	if targetCount > total {
		targetCount = total
	}

	out := make([]int, 0, targetCount)
	out = append(out, 0)
	middleCount := targetCount - 2
	if middleCount > 0 {
		mlen := total - 2
		step := float64(mlen) / float64(middleCount+1)
		for i := 0; i < middleCount; i++ {
			idx := int(step * float64(i+1))
			if idx >= mlen {
				idx = mlen - 1
			}
			out = append(out, 1+idx)
		}
	}
	out = append(out, total-1)
	return out
}

func readStrings(r *byteReader) ([]string, error) {
	n, err := r.uvarint()
	if err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, nil
	}
	out := make([]string, n)
	for i := range out {
		// Group/header strings copied: few unique, long-lived.
		if out[i], err = r.strCopy(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func readTime(r *byteReader) (time.Time, error) {
	sec, err := r.varint()
	if err != nil {
		return time.Time{}, err
	}
	return time.Unix(sec, 0), nil
}
