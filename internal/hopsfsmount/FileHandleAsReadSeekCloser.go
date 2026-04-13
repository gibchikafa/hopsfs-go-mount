// Copyright (c) Microsoft. All rights reserved.
// Copyright (c) Hopsworks AB. All rights reserved.
// Licensed under the MIT license. See LICENSE file in the project root for details.
package hopsfsmount

import (
	"context"
	"syscall"
)

// Wraps FileHandle exposing it as ReadSeekCloser intrface
// Concurrency: not thread safe: at most on request at a time
type FileHandleAsReadSeekCloser struct {
	FileHandle *FileHandle
	Offset     int64
}

// Verify that *FileHandleAsReadSeekCloser implements ReadSeekCloser
var _ ReadSeekCloser = (*FileHandleAsReadSeekCloser)(nil)

// Creates new adapter
func NewFileHandleAsReadSeekCloser(fileHandle *FileHandle) ReadSeekCloser {
	return &FileHandleAsReadSeekCloser{FileHandle: fileHandle}
}

// Reads a chunk of data
func (fhrs *FileHandleAsReadSeekCloser) Read(buffer []byte) (int, error) {
	result, errno := fhrs.FileHandle.Read(context.Background(), buffer, fhrs.Offset)
	if errno != 0 {
		return 0, errno
	}
	data, status := result.Bytes(buffer)
	if status != 0 {
		return 0, syscall.Errno(status)
	}
	fhrs.Offset += int64(len(data))
	return len(data), nil
}

// Seeks to a given position
func (fhrs *FileHandleAsReadSeekCloser) Seek(pos int64) error {
	// Note: seek is implemented as virtual operation, error checking will happen
	// when a Read() is called after a problematic Seek()
	fhrs.Offset = pos
	return nil
}

// Returns reading position
func (fhrs *FileHandleAsReadSeekCloser) Position() (int64, error) {
	return fhrs.Offset, nil
}

// Closes the underlying file handle
func (fhrs *FileHandleAsReadSeekCloser) Close() error {
	return fhrs.FileHandle.Release(context.Background())
}
