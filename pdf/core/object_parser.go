package core

import (
	"bufio"
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strconv"

	"github.com/unidoc/unidoc/common"
)

type ObjectReader struct {
	reader *bufio.Reader
	// fileParser *PdfParser
}

// Skip over any spaces.
func skipSpaces(reader *bufio.Reader) (int, error) {
	cnt := 0
	for {
		b, err := reader.ReadByte()
		if err != nil {
			return 0, err
		}
		if IsWhiteSpace(b) {
			cnt++
		} else {
			reader.UnreadByte()
			break
		}
	}

	return cnt, nil
}

// Skip over comments and spaces. Can handle multi-line comments.
func skipComments(reader *bufio.Reader) error {
	if _, err := skipSpaces(reader); err != nil {
		return err
	}

	isFirst := true
	for {
		bb, err := reader.Peek(1)
		if err != nil {
			common.Log.Debug("Error %s", err.Error())
			return err
		}
		if isFirst && bb[0] != '%' {
			// Not a comment clearly.
			return nil
		} else {
			isFirst = false
		}
		if (bb[0] != '\r') && (bb[0] != '\n') {
			reader.ReadByte()
		} else {
			break
		}
	}

	// Call recursively to handle multiline comments.
	return skipComments(reader)
}

// Read a comment starting with '%'.
func readComment(reader *bufio.Reader) (string, error) {
	var r bytes.Buffer

	_, err := skipSpaces(reader)
	if err != nil {
		return r.String(), err
	}

	isFirst := true
	for {
		bb, err := reader.Peek(1)
		if err != nil {
			common.Log.Debug("Error %s", err.Error())
			return r.String(), err
		}
		if isFirst && bb[0] != '%' {
			return r.String(), errors.New("Comment should start with %")
		} else {
			isFirst = false
		}
		if (bb[0] != '\r') && (bb[0] != '\n') {
			b, _ := reader.ReadByte()
			r.WriteByte(b)
		} else {
			break
		}
	}
	return r.String(), nil
}

// Read a single line of text from current position.
func readTextLine(reader *bufio.Reader) (string, error) {
	var r bytes.Buffer
	for {
		bb, err := reader.Peek(1)
		if err == io.EOF {
			return r.String(), nil
		}
		if err != nil {
			common.Log.Debug("Error %s", err.Error())
			return r.String(), err
		}
		if (bb[0] != '\r') && (bb[0] != '\n') {
			b, _ := reader.ReadByte()
			r.WriteByte(b)
		} else {
			break
		}
	}
	return r.String(), nil
}

// Parse a name starting with '/'.
func parseName(reader *bufio.Reader) (PdfObjectName, error) {
	var r bytes.Buffer
	nameStarted := false
	for {
		bb, err := reader.Peek(1)
		if err == io.EOF {
			break // Can happen when loading from object stream.
		}
		if err != nil {
			return PdfObjectName(r.String()), err
		}

		if !nameStarted {
			// Should always start with '/', otherwise not valid.
			if bb[0] == '/' {
				nameStarted = true
				reader.ReadByte()
			} else if bb[0] == '%' {
				readComment(reader)
				skipSpaces(reader)
			} else {
				common.Log.Debug("ERROR Name starting with %s (% x)", bb, bb)
				return PdfObjectName(r.String()), fmt.Errorf("Invalid name: (%c)", bb[0])
			}
		} else {
			if IsWhiteSpace(bb[0]) {
				break
			} else if (bb[0] == '/') || (bb[0] == '[') || (bb[0] == '(') || (bb[0] == ']') || (bb[0] == '<') || (bb[0] == '>') {
				break // Looks like start of next statement.
			} else if bb[0] == '#' {
				hexcode, err := reader.Peek(3)
				if err != nil {
					return PdfObjectName(r.String()), err
				}
				reader.Discard(3)

				code, err := hex.DecodeString(string(hexcode[1:3]))
				if err != nil {
					return PdfObjectName(r.String()), err
				}
				r.Write(code)
			} else {
				b, _ := reader.ReadByte()
				r.WriteByte(b)
			}
		}
	}
	return PdfObjectName(r.String()), nil
}

// Numeric objects.
// Section 7.3.3.
// Integer or Float.
//
// An integer shall be written as one or more decimal digits optionally
// preceded by a sign. The value shall be interpreted as a signed
// decimal integer and shall be converted to an integer object.
//
// A real value shall be written as one or more decimal digits with an
// optional sign and a leading, trailing, or embedded PERIOD (2Eh)
// (decimal point). The value shall be interpreted as a real number
// and shall be converted to a real object.
//
// Regarding exponential numbers: 7.3.3 Numeric Objects:
// A conforming writer shall not use the PostScript syntax for numbers
// with non-decimal radices (such as 16#FFFE) or in exponential format
// (such as 6.02E23).
// Nonetheless, we sometimes get numbers with exponential format, so
// we will support it in the reader (no confusion with other types, so
// no compromise).
func parseNumber(reader *bufio.Reader) (PdfObject, error) {
	isFloat := false
	allowSigns := true
	var r bytes.Buffer
	for {
		common.Log.Trace("Parsing number \"%s\"", r.String())
		bb, err := reader.Peek(1)
		if err == io.EOF {
			// GH: EOF handling.  Handle EOF like end of line.  Can happen with
			// encoded object streams that the object is at the end.
			// In other cases, we will get the EOF error elsewhere at any rate.
			break // Handle like EOF
		}
		if err != nil {
			common.Log.Debug("ERROR %s", err)
			return nil, err
		}
		if allowSigns && (bb[0] == '-' || bb[0] == '+') {
			// Only appear in the beginning, otherwise serves as a delimiter.
			b, _ := reader.ReadByte()
			r.WriteByte(b)
			allowSigns = false // Only allowed in beginning, and after e (exponential).
		} else if IsDecimalDigit(bb[0]) {
			b, _ := reader.ReadByte()
			r.WriteByte(b)
		} else if bb[0] == '.' {
			b, _ := reader.ReadByte()
			r.WriteByte(b)
			isFloat = true
		} else if bb[0] == 'e' {
			// Exponential number format.
			b, _ := reader.ReadByte()
			r.WriteByte(b)
			isFloat = true
			allowSigns = true
		} else {
			break
		}
	}

	if isFloat {
		fVal, err := strconv.ParseFloat(r.String(), 64)
		if err != nil {
			common.Log.Debug("Error parsing number %v err=%v. Using 0.0. Output may be incorrect", r.String(), err)
			fVal = 0.0
			err = nil
		}
		o := PdfObjectFloat(fVal)
		return &o, err
	} else {
		intVal, err := strconv.ParseInt(r.String(), 10, 64)
		o := PdfObjectInteger(intVal)
		return &o, err
	}
}

// A string starts with '(' and ends with ')'.
func parseString(reader *bufio.Reader) (PdfObjectString, error) {
	reader.ReadByte()

	var r bytes.Buffer
	count := 1
	for {
		bb, err := reader.Peek(1)
		if err != nil {
			return PdfObjectString(r.String()), err
		}

		if bb[0] == '\\' { // Escape sequence.
			reader.ReadByte() // Skip the escape \ byte.
			b, err := reader.ReadByte()
			if err != nil {
				return PdfObjectString(r.String()), err
			}

			// Octal '\ddd' number (base 8).
			if IsOctalDigit(b) {
				bb, err := reader.Peek(2)
				if err != nil {
					return PdfObjectString(r.String()), err
				}

				numeric := []byte{}
				numeric = append(numeric, b)
				for _, val := range bb {
					if IsOctalDigit(val) {
						numeric = append(numeric, val)
					} else {
						break
					}
				}
				reader.Discard(len(numeric) - 1)

				common.Log.Trace("Numeric string \"%s\"", numeric)
				code, err := strconv.ParseUint(string(numeric), 8, 32)
				if err != nil {
					return PdfObjectString(r.String()), err
				}
				r.WriteByte(byte(code))
				continue
			}

			switch b {
			case 'n':
				r.WriteRune('\n')
			case 'r':
				r.WriteRune('\r')
			case 't':
				r.WriteRune('\t')
			case 'b':
				r.WriteRune('\b')
			case 'f':
				r.WriteRune('\f')
			case '(':
				r.WriteRune('(')
			case ')':
				r.WriteRune(')')
			case '\\':
				r.WriteRune('\\')
			}

			continue
		} else if bb[0] == '(' {
			count++
		} else if bb[0] == ')' {
			count--
			if count == 0 {
				reader.ReadByte()
				break
			}
		}

		b, _ := reader.ReadByte()
		r.WriteByte(b)
	}

	return PdfObjectString(r.String()), nil
}

// Starts with '<' ends with '>'.
// Currently not converting the hex codes to characters.
func parseHexString(reader *bufio.Reader) (PdfObjectString, error) {
	reader.ReadByte()

	var r bytes.Buffer
	for {
		bb, err := reader.Peek(1)
		if err != nil {
			return PdfObjectString(""), err
		}

		if bb[0] == '>' {
			reader.ReadByte()
			break
		}

		b, _ := reader.ReadByte()
		if !IsWhiteSpace(b) {
			r.WriteByte(b)
		}
	}

	if r.Len()%2 == 1 {
		r.WriteRune('0')
	}

	buf, _ := hex.DecodeString(r.String())
	return PdfObjectString(buf), nil
}

// Starts with '[' ends with ']'.  Can contain any kinds of direct objects.
func parseArray(reader *bufio.Reader) (PdfObjectArray, error) {
	arr := make(PdfObjectArray, 0)

	reader.ReadByte()

	for {
		skipSpaces(reader)

		bb, err := reader.Peek(1)
		if err != nil {
			return arr, err
		}

		if bb[0] == ']' {
			reader.ReadByte()
			break
		}

		obj, err := parseObject(reader)
		if err != nil {
			return arr, err
		}
		arr = append(arr, obj)
	}

	return arr, nil
}

// Parse bool object.
func parseBool(reader *bufio.Reader) (PdfObjectBool, error) {
	bb, err := reader.Peek(4)
	if err != nil {
		return PdfObjectBool(false), err
	}
	if (len(bb) >= 4) && (string(bb[:4]) == "true") {
		reader.Discard(4)
		return PdfObjectBool(true), nil
	}

	bb, err = reader.Peek(5)
	if err != nil {
		return PdfObjectBool(false), err
	}
	if (len(bb) >= 5) && (string(bb[:5]) == "false") {
		reader.Discard(5)
		return PdfObjectBool(false), nil
	}

	return PdfObjectBool(false), errors.New("Unexpected boolean string")
}

// Parse null object.
func parseNull(reader *bufio.Reader) (PdfObjectNull, error) {
	_, err := reader.Discard(4)
	return PdfObjectNull{}, err
}

// Detect the signature at the current file position and parse
// the corresponding object.
func parseObject(reader *bufio.Reader) (PdfObject, error) {
	common.Log.Trace("Read direct object")
	skipSpaces(reader)
	for {
		bb, err := reader.Peek(2)
		if err != nil {
			return nil, err
		}

		common.Log.Trace("Peek string: %s", string(bb))
		// Determine type.
		if bb[0] == '/' {
			name, err := parseName(reader)
			common.Log.Trace("->Name: '%s'", name)
			return &name, err
		} else if bb[0] == '(' {
			common.Log.Trace("->String!")
			str, err := parseString(reader)
			return &str, err
		} else if bb[0] == '[' {
			common.Log.Trace("->Array!")
			arr, err := parseArray(reader)
			return &arr, err
		} else if (bb[0] == '<') && (bb[1] == '<') {
			common.Log.Trace("->Dict!")
			dict, err := ParseDict(reader)
			return dict, err
		} else if bb[0] == '<' {
			common.Log.Trace("->Hex string!")
			str, err := parseHexString(reader)
			return &str, err
		} else if bb[0] == '%' {
			readComment(reader)
			skipSpaces(reader)
		} else {
			common.Log.Trace("->Number or ref?")
			// Reference or number?
			// Let's peek farther to find out.
			bb, _ = reader.Peek(15)
			peekStr := string(bb)
			common.Log.Trace("Peek str: %s", peekStr)

			if (len(peekStr) > 3) && (peekStr[:4] == "null") {
				null, err := parseNull(reader)
				return &null, err
			} else if (len(peekStr) > 4) && (peekStr[:5] == "false") {
				b, err := parseBool(reader)
				return &b, err
			} else if (len(peekStr) > 3) && (peekStr[:4] == "true") {
				b, err := parseBool(reader)
				return &b, err
			}

			// Match reference.
			result1 := reReference.FindStringSubmatch(string(peekStr))
			if len(result1) > 1 {
				bb, _ = reader.ReadBytes('R')
				common.Log.Trace("-> !Ref: '%s'", string(bb[:]))
				ref, err := parseReference(string(bb))
				return &ref, err
			}

			result2 := reNumeric.FindStringSubmatch(string(peekStr))
			if len(result2) > 1 {
				// Number object.
				common.Log.Trace("-> Number!")
				num, err := parseNumber(reader)
				return num, err
			}

			result2 = reExponential.FindStringSubmatch(string(peekStr))
			if len(result2) > 1 {
				// Number object (exponential)
				common.Log.Trace("-> Exponential Number!")
				common.Log.Trace("% s", result2)
				num, err := parseNumber(reader)
				return num, err
			}

			common.Log.Debug("ERROR Unknown (peek \"%s\")", peekStr)
			return nil, errors.New("Object parsing error - unexpected pattern")
		}
	}
}

func parseReference(refStr string) (PdfObjectReference, error) {
	objref := PdfObjectReference{}

	result := reReference.FindStringSubmatch(string(refStr))
	if len(result) < 3 {
		common.Log.Debug("Error parsing reference")
		return objref, errors.New("Unable to parse reference")
	}

	objNum, _ := strconv.Atoi(result[1])
	genNum, _ := strconv.Atoi(result[2])
	objref.ObjectNumber = int64(objNum)
	objref.GenerationNumber = int64(genNum)

	return objref, nil
}

// Reads and parses a PDF dictionary object enclosed with '<<' and '>>'
// TODO: Unexport (v3).
func ParseDict(reader *bufio.Reader) (*PdfObjectDictionary, error) {
	common.Log.Trace("Reading PDF Dict!")

	dict := MakeDict()

	// Pass the '<<'
	c, _ := reader.ReadByte()
	if c != '<' {
		return nil, errors.New("Invalid dict")
	}
	c, _ = reader.ReadByte()
	if c != '<' {
		return nil, errors.New("Invalid dict")
	}

	for {
		skipSpaces(reader)
		skipComments(reader)

		bb, err := reader.Peek(2)
		if err != nil {
			return nil, err
		}

		common.Log.Trace("Dict peek: %s (% x)!", string(bb), string(bb))
		if (bb[0] == '>') && (bb[1] == '>') {
			common.Log.Trace("EOF dictionary")
			reader.ReadByte()
			reader.ReadByte()
			break
		}
		common.Log.Trace("Parse the name!")

		keyName, err := parseName(reader)
		common.Log.Trace("Key: %s", keyName)
		if err != nil {
			common.Log.Debug("ERROR Returning name err %s", err)
			return nil, err
		}

		if len(keyName) > 4 && keyName[len(keyName)-4:] == "null" {
			// Some writers have a bug where the null is appended without
			// space.  For example "\Boundsnull"
			newKey := keyName[0 : len(keyName)-4]
			common.Log.Debug("Taking care of null bug (%s)", keyName)
			common.Log.Debug("New key \"%s\" = null", newKey)
			skipSpaces(reader)
			bb, _ := reader.Peek(1)
			if bb[0] == '/' {
				dict.Set(newKey, MakeNull())
				continue
			}
		}

		skipSpaces(reader)

		val, err := parseObject(reader)
		if err != nil {
			return nil, err
		}
		dict.Set(keyName, val)

		common.Log.Trace("dict[%s] = %s", keyName, val.String())
	}
	common.Log.Trace("returning PDF Dict!")

	return dict, nil
}

// Parse an indirect object from the input stream. Can also be an object stream.
// Returns the indirect object (*PdfIndirectObject) or the stream object (*PdfObjectStream).
// TODO: Unexport (v3).
func ParseIndirectObject(reader *bufio.Reader) (PdfObject, error) {
	indirect := PdfIndirectObject{}

	common.Log.Trace("-Read indirect obj")
	bb, err := reader.Peek(20)
	if err != nil {
		common.Log.Debug("ERROR: Fail to read indirect obj")
		return &indirect, err
	}
	common.Log.Trace("(indirect obj peek \"%s\"", string(bb))

	indices := reIndirectObject.FindStringSubmatchIndex(string(bb))
	if len(indices) < 6 {
		common.Log.Debug("ERROR: Unable to find object signature (%s)", string(bb))
		return &indirect, errors.New("Unable to detect indirect object signature")
	}
	reader.Discard(indices[0]) // Take care of any small offset.
	common.Log.Trace("Offsets % d", indices)

	// Read the object header.
	hlen := indices[1] - indices[0]
	hb := make([]byte, hlen)
	_, err = ReadAtLeast(reader, hb, hlen)
	if err != nil {
		common.Log.Debug("ERROR: unable to read - %s", err)
		return nil, err
	}
	common.Log.Trace("textline: %s", hb)

	result := reIndirectObject.FindStringSubmatch(string(hb))
	if len(result) < 3 {
		common.Log.Debug("ERROR: Unable to find object signature (%s)", string(hb))
		return &indirect, errors.New("Unable to detect indirect object signature")
	}

	on, _ := strconv.Atoi(result[1])
	gn, _ := strconv.Atoi(result[2])
	indirect.ObjectNumber = int64(on)
	indirect.GenerationNumber = int64(gn)

	for {
		bb, err := reader.Peek(2)
		if err != nil {
			return &indirect, err
		}
		common.Log.Trace("Ind. peek: %s (% x)!", string(bb), string(bb))

		if IsWhiteSpace(bb[0]) {
			skipSpaces(reader)
		} else if bb[0] == '%' {
			skipComments(reader)
		} else if (bb[0] == '<') && (bb[1] == '<') {
			common.Log.Trace("Call ParseDict")
			indirect.PdfObject, err = ParseDict(reader)
			common.Log.Trace("EOF Call ParseDict: %v", err)
			if err != nil {
				return &indirect, err
			}
			common.Log.Trace("Parsed dictionary... finished.")
		} else if (bb[0] == '/') || (bb[0] == '(') || (bb[0] == '[') || (bb[0] == '<') {
			indirect.PdfObject, err = parseObject(reader)
			if err != nil {
				return &indirect, err
			}
			common.Log.Trace("Parsed object ... finished.")
		} else {
			if bb[0] == 'e' {
				lineStr, err := readTextLine(reader)
				if err != nil {
					return nil, err
				}
				if len(lineStr) >= 6 && lineStr[0:6] == "endobj" {
					break
				}
			} else if bb[0] == 's' {
				bb, _ = reader.Peek(10)
				if string(bb[:6]) == "stream" {
					discardBytes := 6
					if len(bb) > 6 {
						if IsWhiteSpace(bb[discardBytes]) && bb[discardBytes] != '\r' && bb[discardBytes] != '\n' {
							// If any other white space character... should not happen!
							// Skip it..
							common.Log.Debug("Non-conformant PDF not ending stream line properly with EOL marker")
							discardBytes++
						}
						if bb[discardBytes] == '\r' {
							discardBytes++
							if bb[discardBytes] == '\n' {
								discardBytes++
							}
						} else if bb[discardBytes] == '\n' {
							discardBytes++
						}
					}

					reader.Discard(discardBytes)

					dict, isDict := indirect.PdfObject.(*PdfObjectDictionary)
					if !isDict {
						return nil, errors.New("Stream object missing dictionary")
					}
					common.Log.Trace("Stream dict %s", dict)

					bufstream := &bytes.Buffer{}
					var stream []byte

					lastEnd := 0
					for {
						b, err := reader.ReadByte()
						if err != nil {
							if err == io.EOF {
								break
							}
							return nil, err
						}

						bufstream.WriteByte(b)

						if b == 'm' && bufstream.Len() > 8 {
							current := bufstream.Bytes()
							if string(current[len(current)-9:]) == "endstream" {
								lastEnd = len(current) - 9
							}
						}
					}

					if lastEnd == 0 {
						return nil, errors.New("No endstream found for stream object")
					}

					stream = bufstream.Bytes()[0:lastEnd]

					// remove end of line from stream
					if string(stream[len(stream)-2:]) == "\r\n" {
						stream = stream[:len(stream)-2]
					} else if string(stream[len(stream)-1:]) == "\n" {
						stream = stream[:len(stream)-1]
					}

					streamobj := PdfObjectStream{}
					streamobj.Stream = stream
					streamobj.PdfObjectDictionary = indirect.PdfObject.(*PdfObjectDictionary)
					streamobj.ObjectNumber = indirect.ObjectNumber
					streamobj.GenerationNumber = indirect.GenerationNumber

					return &streamobj, nil
				}
			}

			indirect.PdfObject, err = parseObject(reader)
			return &indirect, err
		}
	}
	common.Log.Trace("Returning indirect!")
	return &indirect, nil
}

func ReadAtLeast(reader *bufio.Reader, p []byte, n int) (int, error) {
	remaining := n
	start := 0
	numRounds := 0
	for remaining > 0 {
		nRead, err := reader.Read(p[start:])
		if err != nil {
			common.Log.Debug("ERROR Failed reading (%d;%d) %s", nRead, numRounds, err.Error())
			return start, errors.New("Failed reading")
		}
		numRounds++
		start += nRead
		remaining -= nRead
	}
	return start, nil
}
