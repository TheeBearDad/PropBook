package main

// Independent check of a signed bundle. It parses with the standard library's
// debug/macho (not the signer's parser) and recomputes every hash.

import (
	"bytes"
	"crypto/sha256"
	"debug/macho"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
)

func verifyBundle(app string) ([]string, error) {
	b, err := openBundle(app)
	if err != nil {
		return nil, err
	}
	var report []string
	plist, err := os.ReadFile(b.plistPath)
	if err != nil {
		return nil, err
	}
	cr, err := os.ReadFile(filepath.Join(b.contents, "_CodeSignature", "CodeResources"))
	if err != nil {
		return nil, fmt.Errorf("missing _CodeSignature/CodeResources: %w", err)
	}

	// 1. CodeResources must list exactly the bundle's resource files, with matching hashes.
	files, err := b.resourceFiles()
	if err != nil {
		return nil, err
	}
	files2 := regexp.MustCompile(`(?s)<key>files2</key>\s*<dict>(.*?)\n\t</dict>`).FindSubmatch(cr)
	if files2 == nil {
		return nil, errors.New("CodeResources has no files2 section")
	}
	entry := regexp.MustCompile(`(?s)<key>([^<]+)</key>\s*<dict>.*?<key>hash2</key>\s*<data>\s*([A-Za-z0-9+/=]+)\s*</data>`)
	sealed := map[string]string{}
	for _, m := range entry.FindAllSubmatch(files2[1], -1) {
		sealed[string(m[1])] = string(m[2])
	}
	if len(sealed) != len(files) {
		return nil, fmt.Errorf("CodeResources seals %d files, bundle has %d", len(sealed), len(files))
	}
	for _, rel := range files {
		data, err := os.ReadFile(filepath.Join(b.contents, filepath.FromSlash(rel)))
		if err != nil {
			return nil, err
		}
		h := sha256.Sum256(data)
		if sealed[rel] != base64.StdEncoding.EncodeToString(h[:]) {
			return nil, fmt.Errorf("CodeResources hash mismatch for %s", rel)
		}
	}
	report = append(report, fmt.Sprintf("ok   CodeResources seals %d resource file(s)", len(files)))

	// 2. Every architecture slice of the executable.
	exe, err := os.ReadFile(b.exePath)
	if err != nil {
		return nil, err
	}
	type thin struct {
		name string
		data []byte
	}
	var slices []thin
	if ff, err := macho.NewFatFile(bytes.NewReader(exe)); err == nil {
		for _, a := range ff.Arches {
			slices = append(slices, thin{a.Cpu.String(), exe[a.Offset : a.Offset+a.Size]})
		}
	} else if f, err2 := macho.NewFile(bytes.NewReader(exe)); err2 == nil {
		slices = append(slices, thin{f.Cpu.String(), exe})
	} else {
		return nil, fmt.Errorf("executable is not Mach-O: %v", err2)
	}
	want := map[int]([32]byte){csSlotInfo: sha256.Sum256(plist), csSlotResourceDir: sha256.Sum256(cr)}
	for _, t := range slices {
		if err := verifySlice(t.data, b.ident, want); err != nil {
			return report, fmt.Errorf("%s: %w", t.name, err)
		}
		report = append(report, "ok   "+t.name+" slice: page hashes, Info.plist, requirements and CodeResources bindings match")
	}
	return report, nil
}

func verifySlice(data []byte, ident string, want map[int][32]byte) error {
	f, err := macho.NewFile(bytes.NewReader(data))
	if err != nil {
		return err
	}
	var dataoff, datasize uint32
	found := false
	for _, l := range f.Loads {
		raw := l.Raw()
		if f.ByteOrder.Uint32(raw) == lcCodeSig {
			dataoff, datasize = f.ByteOrder.Uint32(raw[8:]), f.ByteOrder.Uint32(raw[12:])
			found = true
		}
	}
	if !found {
		return errors.New("no LC_CODE_SIGNATURE")
	}
	if dataoff%16 != 0 || int(dataoff+datasize) != len(data) {
		return errors.New("signature is not 16-byte aligned at the end of the file")
	}
	le := f.Segment("__LINKEDIT")
	if le == nil || le.Offset+le.Filesz != uint64(dataoff+datasize) || le.Memsz < le.Filesz {
		return errors.New("__LINKEDIT does not cover the signature")
	}
	text := f.Segment("__TEXT")
	sig := data[dataoff:]
	if be.Uint32(sig) != csMagicEmbeddedSig || be.Uint32(sig[4:]) != datasize {
		return errors.New("bad SuperBlob header")
	}
	blobs := map[uint32][]byte{}
	for i := 0; i < int(be.Uint32(sig[8:])); i++ {
		typ, off := be.Uint32(sig[12+i*8:]), be.Uint32(sig[16+i*8:])
		blobs[typ] = sig[off : off+be.Uint32(sig[off+4:])]
	}
	cd, req, wrap := blobs[csSlotCodeDirectory], blobs[csSlotRequirements], blobs[csSlotSignature]
	if cd == nil || req == nil || wrap == nil {
		return errors.New("SuperBlob is missing the CodeDirectory, Requirements or Signature blob")
	}
	if be.Uint32(cd) != csMagicCodeDirectory || be.Uint32(req) != csMagicRequirements || be.Uint32(wrap) != csMagicBlobWrapper {
		return errors.New("bad blob magic")
	}
	if be.Uint32(cd[12:])&csFlagAdhoc == 0 || be.Uint32(cd[12:])&0x20000 != 0 {
		return errors.New("CodeDirectory is not a plain ad-hoc signature")
	}
	identOff := be.Uint32(cd[20:])
	if got := string(cd[identOff : identOff+uint32(bytes.IndexByte(cd[identOff:], 0))]); got != ident {
		return fmt.Errorf("identifier %q, want %q", got, ident)
	}
	hashOff, nSpecial, nCode, limit := be.Uint32(cd[16:]), be.Uint32(cd[24:]), be.Uint32(cd[28:]), be.Uint32(cd[32:])
	if cd[36] != 32 || cd[37] != csHashSHA256 || cd[39] != pageShift {
		return errors.New("unexpected hash type or page size")
	}
	if limit != dataoff {
		return errors.New("code limit does not end at the signature")
	}
	if text == nil || be.Uint64(cd[64:]) != text.Offset || be.Uint64(cd[72:]) != text.Filesz {
		return errors.New("executable segment fields don't match __TEXT")
	}
	if nCode != (limit+pageSize-1)/pageSize {
		return errors.New("wrong number of page hashes")
	}
	for i := uint32(0); i < nCode; i++ {
		end := (i + 1) * pageSize
		if end > limit {
			end = limit
		}
		h := sha256.Sum256(data[i*pageSize : end])
		if !bytes.Equal(cd[hashOff+i*32:hashOff+i*32+32], h[:]) {
			return fmt.Errorf("page %d hash mismatch", i)
		}
	}
	special := func(slot uint32) []byte { return cd[hashOff-slot*32 : hashOff-slot*32+32] }
	if nSpecial < 3 {
		return errors.New("special slots missing")
	}
	rh := sha256.Sum256(req)
	if !bytes.Equal(special(csSlotRequirements), rh[:]) {
		return errors.New("requirements hash mismatch")
	}
	for slot, h := range want {
		if !bytes.Equal(special(uint32(slot)), h[:]) {
			return fmt.Errorf("special slot %d hash mismatch", slot)
		}
	}
	return nil
}
