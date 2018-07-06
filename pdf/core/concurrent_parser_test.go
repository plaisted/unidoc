package core

import (
	"os"
	"testing"

	"github.com/unidoc/unidoc/common"
)

func init() {
	// common.SetLogger(common.DummyLogger{})
	//common.SetLogger(common.NewConsoleLogger(common.LogLevelTrace))
}

// func TestAverage(t *testing.T) {
// 	f, err := os.Open("c:\\test\\scenarios\\18000.pdf")
// 	if err != nil {
// 		t.Error(err)
// 	}
//
// 	parser, err := NewConcurrentParser(f)
// 	if err != nil {
// 		t.Error(err)
// 	}
//
// 	readChan := make(chan int)
// 	returnChan := make(chan bool)
//
// 	cnt := 0
// 	for _, objRef := range parser.xrefs {
// 		go loadObject(t, parser, objRef.objectNumber, returnChan)
// 		cnt++
// 	}
//
// 	returned := 0
// 	for res := range returnChan {
// 		returned++
// 		if !res {
// 			t.Error("Error occured.")
// 		}
// 		if returned == cnt {
// 			break
// 		}
// 	}
// }
func BenchmarkParallelLoad1(b *testing.B) { benchmarkParallelLoad(1, b) }
func BenchmarkParallelLoad2(b *testing.B) { benchmarkParallelLoad(2, b) }
func BenchmarkParallelLoad4(b *testing.B) { benchmarkParallelLoad(4, b) }
func BenchmarkParallelLoad8(b *testing.B) { benchmarkParallelLoad(8, b) }
func BenchmarkParallelLoad40(b *testing.B) { benchmarkParallelLoad(40, b) }
func benchmarkParallelLoad(workers int, b *testing.B) {
	for n := 0; n < b.N; n++ {
		//f, err := os.Open("c:\\test\\scenarios\\3000.pdf")
		f, err := os.Open("c:\\test\\scenarios\\Ticket.pdf")
		if err != nil {
			b.Error(err)
		}
		common.SetLogger(common.DummyLogger{})
		parser, err := NewParser(f)
		common.SetLogger(common.DummyLogger{})
		if err != nil {
			b.Error(err)
		}

		readChan := make(chan int)
		returnChan := make(chan bool)
		workerCount := workers
		for i := 0; i < workerCount; i++ {
			go objectLoader(b, parser, readChan, returnChan)
		}

		for _, objRef := range parser.xrefs {
			readChan <- objRef.objectNumber
		}
		close(readChan)

		returned := 0
		for _ = range returnChan {
			returned++
			if returned == workerCount {
				break
			}
		}
	}
}

func BenchmarkStandard(b *testing.B) {
	for n := 0; n < b.N; n++ {
		//f, err := os.Open("c:\\test\\scenarios\\3000.pdf")
		f, err := os.Open("c:\\test\\scenarios\\690025_stream.pdf")
		if err != nil {
			b.Error(err)
		}

		parser, err := NewParser(f)
		if err != nil {
			b.Error(err)
		}

		readChan := make(chan int)
		returnChan := make(chan bool)
		workerCount := 1
		for i := 0; i < workerCount; i++ {
			go objectLoaderSingle(b, parser, readChan, returnChan)
		}

		for _, objRef := range parser.xrefs {
			readChan <- objRef.objectNumber
		}
		close(readChan)

		returned := 0
		for _ = range returnChan {
			returned++
			if returned == workerCount {
				break
			}
		}
	}
}

func BenchmarkStandardChan(b *testing.B) {
	for n := 0; n < b.N; n++ {
		// f, err := os.Open("c:\\test\\scenarios\\3000.pdf")
		f, err := os.Open("c:\\test\\scenarios\\690025_stream.pdf")
		if err != nil {
			b.Error(err)
		}

		parser, err := NewParser(f)
		if err != nil {
			b.Error(err)
		}

		for _, objRef := range parser.xrefs {
			_, err := parser.LookupByNumber(objRef.objectNumber)
			if err != nil {
				b.Error(err)
			}
		}
	}
}

func objectLoaderSingle(t *testing.B, parser *PdfParser, in chan int, c chan bool) {
	for objNum := range in {
		// t.Logf("Loading object %d", objNum)
		_, err := parser.LookupByNumber(objNum)
		if err != nil {
			t.Error(err)
		} else {
			// t.Logf("%d number loaded successfully", objNum)
		}
	}
	c <- true
}

func objectLoader(t *testing.B, parser *PdfParser, in chan int, c chan bool) {
	for objNum := range in {
		// t.Logf("Loading object %d", objNum)
		_, err := parser.LookupByNumber(objNum)
		if err != nil {
			t.Error(err)
		} else {
			// t.Logf("%d number loaded successfully", objNum)
		}
	}
	c <- true
}

func loadObject(t *testing.T, parser *PdfParser, objNumber int) {
	t.Logf("Loading object %d", objNumber)
	_, err := parser.LookupByNumber(objNumber)
	if err != nil {
		t.Error(err)
	} else {
		t.Logf("%d number loaded successfully", objNumber)
	}
}

func TestOSDataLookupAndWrapping(t *testing.T) {
	obj1 := "<</Limits[719 793]/Nums[719<</S/D/St 360>>721<</S/D/St 361>>723<</S/D/St 362>>725<</S/D/St 363>>727<</S/D/St 364>>729<</S/D/St 365>>731<</S/D/St 366>>733<</S/D/St 367>>735<</S/D/St 368>>737<</S/D/St 369>>739<</S/D/St 370>>741<</S/D/St 371>>743<</S/D/St 372>>745<</S/D/St 373>>747<</S/D/St 374>>749<</S/D/St 375>>751<</S/D/St 376>>753<</S/D/St 377>>755<</S/D/St 378>>757<</S/D/St 379>>759<</S/D/St 380>>761<</S/D/St 381>>763<</S/D/St 382>>765<</S/D/St 383>>767<</S/D/St 384>>769<</S/D/St 385>>771<</S/D/St 386>>773<</S/D/St 387>>775<</S/D/St 388>>777<</S/D/St 389>>779<</S/D/St 390>>781<</S/D/St 391>>783<</S/D/St 392>>785<</S/D/St 393>>787<</S/D/St 394>>789<</S/D/St 395>>791<</S/D/St 396>>793<</P(397)>>]>>\n"
	obj1Length := 707 + 1 //new line
	obj1Start := 0
	obj2 := "<</OPM 1/Type/ExtGState>>\n"
	obj2Length := 25 + 1 //new line
	obj2Start := obj1Start + obj1Length
	obj3 := "<</Limits[599 717]/Nums[599<</S/D/St 300>>601<</S/D/St 301>>603<</S/D/St 302>>605<</S/D/St 303>>607<</S/D/St 304>>609<</S/D/St 305>>611<</S/D/St 306>>613<</S/D/St 307>>615<</S/D/St 308>>617<</S/D/St 309>>619<</S/D/St 310>>621<</S/D/St 311>>623<</S/D/St 312>>625<</S/D/St 313>>627<</S/D/St 314>>629<</S/D/St 315>>631<</S/D/St 316>>633<</S/D/St 317>>635<</S/D/St 318>>637<</S/D/St 319>>639<</S/D/St 320>>641<</S/D/St 321>>643<</S/D/St 322>>645<</S/D/St 323>>647<</S/D/St 324>>649<</S/D/St 325>>651<</S/D/St 326>>653<</S/D/St 327>>655<</S/D/St 328>>657<</S/D/St 329>>659<</S/D/St 330>>661<</S/D/St 331>>663<</S/D/St 332>>665<</S/D/St 333>>667<</S/D/St 334>>669<</S/D/St 335>>671<</S/D/St 336>>673<</S/D/St 337>>675<</S/D/St 338>>677<</S/D/St 339>>679<</S/D/St 340>>681<</S/D/St 341>>683<</S/D/St 342>>685<</S/D/St 343>>687<</S/D/St 344>>689<</S/D/St 345>>691<</S/D/St 346>>693<</S/D/St 347>>695<</S/D/St 348>>697<</S/D/St 349>>699<</S/D/St 350>>701<</S/D/St 351>>703<</S/D/St 352>>705<</S/D/St 353>>707<</S/D/St 354>>709<</S/D/St 355>>711<</S/D/St 356>>713<</S/D/St 357>>715<</S/D/St 358>>717<</S/D/St 359>>]>>\n"
	// obj3Length := 1107 + 1 //new line
	obj3Start := obj2Start + obj2Length
	bytes := []byte(obj1 + obj2 + obj3)
	result := getWrappedOSBytes(bytes, int64(obj2Start), int64(obj3Start), 100)
	if string(result[:10]) != "100 0 obj\n" {
		t.Error("Header incorrect or absent")
	}
	if string(result[len(result)-7:]) != "endobj\n" {
		t.Error("Trailer incorrect or absent")
	}

	result = getWrappedOSBytes(bytes, int64(obj3Start), int64(len(bytes)), 101)
	if string(result[:10]) != "101 0 obj\n" {
		t.Error("Header incorrect or absent")
	}
	if string(result[len(result)-7:]) != "endobj\n" {
		t.Error("Trailer incorrect or absent")
	}
}
