package importer

import (
	"bufio"
	"encoding/binary"
	"github.com/afrunt/tape2digi/internal/i18n"
	"io"
	"math"
)

// wavDecoder streams a RIFF/WAVE file: PCM 8/16/24/32-bit, IEEE float
// 32/64-bit, and WAVE_FORMAT_EXTENSIBLE wrappers of those.
type wavDecoder struct {
	r        *bufio.Reader
	rate     int
	channels int
	bits     int
	float    bool
	left     int64 // data bytes not yet read
	frames   int64
	raw      []byte
}

func newWAVDecoder(rs io.ReadSeeker) (*wavDecoder, error) {
	var hdr [12]byte
	if _, err := io.ReadFull(rs, hdr[:]); err != nil || string(hdr[0:4]) != "RIFF" || string(hdr[8:12]) != "WAVE" {
		return nil, i18n.New("importer.invalidWavFile")
	}
	size, err := rs.Seek(0, io.SeekEnd)
	if err != nil {
		return nil, err
	}
	pos := int64(12)
	d := &wavDecoder{}
	haveFmt := false
	for {
		if _, err := rs.Seek(pos, io.SeekStart); err != nil {
			return nil, err
		}
		var ch [8]byte
		if _, err := io.ReadFull(rs, ch[:]); err != nil {
			return nil, i18n.New("importer.theWavFileHasNo")
		}
		clen := int64(binary.LittleEndian.Uint32(ch[4:]))
		body := pos + 8
		switch string(ch[0:4]) {
		case "fmt ":
			if clen < 16 {
				return nil, i18n.New("importer.damagedWavHeader")
			}
			b := make([]byte, min(clen, 40))
			if _, err := io.ReadFull(rs, b); err != nil {
				return nil, i18n.New("importer.damagedWavHeader")
			}
			tag := binary.LittleEndian.Uint16(b[0:])
			d.channels = int(binary.LittleEndian.Uint16(b[2:]))
			d.rate = int(binary.LittleEndian.Uint32(b[4:]))
			d.bits = int(binary.LittleEndian.Uint16(b[14:]))
			if tag == 0xFFFE && len(b) >= 26 {
				tag = binary.LittleEndian.Uint16(b[24:])
			}
			switch tag {
			case 1:
				if d.bits != 8 && d.bits != 16 && d.bits != 24 && d.bits != 32 {
					return nil, i18n.New("importer.unsupportedWavBitDepthBit", d.bits)
				}
			case 3:
				d.float = true
				if d.bits != 32 && d.bits != 64 {
					return nil, i18n.New("importer.unsupportedWavBitDepthBit", d.bits)
				}
			default:
				return nil, i18n.New("importer.unsupportedWavEncodingType", tag)
			}
			if d.channels < 1 || d.channels > 32 || d.rate < 1000 || d.rate > 768000 {
				return nil, i18n.New("importer.unsupportedWavFormat")
			}
			haveFmt = true
		case "data":
			if !haveFmt {
				return nil, i18n.New("importer.inTheWavFileThe")
			}
			// Streamed files may carry a zero or 0xFFFFFFFF size.
			avail := size - body
			if clen == 0 || clen > avail {
				clen = avail
			}
			align := int64(d.channels * d.bits / 8)
			clen -= clen % align
			if clen <= 0 {
				return nil, i18n.New("importer.theFileContainsNoAudio")
			}
			if _, err := rs.Seek(body, io.SeekStart); err != nil {
				return nil, err
			}
			d.r = bufio.NewReaderSize(rs, 256<<10)
			d.left, d.frames = clen, clen/align
			return d, nil
		}
		pos = body + clen + clen&1
	}
}

func (d *wavDecoder) Info() info {
	return info{Rate: d.rate, Channels: d.channels, Bits: d.bits, Float: d.float, Frames: d.frames}
}

func (d *wavDecoder) Next(dst []float32) (int, error) {
	if d.left <= 0 {
		return 0, io.EOF
	}
	bps := d.bits / 8
	n := len(dst) / d.channels * d.channels
	if int64(n*bps) > d.left {
		n = int(d.left) / bps
	}
	if n == 0 {
		return 0, io.EOF
	}
	if cap(d.raw) < n*bps {
		d.raw = make([]byte, n*bps)
	}
	b := d.raw[:n*bps]
	if _, err := io.ReadFull(d.r, b); err != nil {
		return 0, i18n.New("importer.theWavFileIsTruncated")
	}
	d.left -= int64(len(b))
	for i := 0; i < n; i++ {
		p := b[i*bps:]
		switch {
		case d.float && bps == 4:
			dst[i] = math.Float32frombits(binary.LittleEndian.Uint32(p))
		case d.float:
			dst[i] = float32(math.Float64frombits(binary.LittleEndian.Uint64(p)))
		case bps == 1:
			dst[i] = float32(int(p[0])-128) / 128
		case bps == 2:
			dst[i] = float32(int16(binary.LittleEndian.Uint16(p))) / 32768
		case bps == 3:
			v := int32(p[0]) | int32(p[1])<<8 | int32(int8(p[2]))<<16
			dst[i] = float32(v) / 8388608
		default:
			dst[i] = float32(int32(binary.LittleEndian.Uint32(p))) / 2147483648
		}
	}
	return n, nil
}
