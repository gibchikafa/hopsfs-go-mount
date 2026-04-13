// Copyright (c) Hopsworks AB. All rights reserved.
// Licensed under the MIT license. See LICENSE file in the project root for details.
package hopsfsmount

import (
	"context"
	"io"
	"sync"
	"syscall"

	fusefs "github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
	"hopsworks.ai/hopsfsmount/internal/hopsfsmount/logger"
)

// Represents a handle to an open file
type FileHandle struct {
	File              *FileINode
	mutex             sync.Mutex // all operations on the handle are serialized to simplify invariants
	fileFlags         uint32     // flags used to create/open the file
	tatalBytesRead    int64
	totalBytesWritten int64
	fhID              uint64 // file handle id. for debugging only
}

// Verify that *FileHandle implements necesary FUSE interfaces
var _ fusefs.FileHandle = (*FileHandle)(nil)
var _ fusefs.FileReader = (*FileHandle)(nil)
var _ fusefs.FileReleaser = (*FileHandle)(nil)
var _ fusefs.FileWriter = (*FileHandle)(nil)
var _ fusefs.FileFsyncer = (*FileHandle)(nil)
var _ fusefs.FileFlusher = (*FileHandle)(nil)
var _ fusefs.FileGetattrer = (*FileHandle)(nil)

func (fh *FileHandle) dataChanged() bool {
	if fh.totalBytesWritten > 0 {
		return true
	} else {
		return false
	}
}

// Lock order: dataMutex (3) → fileHandleMutex (1) via upgradeHandleForWriting
func (fh *FileHandle) Truncate(size int64) error {
	fh.File.lockData()
	defer fh.File.unlockData()

	// as an optimization the file is initially opened in readonly mode
	err := fh.File.upgradeHandleForWriting(fh, Truncate)
	if err != nil {
		return err
	}

	sizeChanged, err := fh.File.fileProxy.Truncate(size)
	if err != nil {
		logger.Error("Failed to truncate file", fh.logInfo(logger.Fields{Operation: Truncate, Bytes: size, Error: err}))
		return err
	}

	fh.totalBytesWritten += sizeChanged

	// Mark file as dirty (protected by dataMutex we're holding)
	fh.File.markDirty()

	logger.Info("Truncated file", fh.logInfo(logger.Fields{Operation: Truncate, Bytes: size}))
	return nil
}

// Returns attributes of the file associated with this handle.
// Delegates to File.Getattr which handles its own locking (fileMutex → fileHandleMutex)
// Note: No dataMutex here to avoid invalid lock order dataMutex (3) → fileMutex (2)
func (fh *FileHandle) Getattr(ctx context.Context, out *fuse.AttrOut) syscall.Errno {
	return fh.File.Getattr(ctx, fh, out)
}

// Responds to FUSE Read request
// Lock order: dataMutex (3) alone
func (fh *FileHandle) Read(ctx context.Context, dest []byte, off int64) (fuse.ReadResult, syscall.Errno) {
	fh.File.lockData()
	defer fh.File.unlockData()

	nr, err := fh.File.fileProxy.ReadAt(dest, off)
	fh.tatalBytesRead += int64(nr)

	if err != nil {
		if err == io.EOF {
			logger.Debug("Completed reading", fh.logInfo(logger.Fields{Operation: Read, Error: err, Bytes: nr}))
			if nr >= 0 {
				return fuse.ReadResultData(dest[:nr]), 0
			} else {
				return nil, fusefs.ToErrno(err)
			}
		} else {
			logger.Error("Failed to read", fh.logInfo(logger.Fields{Operation: Read, Error: err, Bytes: nr}))
			return nil, fusefs.ToErrno(err)
		}
	}
	return fuse.ReadResultData(dest[:nr]), 0
}

// Responds to FUSE Write request
// Lock order: dataMutex (3) → fileHandleMutex (1) via upgradeHandleForWriting
func (fh *FileHandle) Write(ctx context.Context, data []byte, off int64) (uint32, syscall.Errno) {
	fh.File.lockData()
	defer fh.File.unlockData()

	// as an optimization the file is initially opened in readonly mode
	fh.File.upgradeHandleForWriting(fh, Write)

	nw, err := fh.File.fileProxy.WriteAt(data, off)
	fh.totalBytesWritten += int64(nw)
	if err != nil {
		logger.Error("Failed to write to staging file", fh.logInfo(logger.Fields{Operation: Write, Error: err}))
		return uint32(nw), fusefs.ToErrno(err)
	}

	// Mark file as dirty (protected by dataMutex we're holding)
	fh.File.markDirty()

	logger.Trace("Write data to staging file", fh.logInfo(logger.Fields{Operation: Write, Bytes: nw, ReqOffset: off}))
	return uint32(nw), 0
}

// Responds to the FUSE Flush request.
// IMPORTANT: Data must be uploaded to DFS in Flush, not in Release (close).
// The FUSE library handles each request in a separate goroutine, and the kernel
// does not wait for Release to complete before returning from the close() syscall.
// Only Flush is synchronous - the kernel waits for Flush to complete before
// returning from close(). If we defer upload to Release, subsequent file operations
// may start before the previous file's upload is complete.
// Lock order: dataMutex (3) → fileHandleMutex (1) via flushToDFS
func (fh *FileHandle) Flush(ctx context.Context) syscall.Errno {
	fh.File.lockData()
	defer fh.File.unlockData()
	return fusefs.ToErrno(fh.File.flushToDFS(Flush))
}

// Responds to the FUSE Fsync request
// Lock order: dataMutex (3) → fileHandleMutex (1) via flushToDFS
func (fh *FileHandle) Fsync(ctx context.Context, _ uint32) syscall.Errno {
	// If delaySyncUntilClose is enabled, skip fsync until file close
	if fh.File.FileSystem.DelaySyncUntilClose {
		logger.Debug("Fsync deferred until close", fh.logInfo(logger.Fields{Operation: Fsync}))
		return 0
	}

	fh.File.lockData()
	defer fh.File.unlockData()
	return fusefs.ToErrno(fh.File.flushToDFS(Fsync))
}

// Closes the handle
// NOTE: Do NOT call flushToDFS here! Data must be uploaded in Flush(), not Release().
// Release() is asynchronous - the kernel does NOT wait for it to complete before
// returning from close(). Only Flush() is synchronous and guarantees data is uploaded
// before the close() syscall returns. Calling flushToDFS here would mean retries could
// happen after close() returns, breaking application expectations.
// Lock order: dataMutex (3) → fileHandleMutex (1) via RemoveHandle
func (fh *FileHandle) Release(_ context.Context) syscall.Errno {
	fh.File.lockData()
	defer fh.File.unlockData()

	//close the file handle if it is the last handle
	fh.File.InvalidateMetadataCache()
	fh.File.RemoveHandle(fh)

	logger.Info("Closed file handle ", fh.logInfo(logger.Fields{Operation: Close, Flags: fh.fileFlags, TotalBytesRead: fh.tatalBytesRead, TotalBytesWritten: fh.totalBytesWritten}))
	return 0
}

func (fh *FileHandle) logInfo(fields logger.Fields) logger.Fields {
	f := logger.Fields{FileHandleID: fh.fhID, Path: fh.File.AbsolutePath()}
	for k, e := range fields {
		f[k] = e
	}
	return f
}
