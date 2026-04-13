// Copyright (c) Microsoft. All rights reserved.
// Copyright (c) Hopsworks AB. All rights reserved.
// Licensed under the MIT license. See LICENSE file in the project root for details.
package hopsfsmount

import (
	"os"
	"syscall"
	"time"

	"github.com/hanwen/go-fuse/v2/fuse"
)

// Attributes common to the file/directory HDFS nodes
type Attrs struct {
	Inode        uint64
	Name         string
	Mode         os.FileMode
	Size         uint64
	Uid          uint32
	Gid          uint32
	DFSUserName  string
	DFSGroupName string
	Mtime        time.Time
	Ctime        time.Time
	Expires      time.Time // indicates when cached attribute information expires
}

// FsInfo provides information about HDFS
type FsInfo struct {
	capacity  uint64
	used      uint64
	remaining uint64
}

// Converts Attrs datastructure into go-fuse representation.
func (attrs *Attrs) ConvertAttrToFuse(out *fuse.AttrOut) {
	out.Ino = attrs.Inode
	out.Size = attrs.Size
	// Set Blocks for du/stat to work correctly.
	// Per POSIX, st_blocks is always in 512-byte units regardless of filesystem block size.
	out.Blocks = (attrs.Size + 511) / 512
	out.Owner = fuse.Owner{Uid: attrs.Uid, Gid: attrs.Gid}
	out.SetTimes(nil, &attrs.Mtime, &attrs.Ctime)
}

func (attrs *Attrs) StableMode() uint32 {
	if (attrs.Mode & os.ModeDir) == os.ModeDir {
		return syscall.S_IFDIR
	}
	return syscall.S_IFREG
}

func (attrs *Attrs) Permissions() uint32 {
	return uint32(attrs.Mode.Perm())
}

// returns DirEntry mode for this attributes.
func (attrs *Attrs) FuseNodeType() uint32 {
	return attrs.StableMode()
}
