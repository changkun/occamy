package protocol

var instructionHandlers = map[string]func(){
	"sync":       handleSync,
	"mouse":      handleMouse,
	"key":        handleKey,
	"clipboard":  handleClipboard,
	"disconnect": handleDisconnect,
	"size":       handleSize,
	"file":       handleFile,
	"pipe":       handlePipe,
	"ack":        handleAck,
	"blob":       handleBlob,
	"end":        handleEnd,
	"get":        handleGet,
	"put":        handlePut,
	"audio":      handleAudio,
}

func (i Instruction) handleInstruction() {
	handler, ok := instructionHandlers[i.elements[0]]
	if !ok {
		return // if unrecognized, ignore
	}
	handler()
	return
}

func handleSync() {
	// TODO:
}

func handleMouse() {
	// TODO:
}

func handleKey() {
	// TODO:
}

func handleClipboard() {
	// TODO:
}

func handleDisconnect() {
	// TODO:
}

func handleSize() {
	// TODO:
}

func handleFile() {
	// TODO:
}

func handlePipe() {
	// TODO:
}

func handleAck() {
	// TODO:
}

func handleBlob() {
	// TODO:
}

func handleEnd() {
	// TODO:
}

func handleGet() {
	// TODO:
}

func handlePut() {
	// TODO:
}
func handleAudio() {
	// TODO:
}
