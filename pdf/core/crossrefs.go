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
	"os"
	"strings"

	"github.com/unidoc/unidoc/common"
)

// TODO (v3): Create a new type xrefType which can be an integer and can be used for improved type checking.
// TODO (v3): Unexport these constants and rename with camelCase.
const (
	// XREF_TABLE_ENTRY indicates a normal xref table entry.
	XREF_TABLE_ENTRY = iota

	// XREF_OBJECT_STREAM indicates an xref entry in an xref object stream.
	XREF_OBJECT_STREAM = iota
)

// XrefObject defines a cross reference entry which is a map between object number (with generation number) and the
// location of the actual object, either as a file offset (xref table entry), or as a location within an xref
// stream object (xref object stream).
// TODO (v3): Unexport.
type XrefObject struct {
	xtype        int
	objectNumber int
	generation   int
	// For normal xrefs (defined by OFFSET)
	offset     int64
	nextOffset int64
	// For xrefs to object streams.
	osObjNumber int
	osObjIndex  int
}

// XrefTable is a map between object number and corresponding XrefObject.
// TODO (v3): Unexport.
// TODO: Consider changing to a slice, so can maintain the object order without sorting when analyzing.
type XrefTable map[int]XrefObject

// ObjectStream represents an object stream's information which can contain multiple indirect objects.
// The information specifies the number of objects and has information about offset locations for
// each object.
// TODO (v3): Unexport.
type ObjectStream struct {
	N       int // TODO (v3): Unexport.
	ds      []byte
	offsets map[int]*osOffsets
}

type osOffsets struct {
	Start int64
	End   int64
}

// ObjectStreams defines a map between object numbers (object streams only) and underlying ObjectStream information.
type ObjectStreams map[int]ObjectStream

// ObjectCache defines a map between object numbers and corresponding PdfObject. Serves as a cache for PdfObjects that
// have already been parsed.
// TODO (v3): Unexport.
type ObjectCache map[int]PdfObject

// Get an object from an object stream.
func (parser *PdfParser) lookupObjectBytesViaOS(sobjNumber int, objNum int) ([]byte, error) {
	var bufReader *bytes.Reader
	var objstm ObjectStream
	var cached bool

	objstm, cached = parser.fromStreamCache(sobjNumber)
	if !cached {
		reader, _, err := parser.lookupReaderByNumber(sobjNumber, false)
		if err != nil {
			common.Log.Debug("Missing object stream with number %d", sobjNumber)
			return nil, err
		}
		soi, err := ParseIndirectObject(reader)
		if err != nil {
			common.Log.Debug("Error parsing object stream with number %d", sobjNumber)
			return nil, err
		}

		so, ok := soi.(*PdfObjectStream)
		if !ok {
			return nil, errors.New("Invalid object stream")
		}

		if parser.crypter != nil && !parser.crypter.isDecrypted(so) {
			return nil, errors.New("Need to decrypt the stream")
		}

		sod := so.PdfObjectDictionary
		common.Log.Trace("so d: %s\n", *sod)
		name, ok := sod.Get("Type").(*PdfObjectName)
		if !ok {
			common.Log.Debug("ERROR: Object stream should always have a Type")
			return nil, errors.New("Object stream missing Type")
		}
		if strings.ToLower(string(*name)) != "objstm" {
			common.Log.Debug("ERROR: Object stream type shall always be ObjStm !")
			return nil, errors.New("Object stream type != ObjStm")
		}

		N, ok := sod.Get("N").(*PdfObjectInteger)
		if !ok {
			return nil, errors.New("Invalid N in stream dictionary")
		}
		firstOffset, ok := sod.Get("First").(*PdfObjectInteger)
		if !ok {
			return nil, errors.New("Invalid First in stream dictionary")
		}

		common.Log.Trace("type: %s number of objects: %d", name, *N)
		ds, err := DecodeStream(so)
		if err != nil {
			return nil, err
		}

		common.Log.Trace("Decoded: %s", ds)

		bufReader = bytes.NewReader(ds)
		reader = bufio.NewReader(bufReader)

		common.Log.Trace("Parsing offset map")
		// Load the offset map (relative to the beginning of the stream...)
		offsets := map[int]*osOffsets{}
		var lastOffset *osOffsets
		// Object list and offsets.
		for i := 0; i < int(*N); i++ {
			skipSpaces(reader)
			// Object number.
			obj, err := parseNumber(reader)
			if err != nil {
				return nil, err
			}
			onum, ok := obj.(*PdfObjectInteger)
			if !ok {
				return nil, errors.New("Invalid object stream offset table")
			}

			skipSpaces(reader)
			// Offset.
			obj, err = parseNumber(reader)
			if err != nil {
				return nil, err
			}
			offset, ok := obj.(*PdfObjectInteger)
			if !ok {
				return nil, errors.New("Invalid object stream offset table")
			}

			common.Log.Trace("obj %d offset %d", *onum, *offset)
			thisOffsets := &osOffsets{
				Start: int64(*firstOffset + *offset),
			}
			offsets[int(*onum)] = thisOffsets
			if lastOffset != nil {
				lastOffset.End = thisOffsets.Start
			}
			lastOffset = thisOffsets
		}

		if lastOffset != nil {
			lastOffset.End = int64(len(ds))
		}

		objstm = ObjectStream{N: int(*N), ds: ds, offsets: offsets}
		parser.toStreamCache(sobjNumber, objstm)
	}

	offsets := objstm.offsets[objNum]
	common.Log.Trace("ACTUAL offset[%d] = %d", objNum, offsets.Start)

	peakEnd := 100
	if len(objstm.ds) < peakEnd {
		peakEnd = len(objstm.ds)
	}
	bb := objstm.ds[:peakEnd]
	common.Log.Trace("OBJ peek \"%s\"", string(bb))

	return getWrappedOSBytes(objstm.ds, offsets.Start, offsets.End, objNum), nil
}

func getWrappedOSBytes(data []byte, start, end int64, objNo int) []byte {
	header := fmt.Sprintf("%d %d obj\n", objNo, 0)
	trailer := "endobj\n"
	dataStart := len(header)
	dataEnd := int64(dataStart) + end - start
	if data[end-1] != '\n' {
		trailer = "\nendobj\n"
	}
	result := make([]byte, end-start+int64(len(header))+int64(len(trailer)))
	copy(result[:dataStart], header)
	copy(result[dataStart:dataEnd], data[start:end])
	copy(result[dataEnd:], trailer)
	return result
}

func getObjectNumber(obj PdfObject) (int64, int64, error) {
	if io, isIndirect := obj.(*PdfIndirectObject); isIndirect {
		return io.ObjectNumber, io.GenerationNumber, nil
	}
	if so, isStream := obj.(*PdfObjectStream); isStream {
		return so.ObjectNumber, so.GenerationNumber, nil
	}
	return 0, 0, errors.New("Not an indirect/stream object")
}

// LookupByNumber looks up a PdfObject by object number.  Returns an error on failure.
// TODO (v3): Unexport.
func (parser *PdfParser) LookupByNumber(objNumber int) (PdfObject, error) {
	// Outside interface for lookupByNumberWrapper.  Default attempts repairs of bad xref tables.
	obj, _, err := parser.lookupByNumberWrapper(objNumber, true)
	return obj, err
}

// Wrapper for lookupByNumber, checks if object encrypted etc.
func (parser *PdfParser) lookupByNumberWrapper(objNumber int, attemptRepairs bool) (PdfObject, bool, error) {
	obj, inObjStream, err := parser.lookupByNumber(objNumber, attemptRepairs)
	if err != nil {
		return nil, inObjStream, err
	}

	// If encrypted, decrypt it prior to returning.
	// Do not attempt to decrypt objects within object streams.
	if !inObjStream && parser.crypter != nil && !parser.crypter.isDecrypted(obj) {
		err := parser.crypter.Decrypt(obj, 0, 0)
		if err != nil {
			return nil, inObjStream, err
		}
	}

	return obj, inObjStream, nil
}

func (parser *PdfParser) lookupReaderByNumber(objNumber int, attemptRepairs bool) (*bufio.Reader, bool, error) {
	data, isObjStream, err := parser.lookupBytesByNumber(objNumber, attemptRepairs)
	if err != nil {
		return nil, isObjStream, err
	}
	if data == nil {
		return nil, false, nil
	}
	return bufio.NewReader(bytes.NewReader(data)), isObjStream, nil
}

func (parser *PdfParser) lookupBytesByNumber(objNumber int, attemptRepairs bool) ([]byte, bool, error) {
	xref, ok := parser.loadFromXrefs(objNumber)
	if !ok {
		// An indirect reference to an undefined object shall not be
		// considered an error by a conforming reader; it shall be
		// treated as a reference to the null object.
		common.Log.Trace("Unable to locate object in xrefs! - Returning null object")
		return nil, false, nil
	}
	common.Log.Trace("Lookup obj number %d", objNumber)
	if xref.xtype == XREF_TABLE_ENTRY {
		common.Log.Trace("xrefobj obj num %d", xref.objectNumber)
		common.Log.Trace("xrefobj gen %d", xref.generation)
		common.Log.Trace("xrefobj offset %d", xref.offset)

		parser.rsMut.Lock()
		parser.rs.Seek(xref.offset, os.SEEK_SET)
		reader := bufio.NewReader(parser.rs)
		objBytes := make([]byte, xref.nextOffset-xref.offset)
		_, err := reader.Read(objBytes)
		parser.rsMut.Unlock()
		return objBytes, false, err
	} else if xref.xtype == XREF_OBJECT_STREAM {
		common.Log.Trace("xref from object stream!")
		common.Log.Trace(">Load via OS!")
		common.Log.Trace("Object stream available in object %d/%d", xref.osObjNumber, xref.osObjIndex)

		if xref.osObjNumber == objNumber {
			common.Log.Debug("ERROR Circular reference!?!")
			return nil, true, errors.New("Xref circular reference")
		}
		_, exists := parser.loadFromXrefs(xref.osObjNumber)
		if exists {
			objBytes, err := parser.lookupObjectBytesViaOS(xref.osObjNumber, objNumber) //xref.osObjIndex)
			if err != nil {
				common.Log.Debug("ERROR Returning ERR (%s)", err)
				return nil, true, err
			}
			common.Log.Trace("<Loaded via OS")
			return objBytes, true, nil
		} else {
			common.Log.Debug("?? Belongs to a non-cross referenced object ...!")
			return nil, true, errors.New("OS belongs to a non cross referenced object")
		}
	}
	return nil, false, errors.New("Unknown xref type")
}

// LookupByNumber
// Repair signals whether to repair if broken.
func (parser *PdfParser) lookupByNumber(objNumber int, attemptRepairs bool) (PdfObject, bool, error) {
	obj, cached := parser.fromObjCache(objNumber)
	if cached {
		return obj, false, nil
	}

	reader, isObjStream, err := parser.lookupReaderByNumber(objNumber, true)
	if err != nil {
		return nil, isObjStream, err
	}

	if reader == nil {
		io := PdfIndirectObject{}
		io.ObjectNumber = int64(objNumber)
		io.PdfObject = &PdfObjectNull{}
		return &io, false, nil
	} else {
		obj, err := ParseIndirectObject(reader)
		if err != nil {
			common.Log.Debug("ERROR Failed reading xref (%s)", err)
			// Offset pointing to a non-object.  Try to repair the file.
			if attemptRepairs {
				common.Log.Debug("Attempting to repair xrefs (top down)")
				xrefTable, err := parser.repairRebuildXrefsTopDown()
				if err != nil {
					common.Log.Debug("ERROR Failed repair (%s)", err)
					return nil, isObjStream, err
				}
				parser.xrefMut.Lock()
				parser.xrefs = *xrefTable
				parser.addXrefNextOffsets()
				parser.xrefMut.Unlock()
				return parser.lookupByNumber(objNumber, false)
			}
			return nil, isObjStream, err
		}

		if attemptRepairs {
			// Check the object number..
			// If it does not match, then try to rebuild, i.e. loop through
			// all the items in the xref and look each one up and correct.
			realObjNum, _, _ := getObjectNumber(obj)
			if int(realObjNum) != objNumber {
				common.Log.Debug("Invalid xrefs: Rebuilding")
				err := parser.rebuildXrefTable()
				if err != nil {
					return nil, isObjStream, err
				}
				// Empty the cache.
				parser.objCacheMut.Lock()
				parser.objCache = ObjectCache{}
				parser.objCacheMut.Unlock()
				// Try looking up again and return.
				return parser.lookupByNumber(objNumber, false)
			}
		}

		if objStm, is := obj.(*PdfObjectStream); is {
			err = parser.validateObjectStreamLength(objStm)
			if err != nil {
				return obj, isObjStream, err
			}
		}

		parser.toObjCache(objNumber, obj)
		return obj, false, nil
	}
}

func (parser *PdfParser) validateObjectStreamLength(obj *PdfObjectStream) error {
	// Special stream length tracing function used to avoid endless recursive looping.
	slo, err := parser.traceStreamLength(obj.PdfObjectDictionary.Get("Length"))
	if err != nil {
		common.Log.Debug("Fail to trace stream length: %v", err)
		return err
	}
	common.Log.Trace("Stream length? %s", slo)

	pstreamLength, ok := slo.(*PdfObjectInteger)
	if !ok {
		return errors.New("Stream length needs to be an integer")
	}
	streamLength := *pstreamLength
	if streamLength < 0 {
		return errors.New("Stream needs to be longer than 0")
	}

	if int(streamLength) == len(obj.Stream) {
		return nil
	}

	// look into current unidoc corrections below and see if we need to follow those
	// should be covered by way we are using object offsets from the start
	obj.PdfObjectDictionary.Set("Length", MakeInteger(int64(len(obj.Stream))))

	return nil
	//	// Validate the stream length based on the cross references.
	//	// Find next object with closest offset to current object and calculate
	//	// the expected stream length based on that.
	//	xref, found := parser.loadFromXrefs(int(obj.ObjectNumber))
	//	if !found {
	//		return errors.New("Bad xref when attempting stream length verification")
	//	}
	//
	//	streamStartOffset := parser.GetFileOffset()
	//	nextObjectOffset := parser.xrefNextObjectOffset(streamStartOffset)
	//	if streamStartOffset+int64(streamLength) > nextObjectOffset && nextObjectOffset > streamStartOffset {
	//		common.Log.Debug("Expected ending at %d", streamStartOffset+int64(streamLength))
	//		common.Log.Debug("Next object starting at %d", nextObjectOffset)
	//		// endstream + "\n" endobj + "\n" (17)
	//		newLength := nextObjectOffset - streamStartOffset - 17
	//		if newLength < 0 {
	//			return errors.New("Invalid stream length, going past boundaries")
	//		}
	//
	//		common.Log.Debug("Attempting a length correction to %d...", newLength)
	//		streamLength = PdfObjectInteger(newLength)
	//		obj.PdfObjectDictionary.Set("Length", MakeInteger(newLength))
	//	}
	//
	//	// Make sure is less than actual file size.
	//	if int64(streamLength) > parser.fileSize {
	//		common.Log.Debug("ERROR: Stream length cannot be larger than file size")
	//		return errors.New("Invalid stream length, larger than file size")
	//	}
	//	return nil
}

// LookupByReference looks up a PdfObject by a reference.
func (parser *PdfParser) LookupByReference(ref PdfObjectReference) (PdfObject, error) {
	common.Log.Trace("Looking up reference %s", ref.String())
	return parser.LookupByNumber(int(ref.ObjectNumber))
}

// Trace traces a PdfObject to direct object, looking up and resolving references as needed (unlike TraceToDirect).
// TODO (v3): Unexport.
func (parser *PdfParser) Trace(obj PdfObject) (PdfObject, error) {
	ref, isRef := obj.(*PdfObjectReference)
	if !isRef {
		// Direct object already.
		return obj, nil
	}

	bakOffset := parser.GetFileOffset()
	defer func() { parser.SetFileOffset(bakOffset) }()

	o, err := parser.LookupByReference(*ref)
	if err != nil {
		return nil, err
	}

	io, isInd := o.(*PdfIndirectObject)
	if !isInd {
		// Not indirect (Stream or null object).
		return o, nil
	}
	o = io.PdfObject
	_, isRef = o.(*PdfObjectReference)
	if isRef {
		return io, errors.New("Multi depth trace pointer to pointer")
	}

	return o, nil
}

func printXrefTable(xrefTable XrefTable) {
	common.Log.Debug("=X=X=X=")
	common.Log.Debug("Xref table:")
	i := 0
	for _, xref := range xrefTable {
		common.Log.Debug("i+1: %d (obj num: %d gen: %d) -> %d", i+1, xref.objectNumber, xref.generation, xref.offset)
		i++
	}
}
