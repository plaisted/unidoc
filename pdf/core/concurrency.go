package core

func (parser *PdfParser) fromObjCache(id int) (PdfObject, bool) {
	parser.objCacheMut.Lock()
	obj, ok := parser.objCache[id]
	parser.objCacheMut.Unlock()
	return obj, ok
}

func (parser *PdfParser) toObjCache(id int, obj PdfObject) {
	parser.objCacheMut.Lock()
	parser.objCache[id] = obj
	parser.objCacheMut.Unlock()
}

func (parser *PdfParser) fromStreamCache(id int) (ObjectStream, bool) {
	parser.objstmsMut.Lock()
	obj, ok := parser.objstms[id]
	parser.objstmsMut.Unlock()
	return obj, ok
}

func (parser *PdfParser) toStreamCache(id int, obj ObjectStream) {
	parser.objstmsMut.Lock()
	parser.objstms[id] = obj
	parser.objstmsMut.Unlock()
}

func (parser *PdfParser) loadFromXrefs(id int) (XrefObject, bool) {
	parser.xrefMut.Lock()
	obj, ok := parser.xrefs[id]
	parser.xrefMut.Unlock()
	return obj, ok
}

func (parser *PdfParser) saveToXrefs(id int, obj XrefObject) {
	parser.xrefMut.Lock()
	parser.xrefs[id] = obj
	parser.xrefMut.Unlock()
}

func (parser *PdfParser) loadFromStreamsInProgress(id int64) (bool, bool) {
	parser.xrefMut.Lock()
	obj, ok := parser.streamLengthReferenceLookupInProgress[id]
	parser.xrefMut.Unlock()
	return obj, ok
}

func (parser *PdfParser) saveToStreamsInProgressXrefs(id int64, val bool) {
	parser.xrefMut.Lock()
	parser.streamLengthReferenceLookupInProgress[id] = val
	parser.xrefMut.Unlock()
}
