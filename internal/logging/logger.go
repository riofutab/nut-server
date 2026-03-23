package logging

import (
	"io"
	"log"
	"os"
)

func New(service string) *log.Logger {
	return log.New(os.Stdout, service+" ", log.LstdFlags|log.Lmicroseconds)
}

func NewWithWriter(service string, writer io.Writer) *log.Logger {
	return log.New(writer, service+" ", log.LstdFlags|log.Lmicroseconds)
}
