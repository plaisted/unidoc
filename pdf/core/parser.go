/*
 * This file is subject to the terms and conditions defined in
 * file 'LICENSE.md', which is part of this source code package.
 */

package core

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/unidoc/unidoc/common"
)

// Regular Expressions for parsing and identifying object signatures.
var rePdfVersion = regexp.MustCompile(`%PDF-(\d)\.(\d)`)
var reEOF = regexp.MustCompile("%%EOF")
var reXrefTable = regexp.MustCompile(`\s*xref\s*`)
var reStartXref = regexp.MustCompile(`startx?ref\s*(\d+)`)
var reNumeric = regexp.MustCompile(`^[\+-.]*([0-9.]+)`)
var reExponential = regexp.MustCompile(`^[\+-.]*([0-9.]+)e[\+-.]*([0-9.]+)`)
var reReference = regexp.MustCompile(`^\s*(\d+)\s+(\d+)\s+R`)
var reIndirectObject = regexp.MustCompile(`(\d+)\s+(\d+)\s+obj`)
var reXrefSubsection = regexp.MustCompile(`(\d+)\s+(\d+)\s*$`)
var reXrefEntry = regexp.MustCompile(`(\d+)\s+(\d+)\s+([nf])\s*$`)

// PdfParser parses a PDF file and provides access to the object structure of the PDF.
type PdfParser struct {
	majorVersion int
	minorVersion int

	rs               io.ReadSeeker
	rsMut            sync.Mutex
	reader           *bufio.Reader
	fileSize         int64
	xrefs            XrefTable
	xrefMut          sync.Mutex
	objstms          ObjectStreams
	objstmsMut       sync.Mutex
	trailer          *PdfObjectDictionary
	objCache         ObjectCache
	objCacheMut      sync.Mutex
	crypter          *PdfCrypt
	repairsAttempted bool // Avoid multiple attempts for repair.
	repairsMut       sync.Mutex

	// Tracker for reference lookups when looking up Length entry of stream objects.
	// The Length entries of stream objects are a special case, as they can require recursive parsing, i.e. look up
	// the length reference (if not object) prior to reading the actual stream.  This has risks of endless looping.
	// Tracking is necessary to avoid recursive loops.
	streamLengthReferenceLookupInProgress map[int64]bool
}

// GetCrypter returns the PdfCrypt instance which has information about the PDFs encryption.
func (parser *PdfParser) GetCrypter() *PdfCrypt {
	return parser.crypter
}

// IsAuthenticated returns true if the PDF has already been authenticated for accessing.
func (parser *PdfParser) IsAuthenticated() bool {
	return parser.crypter.Authenticated
}

// GetTrailer returns the PDFs trailer dictionary. The trailer dictionary is typically the starting point for a PDF,
// referencing other key objects that are important in the document structure.
func (parser *PdfParser) GetTrailer() *PdfObjectDictionary {
	return parser.trailer
}

// Parse the pdf version from the beginning of the file.
// Returns the major and minor parts of the version.
// E.g. for "PDF-1.7" would return 1 and 7.
func (parser *PdfParser) parsePdfVersion() (int, int, error) {
	parser.rsMut.Lock()
	parser.rs.Seek(0, os.SEEK_SET)
	var offset int64 = 20
	b := make([]byte, offset)
	parser.rs.Read(b)
	parser.rsMut.Unlock()

	result1 := rePdfVersion.FindStringSubmatch(string(b))
	if len(result1) < 3 {
		major, minor, err := parser.seekPdfVersionTopDown()
		if err != nil {
			common.Log.Debug("Failed recovery - unable to find version")
			return 0, 0, err
		}

		return major, minor, nil
	}

	majorVersion, err := strconv.ParseInt(result1[1], 10, 64)
	if err != nil {
		return 0, 0, err
	}

	minorVersion, err := strconv.ParseInt(result1[2], 10, 64)
	if err != nil {
		return 0, 0, err
	}

	//version, _ := strconv.Atoi(result1[1])
	common.Log.Debug("Pdf version %d.%d", majorVersion, minorVersion)

	return int(majorVersion), int(minorVersion), nil
}

// Conventional xref table starting with 'xref'.
// NOT THREAD SAFE
func (parser *PdfParser) parseXrefTable() (*PdfObjectDictionary, error) {
	var trailer *PdfObjectDictionary

	txt, err := readTextLine(parser.reader)
	if err != nil {
		return nil, err
	}

	common.Log.Trace("xref first line: %s", txt)
	curObjNum := -1
	secObjects := 0
	insideSubsection := false
	for {
		skipSpaces(parser.reader)
		_, err := parser.reader.Peek(1)
		if err != nil {
			return nil, err
		}

		txt, err = readTextLine(parser.reader)
		if err != nil {
			return nil, err
		}

		result1 := reXrefSubsection.FindStringSubmatch(txt)
		if len(result1) == 3 {
			// Match
			first, _ := strconv.Atoi(result1[1])
			second, _ := strconv.Atoi(result1[2])
			curObjNum = first
			secObjects = second
			insideSubsection = true
			common.Log.Trace("xref subsection: first object: %d objects: %d", curObjNum, secObjects)
			continue
		}
		result2 := reXrefEntry.FindStringSubmatch(txt)
		if len(result2) == 4 {
			if insideSubsection == false {
				common.Log.Debug("ERROR Xref invalid format!\n")
				return nil, errors.New("Xref invalid format")
			}

			first, _ := strconv.ParseInt(result2[1], 10, 64)
			gen, _ := strconv.Atoi(result2[2])
			third := result2[3]

			if strings.ToLower(third) == "n" && first > 1 {
				// Object in use in the file!  Load it.
				// Ignore free objects ('f').
				//
				// Some malformed writers mark the offset as 0 to
				// indicate that the object is free, and still mark as 'n'
				// Fairly safe to assume is free if offset is 0.
				//
				// Some malformed writers even seem to have values such as
				// 1.. Assume null object for those also. That is referring
				// to within the PDF version in the header clearly.
				//
				// Load if not existing or higher generation number than previous.
				// Usually should not happen, lower generation numbers
				// would be marked as free.  But can still happen!
				x, ok := parser.xrefs[curObjNum]
				if !ok || gen > x.generation {
					obj := XrefObject{objectNumber: curObjNum,
						xtype:  XREF_TABLE_ENTRY,
						offset: first, generation: gen}
					parser.xrefs[curObjNum] = obj
				}
			}

			curObjNum++
			continue
		}
		if (len(txt) > 6) && (txt[:7] == "trailer") {
			common.Log.Trace("Found trailer - %s", txt)
			// Sometimes get "trailer << ...."
			// Need to rewind to end of trailer text.
			if len(txt) > 9 {
				offset := parser.GetFileOffset()
				parser.SetFileOffset(offset - int64(len(txt)) + 7)
			}

			skipSpaces(parser.reader)
			skipComments(parser.reader)
			common.Log.Trace("Reading trailer dict!")
			common.Log.Trace("peek: \"%s\"", txt)
			trailer, err = ParseDict(parser.reader)
			common.Log.Trace("EOF reading trailer dict!")
			if err != nil {
				common.Log.Debug("Error parsing trailer dict (%s)", err)
				return nil, err
			}
			break
		}

		if txt == "%%EOF" {
			common.Log.Debug("ERROR: end of file - trailer not found - error!")
			return nil, errors.New("End of file - trailer not found")
		}

		common.Log.Trace("xref more : %s", txt)
	}
	common.Log.Trace("EOF parsing xref table!")

	return trailer, nil
}

// Load the cross references from an xref stream object (XRefStm).
// Also load the dictionary information (trailer dictionary).
// NOT THREAD SAFE
func (parser *PdfParser) parseXrefStream(xstm *PdfObjectInteger) (*PdfObjectDictionary, error) {
	// TS: parser.rsMut.Lock()
	if xstm != nil {
		common.Log.Trace("XRefStm xref table object at %d", xstm)
		parser.rs.Seek(int64(*xstm), os.SEEK_SET)
		parser.reader = bufio.NewReader(parser.rs)
	}

	xrefObj, err := ParseIndirectObject(parser.reader)
	// TS: parser.rsMut.Unlock()
	if err != nil {
		common.Log.Debug("ERROR: Failed to read xref object")
		return nil, errors.New("Failed to read xref object")
	}

	common.Log.Trace("XRefStm object: %s", xrefObj)
	xs, ok := xrefObj.(*PdfObjectStream)
	if !ok {
		common.Log.Debug("ERROR: XRefStm pointing to non-stream object!")
		return nil, errors.New("XRefStm pointing to a non-stream object")
	}

	trailerDict := xs.PdfObjectDictionary

	sizeObj, ok := xs.PdfObjectDictionary.Get("Size").(*PdfObjectInteger)
	if !ok {
		common.Log.Debug("ERROR: Missing size from xref stm")
		return nil, errors.New("Missing Size from xref stm")
	}
	// Sanity check to avoid DoS attacks. Maximum number of indirect objects on 32 bit system.
	if int64(*sizeObj) > 8388607 {
		common.Log.Debug("ERROR: xref Size exceeded limit, over 8388607 (%d)", *sizeObj)
		return nil, errors.New("Range check error")
	}

	wObj := xs.PdfObjectDictionary.Get("W")
	wArr, ok := wObj.(*PdfObjectArray)
	if !ok {
		return nil, errors.New("Invalid W in xref stream")
	}

	wLen := len(*wArr)
	if wLen != 3 {
		common.Log.Debug("ERROR: Unsupported xref stm (len(W) != 3 - %d)", wLen)
		return nil, errors.New("Unsupported xref stm len(W) != 3")
	}

	var b []int64
	for i := 0; i < 3; i++ {
		w, ok := (*wArr)[i].(PdfObject)
		if !ok {
			return nil, errors.New("Invalid W")
		}
		wVal, ok := w.(*PdfObjectInteger)
		if !ok {
			return nil, errors.New("Invalid w object type")
		}

		b = append(b, int64(*wVal))
	}

	ds, err := DecodeStream(xs)
	if err != nil {
		common.Log.Debug("ERROR: Unable to decode stream: %v", err)
		return nil, err
	}

	s0 := int(b[0])
	s1 := int(b[0] + b[1])
	s2 := int(b[0] + b[1] + b[2])
	deltab := int(b[0] + b[1] + b[2])

	if s0 < 0 || s1 < 0 || s2 < 0 {
		common.Log.Debug("Error s value < 0 (%d,%d,%d)", s0, s1, s2)
		return nil, errors.New("Range check error")
	}
	if deltab == 0 {
		common.Log.Debug("No xref objects in stream (deltab == 0)")
		return trailerDict, nil
	}

	// Calculate expected entries.
	entries := len(ds) / deltab

	// Get the object indices.

	objCount := 0
	indexObj := xs.PdfObjectDictionary.Get("Index")
	// Table 17 (7.5.8.2 Cross-Reference Stream Dictionary)
	// (Optional) An array containing a pair of integers for each
	// subsection in this section. The first integer shall be the first
	// object number in the subsection; the second integer shall be the
	// number of entries in the subsection.
	// The array shall be sorted in ascending order by object number.
	// Subsections cannot overlap; an object number may have at most
	// one entry in a section.
	// Default value: [0 Size].
	indexList := []int{}
	if indexObj != nil {
		common.Log.Trace("Index: %b", indexObj)
		indicesArray, ok := indexObj.(*PdfObjectArray)
		if !ok {
			common.Log.Debug("Invalid Index object (should be an array)")
			return nil, errors.New("Invalid Index object")
		}

		// Expect indLen to be a multiple of 2.
		if len(*indicesArray)%2 != 0 {
			common.Log.Debug("WARNING Failure loading xref stm index not multiple of 2.")
			return nil, errors.New("Range check error")
		}

		objCount = 0

		indices, err := indicesArray.ToIntegerArray()
		if err != nil {
			common.Log.Debug("Error getting index array as integers: %v", err)
			return nil, err
		}

		for i := 0; i < len(indices); i += 2 {
			// add the indices to the list..

			startIdx := indices[i]
			numObjs := indices[i+1]
			for j := 0; j < numObjs; j++ {
				indexList = append(indexList, startIdx+j)
			}
			objCount += numObjs
		}
	} else {
		// If no Index, then assume [0 Size]
		for i := 0; i < int(*sizeObj); i++ {
			indexList = append(indexList, i)
		}
		objCount = int(*sizeObj)
	}

	if entries == objCount+1 {
		// For compatibility, expand the object count.
		common.Log.Debug("BAD file: allowing compatibility (append one object to xref stm)")
		indexList = append(indexList, objCount)
		objCount++
	}

	if entries != len(indexList) {
		// If mismatch -> error (already allowing mismatch of 1 if Index not specified).
		common.Log.Debug("ERROR: xref stm: num entries != len(indices) (%d != %d)", entries, len(indexList))
		return nil, errors.New("Xref stm num entries != len(indices)")
	}

	common.Log.Trace("Objects count %d", objCount)
	common.Log.Trace("Indices: % d", indexList)

	// Convert byte array to a larger integer, little-endian.
	convertBytes := func(v []byte) int64 {
		var tmp int64 = 0
		for i := 0; i < len(v); i++ {
			tmp += int64(v[i]) * (1 << uint(8*(len(v)-i-1)))
		}
		return tmp
	}

	common.Log.Trace("Decoded stream length: %d", len(ds))
	objIndex := 0
	for i := 0; i < len(ds); i += deltab {
		err := checkBounds(len(ds), i, i+s0)
		if err != nil {
			common.Log.Debug("Invalid slice range: %v", err)
			return nil, err
		}
		p1 := ds[i : i+s0]

		err = checkBounds(len(ds), i+s0, i+s1)
		if err != nil {
			common.Log.Debug("Invalid slice range: %v", err)
			return nil, err
		}
		p2 := ds[i+s0 : i+s1]

		err = checkBounds(len(ds), i+s1, i+s2)
		if err != nil {
			common.Log.Debug("Invalid slice range: %v", err)
			return nil, err
		}
		p3 := ds[i+s1 : i+s2]

		ftype := convertBytes(p1)
		n2 := convertBytes(p2)
		n3 := convertBytes(p3)

		if b[0] == 0 {
			// If first entry in W is 0, then default to to type 1.
			// (uncompressed object via offset).
			ftype = 1
		}

		if objIndex >= len(indexList) {
			common.Log.Debug("XRef stream - Trying to access index out of bounds - breaking")
			break
		}
		objNum := indexList[objIndex]
		objIndex++

		common.Log.Trace("%d. p1: % x", objNum, p1)
		common.Log.Trace("%d. p2: % x", objNum, p2)
		common.Log.Trace("%d. p3: % x", objNum, p3)

		common.Log.Trace("%d. xref: %d %d %d", objNum, ftype, n2, n3)
		if ftype == 0 {
			common.Log.Trace("- Free object - can probably ignore")
		} else if ftype == 1 {
			common.Log.Trace("- In use - uncompressed via offset %b", p2)
			// Object type 1: Objects that are in use but are not
			// compressed, i.e. defined by an offset (normal entry)
			if xr, ok := parser.xrefs[objNum]; !ok || int(n3) > xr.generation {
				// Only overload if not already loaded!
				// or has a newer generation number. (should not happen)
				obj := XrefObject{objectNumber: objNum,
					xtype: XREF_TABLE_ENTRY, offset: n2, generation: int(n3)}
				parser.xrefs[objNum] = obj
			}
		} else if ftype == 2 {
			// Object type 2: Compressed object.
			common.Log.Trace("- In use - compressed object")
			if _, ok := parser.xrefs[objNum]; !ok {
				obj := XrefObject{objectNumber: objNum,
					xtype: XREF_OBJECT_STREAM, osObjNumber: int(n2), osObjIndex: int(n3)}
				parser.xrefs[objNum] = obj
				common.Log.Trace("entry: %s", parser.xrefs[objNum])
			}
		} else {
			common.Log.Debug("ERROR: --------INVALID TYPE XrefStm invalid?-------")
			// Continue, we do not define anything -> null object.
			// 7.5.8.3:
			//
			// In PDF 1.5 through PDF 1.7, only types 0, 1, and 2 are
			// allowed. Any other value shall be interpreted as a
			// reference to the null object, thus permitting new entry
			// types to be defined in the future.
			continue
		}
	}

	return trailerDict, nil
}

// Parse xref table at the current file position.  Can either be a
// standard xref table, or an xref stream.
// NOT THREAD SAFE
func (parser *PdfParser) parseXref() (*PdfObjectDictionary, error) {
	var err error
	var trailerDict *PdfObjectDictionary

	// Points to xref table or xref stream object?
	bb, _ := parser.reader.Peek(20)
	if reIndirectObject.MatchString(string(bb)) {
		common.Log.Trace("xref points to an object.  Probably xref object")
		common.Log.Trace("starting with \"%s\"", string(bb))
		trailerDict, err = parser.parseXrefStream(nil)
		if err != nil {
			return nil, err
		}
	} else if reXrefTable.MatchString(string(bb)) {
		common.Log.Trace("Standard xref section table!")
		var err error
		trailerDict, err = parser.parseXrefTable()
		if err != nil {
			return nil, err
		}
	} else {
		common.Log.Debug("Warning: Unable to find xref table or stream. Repair attempted: Looking for earliest xref from bottom.")
		err := parser.repairSeekXrefMarker()
		if err != nil {
			common.Log.Debug("Repair failed - %v", err)
			return nil, err
		}

		trailerDict, err = parser.parseXrefTable()
		if err != nil {
			return nil, err
		}
	}

	return trailerDict, err
}

// Look for EOF marker and seek to its beginning.
// Define an offset position from the end of the file.
// NOT THREAD SAFE
func seekToEOFMarker(rs io.ReadSeeker, fSize int64) error {
	// Define the starting point (from the end of the file) to search from.
	var offset int64 = 0

	// Define an buffer length in terms of how many bytes to read from the end of the file.
	var buflen int64 = 1000

	for offset < fSize {
		if fSize <= (buflen + offset) {
			buflen = fSize - offset
		}

		// Move back enough (as we need to read forward).
		_, err := rs.Seek(-offset-buflen, io.SeekEnd)
		if err != nil {
			return err
		}

		// Read the data.
		b1 := make([]byte, buflen)
		rs.Read(b1)
		common.Log.Trace("Looking for EOF marker: \"%s\"", string(b1))
		ind := reEOF.FindAllStringIndex(string(b1), -1)
		if ind != nil {
			// Found it.
			lastInd := ind[len(ind)-1]
			common.Log.Trace("Ind: % d", ind)
			rs.Seek(-offset-buflen+int64(lastInd[0]), io.SeekEnd)
			return nil
		} else {
			common.Log.Debug("Warning: EOF marker not found! - continue seeking")
		}

		offset += buflen
	}

	common.Log.Debug("Error: EOF marker was not found.")
	return errors.New("EOF not found")
}

//
// Load the xrefs from the bottom of file prior to parsing the file.
// 1. Look for %%EOF marker, then
// 2. Move up to find startxref
// 3. Then move to that position (slight offset)
// 4. Move until find "startxref"
// 5. Load the xref position
// 6. Move to the xref position and parse it.
// 7. Load each xref into a table.
//
// Multiple xref table handling:
// 1. Check main xref table (primary)
// 2. Check the Xref stream object (PDF >=1.5)
// 3. Check the Prev xref
// 4. Continue looking for Prev until not found.
//
// The earlier xrefs have higher precedence.  If objects already
// loaded will ignore older versions.
//
// NOT THREAD SAFE
func (parser *PdfParser) loadXrefs() (*PdfObjectDictionary, error) {
	// TS: parser.xrefMut.Lock()
	// TS: defer parser.xrefMut.Unlock()
	// TS: parser.rsMut.Lock()
	// TS: defer parser.rsMut.Unlock()

	parser.xrefs = make(XrefTable)
	parser.objstms = make(map[int]ObjectStream)

	// Get the file size.
	fSize, err := parser.rs.Seek(0, io.SeekEnd)
	if err != nil {
		return nil, err
	}
	common.Log.Trace("fsize: %d", fSize)
	parser.fileSize = fSize

	// Seek the EOF marker.
	err = seekToEOFMarker(parser.rs, fSize)
	if err != nil {
		common.Log.Debug("Failed seek to eof marker: %v", err)
		return nil, err
	}

	// Look for startxref and get the xref offset.
	curOffset, err := parser.rs.Seek(0, io.SeekCurrent)
	if err != nil {
		return nil, err
	}

	// Seek 64 bytes (numBytes) back from EOF marker start.
	var numBytes int64 = 64
	offset := curOffset - numBytes
	if offset < 0 {
		offset = 0
	}
	_, err = parser.rs.Seek(offset, io.SeekStart)
	if err != nil {
		return nil, err
	}

	b2 := make([]byte, numBytes)
	_, err = parser.rs.Read(b2)
	if err != nil {
		common.Log.Debug("Failed reading while looking for startxref: %v", err)
		return nil, err
	}

	result := reStartXref.FindStringSubmatch(string(b2))
	if len(result) < 2 {
		common.Log.Debug("Error: startxref not found!")
		return nil, errors.New("Startxref not found")
	}
	if len(result) > 2 {
		common.Log.Debug("ERROR: Multiple startxref (%s)!", b2)
		return nil, errors.New("Multiple startxref entries?")
	}
	offsetXref, _ := strconv.ParseInt(result[1], 10, 64)
	common.Log.Trace("startxref at %d", offsetXref)

	if offsetXref > fSize {
		common.Log.Debug("ERROR: Xref offset outside of file")
		common.Log.Debug("Attempting repair")
		offsetXref, err = repairLocateXref(parser.rs)
		if err != nil {
			common.Log.Debug("ERROR: Repair attempt failed (%s)")
			return nil, err
		}
	}
	// Read the xref.
	parser.rs.Seek(int64(offsetXref), io.SeekStart)
	parser.reader = bufio.NewReader(parser.rs)

	trailerDict, err := parser.parseXref()
	if err != nil {
		return nil, err
	}

	// Check the XrefStm object also from the trailer.
	xx := trailerDict.Get("XRefStm")
	if xx != nil {
		xo, ok := xx.(*PdfObjectInteger)
		if !ok {
			return nil, errors.New("XRefStm != int")
		}
		_, err = parser.parseXrefStream(xo)
		if err != nil {
			return nil, err
		}
	}

	// Load old objects also.  Only if not already specified.
	prevList := []int64{}
	intInSlice := func(val int64, list []int64) bool {
		for _, b := range list {
			if b == val {
				return true
			}
		}
		return false
	}

	// Load any Previous xref tables (old versions), which can
	// refer to objects also.
	xx = trailerDict.Get("Prev")
	for xx != nil {
		prevInt, ok := xx.(*PdfObjectInteger)
		if !ok {
			// For compatibility: If Prev is invalid, just go with whatever xrefs are loaded already.
			// i.e. not returning an error.  A debug message is logged.
			common.Log.Debug("Invalid Prev reference: Not a *PdfObjectInteger (%T)", xx)
			return trailerDict, nil
		}

		off := *prevInt
		common.Log.Trace("Another Prev xref table object at %d", off)

		// Can be either regular table, or an xref object...
		parser.rs.Seek(int64(off), os.SEEK_SET)
		parser.reader = bufio.NewReader(parser.rs)

		ptrailerDict, err := parser.parseXref()
		if err != nil {
			common.Log.Debug("Warning: Error - Failed loading another (Prev) trailer")
			common.Log.Debug("Attempting to continue by ignoring it")
			break
		}

		xx = ptrailerDict.Get("Prev")
		if xx != nil {
			prevoff := *(xx.(*PdfObjectInteger))
			if intInSlice(int64(prevoff), prevList) {
				// Prevent circular reference!
				common.Log.Debug("Preventing circular xref referencing")
				break
			}
			prevList = append(prevList, int64(prevoff))
		}
	}

	return trailerDict, nil
}

// Return the closest object following offset from the xrefs table.
func (parser *PdfParser) xrefNextObjectOffset(offset int64) int64 {
	nextOffset := int64(0)
	for _, xref := range parser.xrefs {
		if xref.offset > offset && (xref.offset < nextOffset || nextOffset == 0) {
			nextOffset = xref.offset
		}
	}
	return nextOffset
}

func (parser *PdfParser) validateStreamLength(obj PdfObject) error {
	// Special stream length tracing function used to avoid endless recursive looping.
	stream, ok := obj.(*PdfObjectStream)
	if !ok {
		return errors.New("Validate stream called on non-PdfObjectStream")
	}

	slo, err := parser.traceStreamLength(stream.PdfObjectDictionary.Get("Length"))
	if err != nil {
		common.Log.Debug("Fail to trace stream length: %v", err)
		return err
	}
	common.Log.Trace("Stream length? %s", slo) //
	pstreamLength, ok := slo.(*PdfObjectInteger)
	if !ok {
		return errors.New("Stream length needs to be an integer")
	}
	streamLength := *pstreamLength
	if streamLength < 0 {
		return errors.New("Stream needs to be longer than 0")
	}

	if len(stream.Stream) != int(streamLength) {
		return errors.New("Stream length does not match actual size")
	}
	return nil
	//
	// // Validate the stream length based on the cross references.
	// // Find next object with closest offset to current object and calculate
	// // the expected stream length based on that.
	// streamStartOffset := fileParser.GetFileOffset()
	// nextObjectOffset := fileParser.xrefNextObjectOffset(streamStartOffset)
	// if streamStartOffset+int64(streamLength) > nextObjectOffset && nextObjectOffset > streamStartOffset {
	// 	common.Log.Debug("Expected ending at %d", streamStartOffset+int64(streamLength))
	// 	common.Log.Debug("Next object starting at %d", nextObjectOffset)
	// 	// endstream + "\n" endobj + "\n" (17)
	// 	newLength := nextObjectOffset - streamStartOffset - 17
	// 	if newLength < 0 {
	// 		return errors.New("Invalid stream length, going past boundaries")
	// 	}
	//
	// 	common.Log.Debug("Attempting a length correction to %d...", newLength)
	// 	streamLength = PdfObjectInteger(newLength)
	// 	dict.Set("Length", MakeInteger(newLength))
	// }
	//
	// // Make sure is less than actual file size.
	// if int64(streamLength) > fileParser.fileSize {
	// 	common.Log.Debug("ERROR: Stream length cannot be larger than file size")
	// 	return errors.New("Invalid stream length, larger than file size")
	// }
}

// Get stream length, avoiding recursive loops.
// The input is the PdfObject that is to be traced to a direct object.
func (parser *PdfParser) traceStreamLength(lengthObj PdfObject) (PdfObject, error) {
	lengthRef, isRef := lengthObj.(*PdfObjectReference)
	if isRef {
		lookupInProgress, has := parser.loadFromStreamsInProgress(lengthRef.ObjectNumber)
		if has && lookupInProgress {
			common.Log.Debug("Stream Length reference unresolved (illegal)")
			return nil, errors.New("Illegal recursive loop")
		}
		// Mark lookup as in progress.
		parser.saveToStreamsInProgressXrefs(lengthRef.ObjectNumber, true)
	}

	slo, err := parser.Trace(lengthObj)
	if err != nil {
		return nil, err
	}
	common.Log.Trace("Stream length? %s", slo)

	if isRef {
		// Mark as completed lookup
		parser.saveToStreamsInProgressXrefs(lengthRef.ObjectNumber, false)
	}

	return slo, nil
}

// For testing purposes.
// TODO: Unexport (v3) or move to test files, if needed by external test cases.
func NewParserFromString(txt string) *PdfParser {
	parser := PdfParser{}
	buf := []byte(txt)

	bufReader := bytes.NewReader(buf)
	parser.rs = bufReader

	bufferedReader := bufio.NewReader(bufReader)
	parser.reader = bufferedReader

	parser.fileSize = int64(len(txt))

	return &parser
}

// NOT THREAD SAFE
func (parser *PdfParser) addXrefNextOffsets() {
	xrefs := make([]XrefObject, len(parser.xrefs), len(parser.xrefs))
	cnt := 0
	for _, val := range parser.xrefs {
		xrefs[cnt] = val
		cnt++
	}

	sort.Slice(xrefs, func(i, j int) bool {
		return xrefs[i].offset < xrefs[j].offset
	})

	for i, xref := range xrefs {
		// non object streams
		if xref.offset != 0 {
			next := parser.fileSize
			if i+1 < len(xrefs) {
				// no next use eof
				next = xrefs[i+1].offset
			}
			xref.nextOffset = next
			parser.xrefs[xref.objectNumber] = xref
		}
	}
}

// NewParser creates a new parser for a PDF file via ReadSeeker. Loads the cross reference stream and trailer.
// An error is returned on failure.
func NewParser(rs io.ReadSeeker) (*PdfParser, error) {
	parser := &PdfParser{}

	parser.rs = rs
	parser.objCache = make(ObjectCache)
	parser.streamLengthReferenceLookupInProgress = map[int64]bool{}

	// Start by reading the xrefs (from bottom).
	trailer, err := parser.loadXrefs()
	if err != nil {
		common.Log.Debug("ERROR: Failed to load xref table! %s", err)
		return nil, err
	}
	parser.addXrefNextOffsets()

	common.Log.Trace("Trailer: %s", trailer)

	if len(parser.xrefs) == 0 {
		return nil, fmt.Errorf("Empty XREF table - Invalid")
	}

	majorVersion, minorVersion, err := parser.parsePdfVersion()
	if err != nil {
		common.Log.Error("Unable to parse version: %v", err)
		return nil, err
	}
	parser.majorVersion = majorVersion
	parser.minorVersion = minorVersion

	parser.trailer = trailer

	return parser, nil
}

// IsEncrypted checks if the document is encrypted. A bool flag is returned indicating the result.
// First time when called, will check if the Encrypt dictionary is accessible through the trailer dictionary.
// If encrypted, prepares a crypt datastructure which can be used to authenticate and decrypt the document.
// On failure, an error is returned.
func (parser *PdfParser) IsEncrypted() (bool, error) {
	if parser.crypter != nil {
		return true, nil
	}

	if parser.trailer != nil {
		common.Log.Trace("Checking encryption dictionary!")
		encDictRef, isEncrypted := parser.trailer.Get("Encrypt").(*PdfObjectReference)
		if isEncrypted {
			common.Log.Trace("Is encrypted!")
			common.Log.Trace("0: Look up ref %q", encDictRef)
			encObj, err := parser.LookupByReference(*encDictRef)
			common.Log.Trace("1: %q", encObj)
			if err != nil {
				return false, err
			}

			encIndObj, ok := encObj.(*PdfIndirectObject)
			if !ok {
				common.Log.Debug("Encryption object not an indirect object")
				return false, errors.New("Type check error")
			}
			encDict, ok := encIndObj.PdfObject.(*PdfObjectDictionary)

			common.Log.Trace("2: %q", encDict)
			if !ok {
				return false, errors.New("Trailer Encrypt object non dictionary")
			}
			crypter, err := PdfCryptMakeNew(parser, encDict, parser.trailer)
			if err != nil {
				return false, err
			}

			parser.crypter = &crypter
			common.Log.Trace("Crypter object %b", crypter)
			return true, nil
		}
	}
	return false, nil
}

// Decrypt attempts to decrypt the PDF file with a specified password.  Also tries to
// decrypt with an empty password.  Returns true if successful, false otherwise.
// An error is returned when there is a problem with decrypting.
func (parser *PdfParser) Decrypt(password []byte) (bool, error) {
	// Also build the encryption/decryption key.
	if parser.crypter == nil {
		return false, errors.New("Check encryption first")
	}

	authenticated, err := parser.crypter.authenticate(password)
	if err != nil {
		return false, err
	}

	if !authenticated {
		authenticated, err = parser.crypter.authenticate([]byte(""))
	}

	return authenticated, err
}

// CheckAccessRights checks access rights and permissions for a specified password. If either user/owner password is
// specified, full rights are granted, otherwise the access rights are specified by the Permissions flag.
//
// The bool flag indicates that the user can access and view the file.
// The AccessPermissions shows what access the user has for editing etc.
// An error is returned if there was a problem performing the authentication.
func (parser *PdfParser) CheckAccessRights(password []byte) (bool, AccessPermissions, error) {
	// Also build the encryption/decryption key.
	if parser.crypter == nil {
		// If the crypter is not set, the file is not encrypted and we can assume full access permissions.
		perms := AccessPermissions{}
		perms.Printing = true
		perms.Modify = true
		perms.FillForms = true
		perms.RotateInsert = true
		perms.ExtractGraphics = true
		perms.DisabilityExtract = true
		perms.Annotate = true
		perms.FullPrintQuality = true
		return true, perms, nil
	}

	return parser.crypter.checkAccessRights(password)
}
