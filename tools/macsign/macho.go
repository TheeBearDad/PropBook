package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
)

const (
	mhMagic64   = 0xfeedfacf
	fatMagic    = 0xcafebabe
	mhExecute   = 0x2
	lcSegment64 = 0x19
	lcCodeSig   = 0x1d

	cpuX86_64 = 0x01000007
	cpuARM64  = 0x0100000c

	csMagicRequirements   = 0xfade0c01
	csMagicCodeDirectory  = 0xfade0c02
	csMagicEmbeddedSig    = 0xfade0cc0
	csMagicBlobWrapper    = 0xfade0b01
	csSlotCodeDirectory   = 0
	csSlotInfo            = 1
	csSlotRequirements    = 2
	csSlotResourceDir     = 3
	csSlotSignature       = 0x10000
	csHashSHA256          = 2
	csFlagAdhoc           = 0x2
	csExecSegMainBinary   = 0x1
	cdVersionExecSeg      = 0x20400
	cdHeaderSize          = 88 // CodeDirectory fields through execSegFlags (version 0x20400)
	pageShift             = 12
	pageSize              = 1 << pageShift
	linkeditVMAlign       = 16384
	sizeofLinkeditDataCmd = 16
)

var le = binary.LittleEndian
var be = binary.BigEndian

// slice is one thin Mach-O image, with the layout facts the signer needs.
type slice struct {
	data        []byte
	cputype     uint32
	cpusubtype  uint32
	filetype    uint32
	ncmds       uint32
	sizeofcmds  uint32
	cmdsEnd     int // file offset just past the load commands
	firstSect   int // lowest file offset of any section (load commands must stay below it)
	linkeditOff int // file offset of the __LINKEDIT segment_command_64
	textFileOff uint64
	textFileSz  uint64
	sigCmdOff   int // file offset of LC_CODE_SIGNATURE, or -1
	sigDataOff  uint32
	sigDataSz   uint32
}

func parseSlice(b []byte) (*slice, error) {
	if len(b) < 32 || le.Uint32(b) != mhMagic64 {
		return nil, errors.New("not a 64-bit little-endian Mach-O file")
	}
	s := &slice{data: b, cputype: le.Uint32(b[4:]), cpusubtype: le.Uint32(b[8:]), filetype: le.Uint32(b[12:]),
		ncmds: le.Uint32(b[16:]), sizeofcmds: le.Uint32(b[20:]), linkeditOff: -1, sigCmdOff: -1, firstSect: len(b)}
	off := 32
	for i := uint32(0); i < s.ncmds; i++ {
		if off+8 > len(b) {
			return nil, errors.New("truncated load commands")
		}
		cmd, size := le.Uint32(b[off:]), int(le.Uint32(b[off+4:]))
		if size < 8 || off+size > len(b) {
			return nil, errors.New("bad load command size")
		}
		switch cmd {
		case lcSegment64:
			name := string(bytes.TrimRight(b[off+8:off+24], "\x00"))
			fileoff, filesz := le.Uint64(b[off+40:]), le.Uint64(b[off+48:])
			nsects := int(le.Uint32(b[off+64:]))
			switch name {
			case "__LINKEDIT":
				s.linkeditOff = off
			case "__TEXT":
				s.textFileOff, s.textFileSz = fileoff, filesz
			}
			for j := 0; j < nsects; j++ {
				sec := off + 72 + j*80
				secOff := int(le.Uint32(b[sec+48:]))
				secType := le.Uint32(b[sec+64:]) & 0xff
				if secOff > 0 && secType != 0x1 && secType != 0xc && secType != 0x12 && secOff < s.firstSect { // skip zerofill sections
					s.firstSect = secOff
				}
			}
		case lcCodeSig:
			s.sigCmdOff = off
			s.sigDataOff, s.sigDataSz = le.Uint32(b[off+8:]), le.Uint32(b[off+12:])
		}
		off += size
	}
	s.cmdsEnd = off
	if s.linkeditOff < 0 {
		return nil, errors.New("no __LINKEDIT segment")
	}
	return s, nil
}

func (s *slice) linkedit() (fileoff, filesz uint64) {
	return le.Uint64(s.data[s.linkeditOff+40:]), le.Uint64(s.data[s.linkeditOff+48:])
}

// codeLimit is where signed content ends: the old signature's start, or the end of __LINKEDIT.
func (s *slice) codeLimit() int {
	if s.sigCmdOff >= 0 {
		return int(s.sigDataOff)
	}
	fo, fs := s.linkedit()
	return int(fo + fs)
}

// specials are the hashes that bind the bundle's other files to the executable.
type specials struct {
	info, requirements, resources [32]byte
}

func emptyRequirementSet() []byte {
	b := make([]byte, 12)
	be.PutUint32(b, csMagicRequirements)
	be.PutUint32(b[4:], 12)
	be.PutUint32(b[8:], 0)
	return b
}

func signatureSize(codeLimit int, ident string) int {
	nCode := (codeLimit + pageSize - 1) / pageSize
	cd := cdHeaderSize + len(ident) + 1 + 3*32 + nCode*32
	return 12 + 3*8 + cd + 12 + 8
}

// signSlice returns a new copy of the Mach-O with an ad-hoc signature.
func signSlice(in []byte, ident string, sp specials) ([]byte, error) {
	s, err := parseSlice(in)
	if err != nil {
		return nil, err
	}
	limit := s.codeLimit()
	lfo, lfs := s.linkedit()
	if s.sigCmdOff < 0 && int(lfo+lfs) != len(in) {
		return nil, errors.New("__LINKEDIT is not at the end of the file")
	}
	addCmd := s.sigCmdOff < 0
	if addCmd && s.cmdsEnd+sizeofLinkeditDataCmd > s.firstSect {
		return nil, errors.New("no room in the Mach-O header for a code signature load command")
	}

	sigOff := (limit + 15) &^ 15
	sigSize := signatureSize(sigOff, ident)
	out := make([]byte, sigOff+sigSize)
	copy(out, in[:limit])

	// Header and load commands.
	cmdOff := s.sigCmdOff
	if addCmd {
		cmdOff = s.cmdsEnd
		le.PutUint32(out[16:], s.ncmds+1)
		le.PutUint32(out[20:], s.sizeofcmds+sizeofLinkeditDataCmd)
	}
	le.PutUint32(out[cmdOff:], lcCodeSig)
	le.PutUint32(out[cmdOff+4:], sizeofLinkeditDataCmd)
	le.PutUint32(out[cmdOff+8:], uint32(sigOff))
	le.PutUint32(out[cmdOff+12:], uint32(sigSize))
	newFilesz := uint64(sigOff+sigSize) - lfo
	newVMsz := (newFilesz + linkeditVMAlign - 1) &^ (linkeditVMAlign - 1)
	le.PutUint64(out[s.linkeditOff+48:], newFilesz)
	le.PutUint64(out[s.linkeditOff+32:], newVMsz)

	// Signature: SuperBlob { CodeDirectory, Requirements, empty CMS wrapper }.
	req := emptyRequirementSet()
	nCode := (sigOff + pageSize - 1) / pageSize
	cdLen := cdHeaderSize + len(ident) + 1 + 3*32 + nCode*32
	identOff := cdHeaderSize
	hashOff := identOff + len(ident) + 1 + 3*32

	cd := make([]byte, cdLen)
	be.PutUint32(cd[0:], csMagicCodeDirectory)
	be.PutUint32(cd[4:], uint32(cdLen))
	be.PutUint32(cd[8:], cdVersionExecSeg)
	be.PutUint32(cd[12:], csFlagAdhoc)
	be.PutUint32(cd[16:], uint32(hashOff))
	be.PutUint32(cd[20:], uint32(identOff))
	be.PutUint32(cd[24:], 3) // special slots: Info, Requirements, ResourceDir
	be.PutUint32(cd[28:], uint32(nCode))
	be.PutUint32(cd[32:], uint32(sigOff))
	cd[36] = 32 // hash size
	cd[37] = csHashSHA256
	cd[38] = 0 // platform
	cd[39] = pageShift
	// 40: spare2, 44: scatterOffset, 48: teamOffset, 52: spare3, 56: codeLimit64 (all zero)
	be.PutUint64(cd[64:], s.textFileOff)
	be.PutUint64(cd[72:], s.textFileSz)
	if s.filetype == mhExecute {
		be.PutUint64(cd[80:], csExecSegMainBinary)
	}
	copy(cd[identOff:], ident)
	copy(cd[hashOff-3*32:], sp.resources[:])
	copy(cd[hashOff-2*32:], sp.requirements[:])
	copy(cd[hashOff-1*32:], sp.info[:])
	for i := 0; i < nCode; i++ {
		end := (i + 1) * pageSize
		if end > sigOff {
			end = sigOff
		}
		h := sha256.Sum256(out[i*pageSize : end])
		copy(cd[hashOff+i*32:], h[:])
	}

	wrapper := make([]byte, 8)
	be.PutUint32(wrapper, csMagicBlobWrapper)
	be.PutUint32(wrapper[4:], 8)

	sig := out[sigOff:]
	be.PutUint32(sig[0:], csMagicEmbeddedSig)
	be.PutUint32(sig[4:], uint32(sigSize))
	be.PutUint32(sig[8:], 3)
	p := 12 + 3*8
	for i, blob := range []struct {
		slot uint32
		data []byte
	}{{csSlotCodeDirectory, cd}, {csSlotRequirements, req}, {csSlotSignature, wrapper}} {
		be.PutUint32(sig[12+i*8:], blob.slot)
		be.PutUint32(sig[16+i*8:], uint32(p))
		copy(sig[p:], blob.data)
		p += len(blob.data)
	}
	if p != sigSize {
		return nil, fmt.Errorf("internal: signature size %d, expected %d", p, sigSize)
	}
	return out, nil
}

// thinSlices splits a universal binary (or returns a thin one as-is).
type fatArch struct {
	cputype, cpusubtype, align uint32
	data                       []byte
}

func readSlices(b []byte) ([]fatArch, bool, error) {
	if len(b) >= 4 && be.Uint32(b) == fatMagic {
		n := int(be.Uint32(b[4:]))
		var out []fatArch
		for i := 0; i < n; i++ {
			h := 8 + i*20
			if h+20 > len(b) {
				return nil, true, errors.New("truncated fat header")
			}
			off, size := int(be.Uint32(b[h+8:])), int(be.Uint32(b[h+12:]))
			if off+size > len(b) {
				return nil, true, errors.New("fat slice out of range")
			}
			out = append(out, fatArch{be.Uint32(b[h:]), be.Uint32(b[h+4:]), be.Uint32(b[h+16:]), b[off : off+size]})
		}
		return out, true, nil
	}
	s, err := parseSlice(b)
	if err != nil {
		return nil, false, err
	}
	return []fatArch{{s.cputype, s.cpusubtype, 14, b}}, false, nil
}

func writeFat(archs []fatArch) []byte {
	var out bytes.Buffer
	hdr := make([]byte, 8+20*len(archs))
	be.PutUint32(hdr, fatMagic)
	be.PutUint32(hdr[4:], uint32(len(archs)))
	off := len(hdr)
	type place struct{ off, size int }
	places := make([]place, len(archs))
	for i, a := range archs {
		align := 1 << a.align
		off = (off + align - 1) &^ (align - 1)
		places[i] = place{off, len(a.data)}
		h := 8 + i*20
		be.PutUint32(hdr[h:], a.cputype)
		be.PutUint32(hdr[h+4:], a.cpusubtype)
		be.PutUint32(hdr[h+8:], uint32(off))
		be.PutUint32(hdr[h+12:], uint32(len(a.data)))
		be.PutUint32(hdr[h+16:], a.align)
		off += len(a.data)
	}
	out.Write(hdr)
	for i, a := range archs {
		out.Write(make([]byte, places[i].off-out.Len()))
		out.Write(a.data)
	}
	return out.Bytes()
}

func lipo(outPath string, inputs []string) error {
	var archs []fatArch
	seen := map[uint32]bool{}
	for _, p := range inputs {
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		s, err := parseSlice(b)
		if err != nil {
			return fmt.Errorf("%s: %w", p, err)
		}
		if seen[s.cputype] {
			return fmt.Errorf("%s: duplicate architecture", p)
		}
		seen[s.cputype] = true
		archs = append(archs, fatArch{s.cputype, s.cpusubtype, 14, b})
	}
	return os.WriteFile(outPath, writeFat(archs), 0o755)
}

func archName(cpu uint32) string {
	switch cpu {
	case cpuX86_64:
		return "x86_64"
	case cpuARM64:
		return "arm64"
	}
	return fmt.Sprintf("cpu %#x", cpu)
}
