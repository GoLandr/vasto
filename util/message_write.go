package util

import (
	"encoding/binary"
	"fmt"
	"io"
)

// WriteMessage writes little endian size info in 4 bytes, followed by the actual []byte.
func WriteMessage(writer io.Writer, m []byte) (err error) {
	if err = binary.Write(writer, binary.LittleEndian, int32(len(m))); err != nil {
		return fmt.Errorf("Failed to write message length: %v", err)
	}
	if _, err = writer.Write(m); err != nil {
		return fmt.Errorf("Failed to write message content: %v", err)
	}
	return
}
