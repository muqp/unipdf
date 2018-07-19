/*
 * Copyright (c) 2013 Kurt Jung (Gmail: kurt.w.jung)
 *
 * Permission to use, copy, modify, and distribute this software for any
 * purpose with or without fee is hereby granted, provided that the above
 * copyright notice and this permission notice appear in all copies.
 *
 * THE SOFTWARE IS PROVIDED "AS IS" AND THE AUTHOR DISCLAIMS ALL WARRANTIES
 * WITH REGARD TO THIS SOFTWARE INCLUDING ALL IMPLIED WARRANTIES OF
 * MERCHANTABILITY AND FITNESS. IN NO EVENT SHALL THE AUTHOR BE LIABLE FOR
 * ANY SPECIAL, DIRECT, INDIRECT, OR CONSEQUENTIAL DAMAGES OR ANY DAMAGES
 * WHATSOEVER RESULTING FROM LOSS OF USE, DATA OR PROFITS, WHETHER IN AN
 * ACTION OF CONTRACT, NEGLIGENCE OR OTHER TORTIOUS ACTION, ARISING OUT OF
 * OR IN CONNECTION WITH THE USE OR PERFORMANCE OF THIS SOFTWARE.
 */

package fonts

// Utility to parse TTF font files
// Version:    1.0
// Date:       2011-06-18
// Author:     Olivier PLATHEY
// Port to Go: Kurt Jung, 2013-07-15

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strings"

	"github.com/unidoc/unidoc/common"
	"github.com/unidoc/unidoc/pdf/core"
	"github.com/unidoc/unidoc/pdf/model/textencoding"
)

// MakeEncoder returns an encoder built from the tables in  `rec`.
func (rec *TtfType) MakeEncoder() (textencoding.SimpleEncoder, error) {
	encoding := map[uint16]string{}
	for code := uint16(0); code <= 256; code++ {
		gid, ok := rec.Chars[code]
		if !ok {
			continue
		}
		glyph := ""
		if 0 <= gid && int(gid) < len(rec.GlyphNames) {
			glyph = rec.GlyphNames[gid]
		} else {
			glyph = string(rune(gid))
		}
		encoding[code] = glyph
	}
	if len(encoding) == 0 {
		common.Log.Debug("WARNING: Zero length TrueType enconding. rec=%s Chars=[% 02x]",
			rec, rec.Chars)
	}
	return textencoding.NewCustomSimpleTextEncoder(encoding, nil)
}

// TtfType describes a TrueType font file.
// http://scripts.sil.org/cms/scripts/page.php?site_id=nrsi&id=iws-chapter08
type TtfType struct {
	Embeddable             bool
	UnitsPerEm             uint16
	PostScriptName         string
	Bold                   bool
	ItalicAngle            float64
	IsFixedPitch           bool
	TypoAscender           int16
	TypoDescender          int16
	UnderlinePosition      int16
	UnderlineThickness     int16
	Xmin, Ymin, Xmax, Ymax int16
	CapHeight              int16
	Widths                 []uint16

	// Chars maps rune values (unicode) to the indexes in GlyphNames. i.e GlyphNames[Chars[r]] is
	// the glyph corresponding to rune r.
	Chars map[uint16]uint16
	// GlyphNames is a list of glyphs from the "post" section of the TrueType file.
	GlyphNames []string
}

func (ttf *TtfType) String() string {
	return fmt.Sprintf("FONT_FILE2{%#q Embeddable=%t UnitsPerEm=%d Bold=%t ItalicAngle=%f "+
		"CapHeight=%d Chars=%d GlyphNames=%d}",
		ttf.PostScriptName, ttf.Embeddable, ttf.UnitsPerEm, ttf.Bold, ttf.ItalicAngle,
		ttf.CapHeight, len(ttf.Chars), len(ttf.GlyphNames))
}

// ttfParser contains some state variables used to parse a TrueType file.
type ttfParser struct {
	rec              TtfType
	f                io.ReadSeeker
	tables           map[string]uint32
	numberOfHMetrics uint16
	numGlyphs        uint16
}

// NewFontFile2FromPdfObject returns a TtfType describing the TrueType font file in PdfObject `obj`.
func NewFontFile2FromPdfObject(obj core.PdfObject) (rec TtfType, err error) {
	obj = core.TraceToDirectObject(obj)
	streamObj, ok := obj.(*core.PdfObjectStream)
	if !ok {
		common.Log.Debug("ERROR: FontFile2 must be a stream (%T)", obj)
		err = core.ErrTypeError
		return
	}
	data, err := core.DecodeStream(streamObj)
	if err != nil {
		return
	}

	// fmt.Println("===============&&&&===============")
	// fmt.Printf("%#q", string(data))
	// fmt.Println("===============####===============")

	t := ttfParser{f: bytes.NewReader(data)}
	rec, err = t.Parse()
	return
}

// NewFontFile2FromPdfObject returns a TtfType describing the TrueType font file in disk file `fileStr`.
func TtfParse(fileStr string) (rec TtfType, err error) {
	f, err := os.Open(fileStr)
	if err != nil {
		return
	}
	defer f.Close()

	t := ttfParser{f: f}
	rec, err = t.Parse()
	return
}

// NewFontFile2FromPdfObject returns a TtfType describing the TrueType font file in io.Reader `t`.f.
func (t *ttfParser) Parse() (TtfRec TtfType, err error) {

	version, err := t.ReadStr(4)
	if err != nil {
		return
	}
	if version == "OTTO" {
		err = errors.New("fonts based on PostScript outlines are not supported")
		return
	}
	// XXX: !@#$ Not sure what to do here. Have seen version="true"
	if version != "\x00\x01\x00\x00" {
		common.Log.Debug("ERROR: Unrecognized TrueType file format. version=%q", version)
	}
	numTables := int(t.ReadUShort())
	t.Skip(3 * 2) // searchRange, entrySelector, rangeShift
	t.tables = make(map[string]uint32)
	var tag string
	for j := 0; j < numTables; j++ {
		tag, err = t.ReadStr(4)
		if err != nil {
			return
		}
		t.Skip(4) // checkSum
		offset := t.ReadULong()
		t.Skip(4) // length
		t.tables[tag] = offset
	}

	common.Log.Trace(describeTables(t.tables))

	err = t.ParseComponents()
	if err != nil {
		return
	}

	TtfRec = t.rec
	return
}

// describeTables returns a string describing `tables`, the tables in a TrueType font file.
func describeTables(tables map[string]uint32) string {
	tags := []string{}
	for tag := range tables {
		tags = append(tags, tag)
	}
	sort.Slice(tags, func(i, j int) bool { return tables[tags[i]] < tables[tags[j]] })
	parts := []string{fmt.Sprintf("TrueType tables: %d", len(tables))}
	for _, tag := range tags {
		parts = append(parts, fmt.Sprintf("\t%q %5d", tag, tables[tag]))
	}
	return strings.Join(parts, "\n")
}

// ParseComponents parses the tables in a TrueType font file/
// The standard TrueType tables are
// "head"
// "hhea"
// "loca"
// "maxp"
// "cvt "
// "prep"
// "glyf"
// "hmtx"
// "fpgm"
// "gasp"
func (t *ttfParser) ParseComponents() (err error) {

	// Mandatory tables.
	err = t.ParseHead()
	if err != nil {
		return
	}
	err = t.ParseHhea()
	if err != nil {
		return
	}
	err = t.ParseMaxp()
	if err != nil {
		return
	}
	err = t.ParseHmtx()
	if err != nil {
		return
	}

	// Optional tables.
	if _, ok := t.tables["name"]; ok {
		err = t.ParseName()
		if err != nil {
			return
		}
	}
	if _, ok := t.tables["OS/2"]; ok {
		err = t.ParseOS2()
		if err != nil {
			return
		}
	}
	if _, ok := t.tables["post"]; ok {
		err = t.ParsePost()
		if err != nil {
			return
		}
	}
	if _, ok := t.tables["cmap"]; ok {
		err = t.ParseCmap()
		if err != nil {
			return
		}
	}

	return
}

func (t *ttfParser) ParseHead() (err error) {
	err = t.Seek("head")
	t.Skip(3 * 4) // version, fontRevision, checkSumAdjustment
	magicNumber := t.ReadULong()
	if magicNumber != 0x5F0F3CF5 {
		err = fmt.Errorf("incorrect magic number")
		return
	}
	t.Skip(2) // flags
	t.rec.UnitsPerEm = t.ReadUShort()
	t.Skip(2 * 8) // created, modified
	t.rec.Xmin = t.ReadShort()
	t.rec.Ymin = t.ReadShort()
	t.rec.Xmax = t.ReadShort()
	t.rec.Ymax = t.ReadShort()
	return
}

func (t *ttfParser) ParseHhea() (err error) {
	err = t.Seek("hhea")
	if err == nil {
		t.Skip(4 + 15*2)
		t.numberOfHMetrics = t.ReadUShort()
	}
	return
}

func (t *ttfParser) ParseMaxp() (err error) {
	err = t.Seek("maxp")
	if err == nil {
		t.Skip(4)
		t.numGlyphs = t.ReadUShort()
	}
	return
}

func (t *ttfParser) ParseHmtx() (err error) {
	err = t.Seek("hmtx")
	if err == nil {
		t.rec.Widths = make([]uint16, 0, 8)
		for j := uint16(0); j < t.numberOfHMetrics; j++ {
			t.rec.Widths = append(t.rec.Widths, t.ReadUShort())
			t.Skip(2) // lsb
		}
		if t.numberOfHMetrics < t.numGlyphs {
			lastWidth := t.rec.Widths[t.numberOfHMetrics-1]
			for j := t.numberOfHMetrics; j < t.numGlyphs; j++ {
				t.rec.Widths = append(t.rec.Widths, lastWidth)
			}
		}
	}
	return
}

// parseCmapSubtable31 parses information from an (3,1) subtable (Windows Unicode).
func (t *ttfParser) parseCmapSubtable31(offset31 int64) (err error) {
	startCount := make([]uint16, 0, 8)
	endCount := make([]uint16, 0, 8)
	idDelta := make([]int16, 0, 8)
	idRangeOffset := make([]uint16, 0, 8)
	t.rec.Chars = make(map[uint16]uint16)
	t.f.Seek(int64(t.tables["cmap"])+offset31, os.SEEK_SET)
	format := t.ReadUShort()
	if format != 4 {
		err = fmt.Errorf("unexpected subtable format: %d", format)
		return
	}
	t.Skip(2 * 2) // length, language
	segCount := int(t.ReadUShort() / 2)
	t.Skip(3 * 2) // searchRange, entrySelector, rangeShift
	for j := 0; j < segCount; j++ {
		endCount = append(endCount, t.ReadUShort())
	}
	t.Skip(2) // reservedPad
	for j := 0; j < segCount; j++ {
		startCount = append(startCount, t.ReadUShort())
	}
	for j := 0; j < segCount; j++ {
		idDelta = append(idDelta, t.ReadShort())
	}
	offset, _ := t.f.Seek(int64(0), os.SEEK_CUR)
	for j := 0; j < segCount; j++ {
		idRangeOffset = append(idRangeOffset, t.ReadUShort())
	}
	for j := 0; j < segCount; j++ {
		c1 := startCount[j]
		c2 := endCount[j]
		d := idDelta[j]
		ro := idRangeOffset[j]
		if ro > 0 {
			t.f.Seek(offset+2*int64(j)+int64(ro), os.SEEK_SET)
		}
		for c := c1; c <= c2; c++ {
			if c == 0xFFFF {
				break
			}
			var gid int32
			if ro > 0 {
				gid = int32(t.ReadUShort())
				if gid > 0 {
					gid += int32(d)
				}
			} else {
				gid = int32(c) + int32(d)
			}
			if gid >= 65536 {
				gid -= 65536
			}
			if gid > 0 {
				t.rec.Chars[c] = uint16(gid)
			}
		}
	}
	return
}

// parseCmapSubtable10 parses information from an (1,0) subtable (symbol).
func (t *ttfParser) parseCmapSubtable10(offset10 int64) error {

	if t.rec.Chars == nil {
		t.rec.Chars = make(map[uint16]uint16)
	}

	t.f.Seek(int64(t.tables["cmap"])+offset10, os.SEEK_SET)
	var length, language uint32
	format := t.ReadUShort()
	if format < 8 {
		length = uint32(t.ReadUShort())
		language = uint32(t.ReadUShort())
	} else {
		t.ReadUShort()
		length = t.ReadULong()
		language = t.ReadULong()
	}
	common.Log.Debug("parseCmapSubtable10: format=%d length=%d language=%d",
		format, length, language)

	if format != 0 {
		return errors.New("unsupported cmap subtable format")
	}

	dataStr, err := t.ReadStr(256)
	if err != nil {
		return err
	}
	data := []byte(dataStr)

	for code, glyphId := range data {
		t.rec.Chars[uint16(code)] = uint16(glyphId)
		if glyphId != 0 {
			fmt.Printf("\t0x%02x ➞ 0x%02x=%c\n", code, glyphId, rune(glyphId))
		}
	}
	return nil
}

// ParseCmap parses the cmap table in a TrueType font.
func (t *ttfParser) ParseCmap() (err error) {
	var offset int64
	if err = t.Seek("cmap"); err != nil {
		return
	}
	common.Log.Debug("ParseCmap")
	/* version := */ t.ReadUShort()
	numTables := int(t.ReadUShort())
	offset10 := int64(0)
	offset31 := int64(0)
	for j := 0; j < numTables; j++ {
		platformID := t.ReadUShort()
		encodingID := t.ReadUShort()
		offset = int64(t.ReadULong())
		if platformID == 3 && encodingID == 1 {
			// (3,1) subtable. Windows Unicode.
			offset31 = offset
		}
		//fmt.Printf("(%d,%d) subtable @ %d\n", platformID, encodingID, offset)
	}

	// Latin font support based on (3,1) table encoding.
	if offset31 != 0 {
		err = t.parseCmapSubtable31(offset31)
		if err != nil {
			return
		}
	}

	// Many non-Latin fonts (including asian fonts) use subtable (1,0).

	if offset10 != 0 {
		// fmt.Printf("Offset10: %d\n", offset10)
		err = t.parseCmapVersion(offset10)
		if err != nil {
			return
		}
	}

	return
}

func (t *ttfParser) parseCmapVersion(offset int64) error {
	common.Log.Trace("parseCmapVersion: offset=%d", offset)

	if t.rec.Chars == nil {
		t.rec.Chars = make(map[uint16]uint16)
	}

	t.f.Seek(int64(t.tables["cmap"])+offset, os.SEEK_SET)
	var length, language uint32
	format := t.ReadUShort()
	if format < 8 {
		length = uint32(t.ReadUShort())
		language = uint32(t.ReadUShort())
	} else {
		t.ReadUShort()
		length = t.ReadULong()
		language = t.ReadULong()
	}
	common.Log.Debug("parseCmapVersion: format=%d length=%d language=%d",
		format, length, language)

	switch format {
	case 0:
		return t.parseCmapFormat0()
	case 6:
		return t.parseCmapFormat6()
	default:
		common.Log.Debug("ERROR: Unsupported cmap format=%d", format)
		return nil // XXX: Can't return an error here if creator_test.go is to pass.
	}
}

func (t *ttfParser) parseCmapFormat0() error {
	dataStr, err := t.ReadStr(256)
	if err != nil {
		return err
	}
	data := []byte(dataStr)
	common.Log.Trace("parseCmapFormat0: %s\ndataStr=%+q\ndata=[% 02x]", t.rec.String(), dataStr, data)

	for code, glyphId := range data {
		t.rec.Chars[uint16(code)] = uint16(glyphId)
	}
	return nil
}

func (t *ttfParser) parseCmapFormat6() error {

	firstCode := int(t.ReadUShort())
	entryCount := int(t.ReadUShort())

	common.Log.Trace("parseCmapFormat6: %s firstCode=%d entryCount=%d",
		t.rec.String(), firstCode, entryCount)

	for i := 0; i < entryCount; i++ {
		glyphId := t.ReadUShort()
		t.rec.Chars[uint16(i+firstCode)] = glyphId
	}

	return nil
}

func (t *ttfParser) ParseName() (err error) {
	if err = t.Seek("name"); err != nil {
		return
	}
	tableOffset, _ := t.f.Seek(0, os.SEEK_CUR)
	t.rec.PostScriptName = ""
	t.Skip(2) // format
	count := t.ReadUShort()
	stringOffset := t.ReadUShort()
	for j := uint16(0); j < count && t.rec.PostScriptName == ""; j++ {
		t.Skip(3 * 2) // platformID, encodingID, languageID
		nameID := t.ReadUShort()
		length := t.ReadUShort()
		offset := t.ReadUShort()
		if nameID == 6 {
			// PostScript name
			t.f.Seek(int64(tableOffset)+int64(stringOffset)+int64(offset), os.SEEK_SET)
			var s string
			s, err = t.ReadStr(int(length))
			if err != nil {
				return
			}
			s = strings.Replace(s, "\x00", "", -1)
			var re *regexp.Regexp
			if re, err = regexp.Compile("[(){}<> /%[\\]]"); err != nil {
				return
			}
			t.rec.PostScriptName = re.ReplaceAllString(s, "")
		}
	}
	if t.rec.PostScriptName == "" {
		err = fmt.Errorf("the name PostScript was not found")
	}
	return
}

func (t *ttfParser) ParseOS2() (err error) {
	if err = t.Seek("OS/2"); err != nil {
		return
	}
	version := t.ReadUShort()
	t.Skip(3 * 2) // xAvgCharWidth, usWeightClass, usWidthClass
	fsType := t.ReadUShort()
	t.rec.Embeddable = (fsType != 2) && (fsType&0x200) == 0
	t.Skip(11*2 + 10 + 4*4 + 4)
	fsSelection := t.ReadUShort()
	t.rec.Bold = (fsSelection & 32) != 0
	t.Skip(2 * 2) // usFirstCharIndex, usLastCharIndex
	t.rec.TypoAscender = t.ReadShort()
	t.rec.TypoDescender = t.ReadShort()
	if version >= 2 {
		t.Skip(3*2 + 2*4 + 2)
		t.rec.CapHeight = t.ReadShort()
	} else {
		t.rec.CapHeight = 0
	}
	return
}

// ParsePost reads the "post" section in a TrueType font table and sets t.rec.GlyphNames.
func (t *ttfParser) ParsePost() (err error) {
	if err = t.Seek("post"); err != nil {
		return
	}
	//versionUpper := t.ReadShort()
	//versionFraction := t.ReadUShort()

	formatType := t.Read32Fixed()
	t.rec.ItalicAngle = t.Read32Fixed()
	t.rec.UnderlinePosition = t.ReadShort()
	t.rec.UnderlineThickness = t.ReadShort()
	t.rec.IsFixedPitch = t.ReadULong() != 0
	/*minMemType42 := */ t.ReadULong()
	/*maxMemType42 := */ t.ReadULong()
	/*mimMemType1 := */ t.ReadULong()
	/*maxMemType1 := */ t.ReadULong()

	common.Log.Trace("ParsePost: formatType=%f", formatType)

	switch formatType {
	case 1.0: // This font file contains the standard Macintosh TrueTyp 258 glyphs.
		t.rec.GlyphNames = macGlyphNames
	case 2.0:
		numGlyphs := int(t.ReadUShort())
		glyphNameIndex := make([]int, numGlyphs)
		t.rec.GlyphNames = make([]string, numGlyphs)
		maxIndex := -1
		for i := 0; i < numGlyphs; i++ {
			index := int(t.ReadUShort())
			glyphNameIndex[i] = index
			// Index numbers between 0x7fff and 0xffff are reserved for future use
			if index <= 0x7fff && index > maxIndex {
				maxIndex = index
			}
		}
		var nameArray []string
		if maxIndex >= len(macGlyphNames) {
			nameArray = make([]string, maxIndex-len(macGlyphNames)+1)
			for i := 0; i < maxIndex-len(macGlyphNames)+1; i++ {
				numberOfChars := int(t.ReadByte())
				names, err := t.ReadStr(numberOfChars)
				if err != nil {
					return err
				}
				nameArray[i] = names
			}
		}
		for i := 0; i < numGlyphs; i++ {
			index := glyphNameIndex[i]
			if index < len(macGlyphNames) {
				t.rec.GlyphNames[i] = macGlyphNames[index]
			} else if index >= len(macGlyphNames) && index <= 32767 {
				t.rec.GlyphNames[i] = nameArray[index-len(macGlyphNames)]
			} else {
				// PDFBOX-808: Index numbers between 32768 and 65535 are
				// reserved for future use, so we should just ignore them
				t.rec.GlyphNames[i] = ".undefined"
			}
		}
	case 2.5:
		glyphNameIndex := make([]int, t.numGlyphs) // !@#$ Check that this is parsed first
		for i := 0; i < len(glyphNameIndex); i++ {
			offset := int(t.ReadSByte())
			glyphNameIndex[i] = i + 1 + offset
		}
		t.rec.GlyphNames = make([]string, len(glyphNameIndex))
		for i := 0; i < len(t.rec.GlyphNames); i++ {
			name := macGlyphNames[glyphNameIndex[i]]
			t.rec.GlyphNames[i] = name
		}
	case 3.0:
		// no postscript information is provided.
		common.Log.Debug("No PostScript name information is provided for the font.")
	default:
		common.Log.Debug("ERROR: Unknown formatType=%f", formatType)
	}

	return
}

// The 258 standard mac glyph names used in 'post' format 1 and 2.
var macGlyphNames = []string{
	".notdef", ".null", "nonmarkingreturn", "space", "exclam", "quotedbl",
	"numbersign", "dollar", "percent", "ampersand", "quotesingle",
	"parenleft", "parenright", "asterisk", "plus", "comma", "hyphen",
	"period", "slash", "zero", "one", "two", "three", "four", "five",
	"six", "seven", "eight", "nine", "colon", "semicolon", "less",
	"equal", "greater", "question", "at", "A", "B", "C", "D", "E", "F",
	"G", "H", "I", "J", "K", "L", "M", "N", "O", "P", "Q", "R", "S",
	"T", "U", "V", "W", "X", "Y", "Z", "bracketleft", "backslash",
	"bracketright", "asciicircum", "underscore", "grave", "a", "b",
	"c", "d", "e", "f", "g", "h", "i", "j", "k", "l", "m", "n", "o",
	"p", "q", "r", "s", "t", "u", "v", "w", "x", "y", "z", "braceleft",
	"bar", "braceright", "asciitilde", "Adieresis", "Aring",
	"Ccedilla", "Eacute", "Ntilde", "Odieresis", "Udieresis", "aacute",
	"agrave", "acircumflex", "adieresis", "atilde", "aring",
	"ccedilla", "eacute", "egrave", "ecircumflex", "edieresis",
	"iacute", "igrave", "icircumflex", "idieresis", "ntilde", "oacute",
	"ograve", "ocircumflex", "odieresis", "otilde", "uacute", "ugrave",
	"ucircumflex", "udieresis", "dagger", "degree", "cent", "sterling",
	"section", "bullet", "paragraph", "germandbls", "registered",
	"copyright", "trademark", "acute", "dieresis", "notequal", "AE",
	"Oslash", "infinity", "plusminus", "lessequal", "greaterequal",
	"yen", "mu", "partialdiff", "summation", "product", "pi",
	"integral", "ordfeminine", "ordmasculine", "Omega", "ae", "oslash",
	"questiondown", "exclamdown", "logicalnot", "radical", "florin",
	"approxequal", "Delta", "guillemotleft", "guillemotright",
	"ellipsis", "nonbreakingspace", "Agrave", "Atilde", "Otilde", "OE",
	"oe", "endash", "emdash", "quotedblleft", "quotedblright",
	"quoteleft", "quoteright", "divide", "lozenge", "ydieresis",
	"Ydieresis", "fraction", "currency", "guilsinglleft",
	"guilsinglright", "fi", "fl", "daggerdbl", "periodcentered",
	"quotesinglbase", "quotedblbase", "perthousand", "Acircumflex",
	"Ecircumflex", "Aacute", "Edieresis", "Egrave", "Iacute",
	"Icircumflex", "Idieresis", "Igrave", "Oacute", "Ocircumflex",
	"apple", "Ograve", "Uacute", "Ucircumflex", "Ugrave", "dotlessi",
	"circumflex", "tilde", "macron", "breve", "dotaccent", "ring",
	"cedilla", "hungarumlaut", "ogonek", "caron", "Lslash", "lslash",
	"Scaron", "scaron", "Zcaron", "zcaron", "brokenbar", "Eth", "eth",
	"Yacute", "yacute", "Thorn", "thorn", "minus", "multiply",
	"onesuperior", "twosuperior", "threesuperior", "onehalf",
	"onequarter", "threequarters", "franc", "Gbreve", "gbreve",
	"Idotaccent", "Scedilla", "scedilla", "Cacute", "cacute", "Ccaron",
	"ccaron", "dcroat",
}

// Seek moves the file pointer to the table named `tag`.
func (t *ttfParser) Seek(tag string) error {
	ofs, ok := t.tables[tag]
	if !ok {
		return fmt.Errorf("table not found: %s", tag)
	}
	t.f.Seek(int64(ofs), os.SEEK_SET)
	return nil
}

func (t *ttfParser) Skip(n int) {
	t.f.Seek(int64(n), os.SEEK_CUR)
}

func (t *ttfParser) ReadStr(length int) (str string, err error) {
	var n int
	buf := make([]byte, length)
	n, err = t.f.Read(buf)
	if err != nil {
		return
	}
	if n == length {
		str = string(buf)
	} else {
		err = fmt.Errorf("unable to read %d bytes", length)
	}
	return
}

func (t *ttfParser) ReadByte() (val uint8) {
	binary.Read(t.f, binary.BigEndian, &val)
	return
}

func (t *ttfParser) ReadSByte() (val int8) {
	binary.Read(t.f, binary.BigEndian, &val)
	return
}

func (t *ttfParser) ReadUShort() (val uint16) {
	binary.Read(t.f, binary.BigEndian, &val)
	return
}

func (t *ttfParser) ReadShort() (val int16) {
	binary.Read(t.f, binary.BigEndian, &val)
	return
}

func (t *ttfParser) ReadULong() (val uint32) {
	binary.Read(t.f, binary.BigEndian, &val)
	return
}

func (t *ttfParser) Read32Fixed() float64 {
	whole := float64(t.ReadUShort())
	frac := float64(t.ReadUShort()) / 65536.0
	return whole + frac
}
