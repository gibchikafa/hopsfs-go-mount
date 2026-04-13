// Copyright (c) Microsoft. All rights reserved.
// Copyright (c) Hopsworks AB. All rights reserved.
// Licensed under the MIT license. See LICENSE file in the project root for details.
package hopsfsmount

import (
	"context"
	"fmt"
	"os"
	"path"
	"sync"
	"syscall"
	"time"

	"github.com/colinmarc/hdfs/v2"
	fusefs "github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
	"hopsworks.ai/hopsfsmount/internal/hopsfsmount/logger"
)

// Encapsulates state and operations for directory node on the HDFS file system
type DirINode struct {
	fusefs.Inode
	FileSystem    *FileSystem        // Pointer to the owning filesystem
	Attrs         Attrs              // Cached attributes of the directory, TODO: add TTL
	Parent        *DirINode          // Pointer to the parent directory (allows computing fully-qualified paths on demand)
	children      map[string]fusefs.InodeEmbedder // Cached directory entries
	negativeCache map[string]time.Time            // Caches "not found" results: name → expiry time
	childrenMutex sync.Mutex                      // for concurrent read and updates
	dirMutex      sync.Mutex                      // One read or write operation on a directory at a time
}

// Verify that *Dir implements necesary FUSE interfaces
var _ fusefs.InodeEmbedder = (*DirINode)(nil)
var _ fusefs.NodeGetattrer = (*DirINode)(nil)
var _ fusefs.NodeReaddirer = (*DirINode)(nil)
var _ fusefs.NodeLookuper = (*DirINode)(nil)
var _ fusefs.NodeMkdirer = (*DirINode)(nil)
var _ fusefs.NodeUnlinker = (*DirINode)(nil)
var _ fusefs.NodeRmdirer = (*DirINode)(nil)
var _ fusefs.NodeRenamer = (*DirINode)(nil)
var _ fusefs.NodeOnForgetter = (*DirINode)(nil)
var _ fusefs.NodeSymlinker = (*DirINode)(nil)
var _ fusefs.NodeReadlinker = (*DirINode)(nil)
var _ fusefs.NodeLinker = (*DirINode)(nil)
var _ fusefs.NodeCreater = (*DirINode)(nil)
var _ fusefs.NodeSetattrer = (*DirINode)(nil)
var _ fusefs.NodeFsyncer = (*DirINode)(nil)
var _ fusefs.NodeStatfser = (*DirINode)(nil)

// Returns absolute path of the dir in HDFS namespace
func (dir *DirINode) AbsolutePath() string {
	if dir.Parent == nil {
		return dir.FileSystem.SrcDir
	} else {
		return path.Join(dir.Parent.AbsolutePath(), dir.Attrs.Name)
	}
}

// Returns absolute path of the child item of this directory
func (dir *DirINode) AbsolutePathForChild(name string) string {
	return path.Join(dir.AbsolutePath(), name)
}

// Responds on FUSE request to get directory attributes.
func (dir *DirINode) Getattr(ctx context.Context, _ fusefs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	dir.lockMutex()
	defer dir.unlockMutex()

	if dir.Parent != nil && dir.FileSystem.Clock.Now().After(dir.Attrs.Expires) {
		_, err := dir.Parent.statInodeInHopsFS(ctx, GetattrDir, dir.Attrs.Name, &dir.Attrs)
		if err != nil {
			return fusefs.ToErrno(err)
		}
	} else {
		logger.Info("Stat successful. Returning from Cache ", logger.Fields{Operation: GetattrDir, Path: path.Join(dir.AbsolutePath()), FileSize: dir.Attrs.Size,
			IsDir: dir.Attrs.Mode.IsDir(), IsRegular: dir.Attrs.Mode.IsRegular()})
	}
	fillAttrOut(&dir.Attrs, out)
	return 0
}

func (dir *DirINode) getChildNode(operation, name string) fusefs.InodeEmbedder {
	dir.lockChildrenMutex()
	defer dir.unlockChildrenMutex()

	if dir.children == nil {
		dir.children = make(map[string]fusefs.InodeEmbedder)
		return nil
	}

	node := dir.children[name]
	if node != nil {
		logger.Debug("Children's List. getChildInode ", logger.Fields{Operation: operation, Parent: dir.AbsolutePath(), Child: name, NumChildren: len(dir.children)})
	} else {
		logger.Debug("Children's List. getChildInode. Not Found  ", logger.Fields{Operation: operation, Parent: dir.AbsolutePath(), Child: name, NumChildren: len(dir.children)})
	}

	return node
}

func (dir *DirINode) addOrUpdateChildInodeAttrs(operation, name string, attrs Attrs) fusefs.InodeEmbedder {
	dir.lockChildrenMutex()
	defer dir.unlockChildrenMutex()

	if dir.children == nil {
		dir.children = make(map[string]fusefs.InodeEmbedder)
	}

	if node, ok := dir.children[name]; ok {
		if fnode, ok := node.(*FileINode); ok {
			fnode.Attrs = attrs
		} else if dnode, ok := node.(*DirINode); ok {
			dnode.Attrs = attrs
		}
		logger.Debug("Children's List. addOrUpdateChildInodeAttrs. Update ", logger.Fields{Operation: operation, Parent: dir.AbsolutePath(), Child: name, NumChildren: len(dir.children)})
		return node
	} else {
		var node fusefs.InodeEmbedder
		if (attrs.Mode & os.ModeDir) == 0 {
			node = &FileINode{FileSystem: dir.FileSystem, Parent: dir, Attrs: attrs}
		} else {
			node = &DirINode{FileSystem: dir.FileSystem, Parent: dir, Attrs: attrs}
		}
		dir.children[name] = node
		logger.Debug("Children's List. addOrUpdateChildInodeAttrs. Add ", logger.Fields{Operation: operation, Parent: dir.AbsolutePath(), Child: name, NumChildren: len(dir.children)})
		return node
	}
}

func (dir *DirINode) removeChildInode(operation, name string) {
	dir.lockChildrenMutex()
	defer dir.unlockChildrenMutex()

	if dir.children != nil {
		delete(dir.children, name)
		logger.Debug("Children's List. removeChildInode ", logger.Fields{Operation: operation, Parent: dir.AbsolutePath(), Child: name, NumChildren: len(dir.children)})
	}
}

// used in rename. when an inode is moved from one dir to another
func (dir *DirINode) adoptChildInode(operation, name string, node fusefs.InodeEmbedder) {
	dir.lockChildrenMutex()
	defer dir.unlockChildrenMutex()

	if dir.children == nil {
		dir.children = make(map[string]fusefs.InodeEmbedder)
	}

	if _, ok := dir.children[name]; ok {
		logger.Debug("Children's List. Adopted inode. Replaced existing node ", logger.Fields{Operation: operation, Parent: dir.AbsolutePath(), Child: name, NumChildren: len(dir.children)})
	} else {
		logger.Debug("Children's List. Adopted inode. Added new node ", logger.Fields{Operation: operation, Parent: dir.AbsolutePath(), Child: name, NumChildren: len(dir.children)})
	}

	dir.children[name] = node
}

// Responds on FUSE request to lookup the directory.
func (dir *DirINode) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fusefs.Inode, syscall.Errno) {
	dir.lockMutex()
	defer dir.unlockMutex()

	return dir.LookupInt(ctx, Lookup, name, out)
}

func (dir *DirINode) LookupInt(ctx context.Context, opName string, name string, out *fuse.EntryOut) (*fusefs.Inode, syscall.Errno) {
	if !dir.FileSystem.IsPathAllowed(dir.AbsolutePathForChild(name)) {
		return nil, syscall.ENOENT
	}

	if node := dir.getChildNode(opName, name); node != nil {
		attrs := nodeAttributes(node)
		fillEntryOut(&attrs, out)
		if child := dir.GetChild(name); child != nil {
			return child, 0
		}
		return dir.newChildInode(ctx, node), 0
	}

	// Check negative cache before hitting the backend
	if dir.checkNegativeCache(opName, name) {
		return nil, syscall.ENOENT
	}

	var attrs Attrs
	node, err := dir.statInodeInHopsFS(ctx, opName, name, &attrs)
	if err != nil {
		return nil, fusefs.ToErrno(err)
	}
	fillEntryOut(&attrs, out)
	return dir.newChildInode(ctx, node), 0
}

// Responds on FUSE request to read directory.
func (dir *DirINode) Readdir(ctx context.Context) (fusefs.DirStream, syscall.Errno) {
	dir.lockMutex()
	defer dir.unlockMutex()

	absolutePath := dir.AbsolutePath()
	logger.Info("Read directory", logger.Fields{Operation: ReadDir, Path: absolutePath})

	allAttrs, err := dir.FileSystem.getDFSConnector().ReadDir(absolutePath)
	if err != nil {
		logger.Warn("Failed to list DFS directory", logger.Fields{Operation: ReadDir, Path: absolutePath, Error: err})
		return nil, fusefs.ToErrno(err)
	}

	entries := make([]fuse.DirEntry, 0, len(allAttrs))
	for _, a := range allAttrs {
		if dir.FileSystem.IsPathAllowed(dir.AbsolutePathForChild(a.Name)) {
			entries = append(entries, fuse.DirEntry{
				Ino:  a.Inode,
				Name: a.Name,
				Mode: a.FuseNodeType(),
			})
			// Speculatively pre-creating child Dir or File node with cached attributes,
			// since it's highly likely that we will have Lookup() call for this name
			// This is the key trick which dramatically speeds up 'ls'
			dir.addOrUpdateChildInodeAttrs(ReadDir, a.Name, a)
		}
	}
	return fusefs.NewListDirStream(entries), 0
}

// Performs Stat() query on the backend.
func (dir *DirINode) statInodeInHopsFS(_ context.Context, operation, name string, attrs *Attrs) (fusefs.InodeEmbedder, error) {

	a, err := dir.FileSystem.getDFSConnector().Stat(path.Join(dir.AbsolutePath(), name))
	if err != nil {
		logger.Info("Stat failed on backend", logger.Fields{Operation: operation, Path: path.Join(dir.AbsolutePath(), name), Error: err})
		dir.removeChildInode(operation, name)
		if err == syscall.ENOENT {
			dir.addNegativeCacheEntry(name)
		}
		return nil, err
	}
	*attrs = a

	inode := dir.addOrUpdateChildInodeAttrs(operation, name, *attrs)
	logger.Info("Stat successful on backend", logger.Fields{Operation: operation, Path: path.Join(dir.AbsolutePath(), name), FileSize: attrs.Size,
		IsDir: attrs.Mode.IsDir(), IsRegular: attrs.Mode.IsRegular()})
	return inode, nil
}

// Responds on FUSE Mkdir request.
func (dir *DirINode) Mkdir(ctx context.Context, name string, mode uint32, out *fuse.EntryOut) (*fusefs.Inode, syscall.Errno) {
	dir.lockMutex()
	defer dir.unlockMutex()

	reqMode := ComputePermissions(os.ModeDir | os.FileMode(mode))
	uid, gid := currentCallerIDs(ctx)

	// check user and group information first.
	userName, err := getUserName(uid)
	if err != nil {
		logger.Error("Unable to find user information. ", logger.Fields{Operation: Mkdir,
			Path: dir.AbsolutePathForChild(name), UID: uid, HopsFSUserName: GetConnectionUser()})
		return nil, fusefs.ToErrno(err)
	}

	groupName, err := getGroupName(dir.AbsolutePathForChild(name), gid)
	if err != nil {
		logger.Error("Unable to find group information. ", logger.Fields{Operation: Mkdir,
			Path: dir.AbsolutePathForChild(name), GID: gid,
			GetGroupFromHopsFSDatasetPath: UseGroupFromHopsFsDatasetPath})
		return nil, fusefs.ToErrno(err)
	}
	err = dir.FileSystem.getDFSConnector().MkdirWithGroup(dir.AbsolutePathForChild(name), reqMode, groupName)
	if err != nil {
		logger.Info("mkdir failed", logger.Fields{Operation: Mkdir, Path: path.Join(dir.AbsolutePath(), name), Error: err})
		return nil, fusefs.ToErrno(err)
	}
	logger.Debug("mkdir successful with group", logger.Fields{Operation: Mkdir, Path: path.Join(dir.AbsolutePath(), name), Group: groupName})

	dir.removeNegativeCacheEntry(name)
	attrs := Attrs{
		Name:         name,
		Mode:         reqMode | os.ModeDir,
		Uid:          uid,
		Gid:          gid,
		DFSUserName:  userName,
		DFSGroupName: groupName,
	}
	newNode := dir.addOrUpdateChildInodeAttrs(Mkdir, name,
		Attrs{
			Name:         name,
			Mode:         reqMode | os.ModeDir,
			Uid:          uid,
			Gid:          gid,
			DFSUserName:  userName,
			DFSGroupName: groupName,
		})
	fillEntryOut(&attrs, out)
	return dir.newChildInode(ctx, newNode), 0
}

// Responds on FUSE Create request.
func (dir *DirINode) Create(ctx context.Context, name string, flags uint32, mode uint32, out *fuse.EntryOut) (*fusefs.Inode, fusefs.FileHandle, uint32, syscall.Errno) {
	dir.lockMutex()
	defer dir.unlockMutex()

	reqMode := ComputePermissions(os.FileMode(mode))
	logger.Info("Creating a new file", logger.Fields{Operation: Create, Path: dir.AbsolutePathForChild(name), Mode: reqMode, Flags: flags})
	uid, gid := currentCallerIDs(ctx)

	// first determine the usename and grup name for the new file
	userName, err := getUserName(uid)
	if err != nil {
		logger.Error("Unable to find user information. ", logger.Fields{Operation: Create,
			Path: dir.AbsolutePathForChild(name), UID: uid, HopsFSUserName: GetConnectionUser()})
		return nil, nil, 0, fusefs.ToErrno(err)
	}

	groupName, err := getGroupName(dir.AbsolutePathForChild(name), gid)
	if err != nil {
		logger.Error("Unable to find group information. ", logger.Fields{Operation: Create,
			Path: dir.AbsolutePathForChild(name), GID: gid,
			GetGroupFromHopsFSDatasetPath: UseGroupFromHopsFsDatasetPath})
		return nil, nil, 0, fusefs.ToErrno(err)
	}

	newFileAttrs := Attrs{
		Name:         name,
		Mode:         reqMode,
		Uid:          uid,
		Gid:          gid,
		DFSUserName:  userName,
		DFSGroupName: groupName,
	}

	dir.removeNegativeCacheEntry(name)
	file := dir.addOrUpdateChildInodeAttrs(Create, name, newFileAttrs).(*FileINode)
	handle, err := file.NewFileHandle(false, flags)
	if err != nil {
		logger.Error("File creation failed", logger.Fields{Operation: Create, Path: dir.AbsolutePathForChild(name), Mode: reqMode, Flags: flags, Error: err})
		dir.removeChildInode(Create, name)
		return nil, nil, 0, fusefs.ToErrno(err)
	}
	// Note: handle is already added to activeHandles inside NewFileHandle
	// File created with groupname parameter - no chown needed
	logger.Debug("File created with group", logger.Fields{
		Operation: Create,
		Path:      dir.AbsolutePathForChild(name),
		User:      userName,
		Group:     groupName,
	})

	//update the attributes of the file now
	node, err := dir.statInodeInHopsFS(ctx, Create, file.Attrs.Name, &file.Attrs)
	if err != nil {
		dir.removeChildInode(Create, name)
		return nil, nil, 0, fusefs.ToErrno(err)
	}

	fillEntryOut(&file.Attrs, out)
	fuseFlags := uint32(0)
	if !EnablePageCache {
		fuseFlags = fuse.FOPEN_DIRECT_IO
	}
	return dir.newChildInode(ctx, node), handle, fuseFlags, 0
}

func (dir *DirINode) removeName(name string) syscall.Errno {
	dir.lockMutex()
	defer dir.unlockMutex()

	path := dir.AbsolutePathForChild(name)
	logger.Debug("Removing path", logger.Fields{Operation: Remove, Path: path})
	err := dir.FileSystem.getDFSConnector().Remove(path)
	if err == nil {
		dir.removeChildInode(Remove, name)
		// Invalidate staging file cache for the removed path
		if StagingCache != nil {
			StagingCache.Remove(path)
		}
		logger.Info("Removed path", logger.Fields{Operation: Remove, Path: path})
	} else {
		logger.Warn("Failed to remove path", logger.Fields{Operation: Remove, Path: path, Error: err})
	}
	return fusefs.ToErrno(err)
}

func (dir *DirINode) Unlink(ctx context.Context, name string) syscall.Errno {
	return dir.removeName(name)
}

func (dir *DirINode) Rmdir(ctx context.Context, name string) syscall.Errno {
	return dir.removeName(name)
}

// Responds on FUSE Rename request.
func (srcParent *DirINode) Rename(ctx context.Context, oldName string, newParent fusefs.InodeEmbedder, newName string, flags uint32) syscall.Errno {
	srcParent.lockMutex()
	defer srcParent.unlockMutex()

	if flags&fusefs.RENAME_EXCHANGE == fusefs.RENAME_EXCHANGE ||
		flags&0x4 == 0x4 {
		logger.Error("Rename. Unsupported Flags ", logger.Fields{Operation: Rename, Flags: flags})
		return syscall.EINVAL
	}

	options := hdfs.RENAME_OPTION_NONE
	if flags&0x1 == 0x1 {
		options = options | hdfs.RENAME_NOREPLACE
	}

	dstParentDir := newParent.(*DirINode)
	oldPath := srcParent.AbsolutePathForChild(oldName)
	newPath := dstParentDir.AbsolutePathForChild(newName)
	logger.Debug("Renaming", logger.Fields{Operation: Rename, From: oldPath, To: newPath})

	srcInode := srcParent.getChildNode(Rename, oldName)
	if srcInode == nil {
		var out fuse.EntryOut
		_, errno := srcParent.LookupInt(ctx, Rename, oldName, &out)
		if errno != 0 {
			logger.Error("Rename failed. Src Inode not found", logger.Fields{Operation: Rename, From: oldPath, To: newPath})
			return errno
		}
		srcInode = srcParent.getChildNode(Rename, oldName)
	}

	dstInode := dstParentDir.getChildNode(Rename, newName)
	if dstInode == nil {
		var out fuse.EntryOut
		if _, errno := dstParentDir.LookupInt(ctx, Rename, newName, &out); errno == 0 {
			dstInode = dstParentDir.getChildNode(Rename, newName)
		}
	}

	err := srcParent.FileSystem.getDFSConnector().Rename2(oldPath, newPath, hdfs.RenameOptions(options))
	if err != nil {
		logger.Error("Rename failed at the backend", logger.Fields{Operation: Rename, From: oldPath, To: newPath, Error: err})
		return fusefs.ToErrno(err)
	}

	// Transfer staging file cache entry from old path to new path if it exists
	if StagingCache != nil {
		StagingCache.Rename(oldPath, newPath)
	}

	if srcInode != nil {
		srcParent.removeChildInode(Rename, oldName)
	}
	if dstInode != nil {
		dstParentDir.removeChildInode(Rename, newName)
	}
	dstParentDir.removeNegativeCacheEntry(newName)

	if fnode, ok := srcInode.(*FileINode); ok {
		logger.Trace("Rename src is file", logger.Fields{Operation: Rename, From: oldPath, To: newPath})
		fnode.Attrs.Name = newName
		fnode.Parent = dstParentDir
		dstParentDir.adoptChildInode(Rename, newName, fnode)
	}
	if dnode, ok := srcInode.(*DirINode); ok {
		logger.Trace("Rename src is dir", logger.Fields{Operation: Rename, From: oldPath, To: newPath})
		dnode.Attrs.Name = newName
		dnode.Parent = dstParentDir
		dstParentDir.adoptChildInode(Rename, newName, dnode)
	}

	logger.Info("Renamed", logger.Fields{Operation: Rename, From: oldPath, To: newPath})
	return 0
}

// Responds on FUSE Chmod request.
func (dir *DirINode) Setattr(ctx context.Context, _ fusefs.FileHandle, req *fuse.SetAttrIn, resp *fuse.AttrOut) syscall.Errno {
	dir.lockMutex()
	defer dir.unlockMutex()

	path := dir.AbsolutePath()

	if _, ok := req.GetSize(); ok {
		logger.Error(fmt.Sprintf("Unsupported operation. Can not set size of a directory"), logger.Fields{Operation: Chmod, Path: path})
		return syscall.ENOTSUP
	}

	if mode, ok := req.GetMode(); ok {
		if err := ChmodOp(&dir.Attrs, dir.FileSystem, path, os.ModeDir|os.FileMode(mode), resp); err != nil {
			logger.Warn("Setattr (chmod) failed. ", logger.Fields{Operation: Chmod, Path: path, Mode: mode})
			return fusefs.ToErrno(err)
		}
	}

	var uidPtr, gidPtr *uint32
	if uid, ok := req.GetUID(); ok {
		uidPtr = &uid
	}
	if gid, ok := req.GetGID(); ok {
		gidPtr = &gid
	}
	if uidPtr != nil || gidPtr != nil {
		if err := SetAttrChownOp(&dir.Attrs, dir.FileSystem, path, uidPtr, gidPtr, resp); err != nil {
			logger.Warn("Setattr (chown/chgrp )failed", logger.Fields{Operation: Chmod, Path: path})
			return fusefs.ToErrno(err)
		}
	}

	if err := UpdateTS(&dir.Attrs, dir.FileSystem, path, req, resp); err != nil {
		return fusefs.ToErrno(err)
	}

	return 0
}

func (dir *DirINode) OnForget() {
	dir.lockMutex()
	defer dir.unlockMutex()
}

func (dir *DirINode) Symlink(ctx context.Context, target, name string, out *fuse.EntryOut) (*fusefs.Inode, syscall.Errno) {
	logger.Error("Unsupported Symlink operation.", logger.Fields{Operation: Symlink, Path: dir.AbsolutePath()})
	return nil, syscall.ENOTSUP
}

func (dir *DirINode) Readlink(ctx context.Context) ([]byte, syscall.Errno) {
	logger.Error("Unsupported Readlink operation.", logger.Fields{Operation: ReadLink, Path: dir.AbsolutePath()})
	return nil, syscall.ENOTSUP
}

func (dir *DirINode) Link(ctx context.Context, old fusefs.InodeEmbedder, name string, out *fuse.EntryOut) (*fusefs.Inode, syscall.Errno) {
	logger.Error("Unsupported Link operation.", logger.Fields{Operation: Link, Path: dir.AbsolutePath()})
	return nil, syscall.ENOTSUP
}

// Synchronize directory contents.
// All dir operations are first performed on the backend. So no-op.
func (dir *DirINode) Fsync(ctx context.Context, _ fusefs.FileHandle, flags uint32) syscall.Errno {
	logger.Info("Fsync called on Dir ", logger.Fields{Operation: Fsync, Path: dir.AbsolutePath(), Flags: flags})
	return 0
}

func (dir *DirINode) Statfs(ctx context.Context, out *fuse.StatfsOut) syscall.Errno {
	fsInfo, err := dir.FileSystem.getDFSConnector().StatFs()
	if err != nil {
		logger.Warn("Stat DFS failed", logger.Fields{Operation: StatFS, Error: err})
		return fusefs.ToErrno(err)
	}
	out.Bsize = 1024
	out.Bfree = fsInfo.remaining / uint64(out.Bsize)
	out.Bavail = out.Bfree
	out.Blocks = fsInfo.capacity / uint64(out.Bsize)
	return 0
}

func (dir *DirINode) newChildInode(ctx context.Context, node fusefs.InodeEmbedder) *fusefs.Inode {
	name := nodeAttributes(node).Name
	if child := dir.GetChild(name); child != nil {
		return child
	}
	attrs := nodeAttributes(node)
	return dir.NewInode(ctx, node, fusefs.StableAttr{Mode: attrs.StableMode(), Ino: attrs.Inode})
}

func nodeAttributes(node fusefs.InodeEmbedder) Attrs {
	switch n := node.(type) {
	case *DirINode:
		return n.Attrs
	case *FileINode:
		return n.Attrs
	default:
		return Attrs{}
	}
}

func fillEntryOut(attrs *Attrs, out *fuse.EntryOut) {
	out.Ino = attrs.Inode
	out.Mode = attrs.Permissions()
	out.Size = attrs.Size
	out.Blocks = (attrs.Size + 511) / 512
	out.Owner = fuse.Owner{Uid: attrs.Uid, Gid: attrs.Gid}
	out.SetTimes(nil, &attrs.Mtime, &attrs.Ctime)
	out.SetEntryTimeout(CacheAttrsTimeDuration)
	out.SetAttrTimeout(CacheAttrsTimeDuration)
}

func currentCallerIDs(ctx context.Context) (uint32, uint32) {
	if caller, ok := fuse.FromContext(ctx); ok && caller != nil {
		return caller.Uid, caller.Gid
	}
	owner := fuse.CurrentOwner()
	return owner.Uid, owner.Gid
}
// checkNegativeCache returns true if the name is in the negative cache and not expired.
// Must NOT hold childrenMutex when calling this.
func (dir *DirINode) checkNegativeCache(operation, name string) bool {
	dir.lockChildrenMutex()
	defer dir.unlockChildrenMutex()

	if dir.negativeCache == nil {
		return false
	}

	expiry, ok := dir.negativeCache[name]
	if !ok {
		return false
	}

	if dir.FileSystem.Clock.Now().After(expiry) {
		delete(dir.negativeCache, name)
		return false
	}

	logger.Debug("Negative cache hit", logger.Fields{Operation: operation, Parent: dir.AbsolutePath(), Child: name})
	return true
}

// addNegativeCacheEntry adds a name to the negative cache with TTL = CacheAttrsTimeDuration.
// Must NOT hold childrenMutex when calling this.
func (dir *DirINode) addNegativeCacheEntry(name string) {
	dir.lockChildrenMutex()
	defer dir.unlockChildrenMutex()

	if dir.negativeCache == nil {
		dir.negativeCache = make(map[string]time.Time)
	}

	dir.negativeCache[name] = dir.FileSystem.Clock.Now().Add(CacheAttrsTimeDuration)
}

// removeNegativeCacheEntry removes a name from the negative cache.
// Must NOT hold childrenMutex when calling this.
func (dir *DirINode) removeNegativeCacheEntry(name string) {
	dir.lockChildrenMutex()
	defer dir.unlockChildrenMutex()

	if dir.negativeCache != nil {
		delete(dir.negativeCache, name)
	}
}

func (dir *DirINode) lockMutex() {
	dir.dirMutex.Lock()
}

func (dir *DirINode) unlockMutex() {
	dir.dirMutex.Unlock()
}

func (dir *DirINode) lockChildrenMutex() {
	dir.childrenMutex.Lock()
}

func (dir *DirINode) unlockChildrenMutex() {
	dir.childrenMutex.Unlock()
}
