// Copyright (c) Hopsworks AB. All rights reserved.
// Licensed under the MIT license. See LICENSE file in the project root for details.
package main

import (
	"fmt"
	"io/fs"
	"io/ioutil"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"bazil.org/fuse/fs/fstestutil"
)

func TestReadWriteEmptyFile(t *testing.T) {

	withMount(t, "/", func(mountPoint string, hdfsAccessor HdfsAccessor) {
		//create a file, make sure that use and group information is correct
		r := rand.New(rand.NewSource(time.Now().Local().Unix()))
		for i := 0; i < 10; i++ {
			testFile := filepath.Join(mountPoint, fmt.Sprintf("somefile_%d", r.Int()))
			os.Remove(testFile)

			file, err := os.Create(testFile)
			if err != nil {
				t.Fatalf("Unable to create a new file")
			}

			file.WriteString("test")
			err = file.Close()
			if err != nil {
				t.Fatalf("Close failed")
			}
			os.Remove(testFile)
		}
		logdebug("Done", nil)
	})
}

func TestSimple(t *testing.T) {

	withMount(t, "/", func(mountPoint string, hdfsAccessor HdfsAccessor) {
		for i := 0; i < 3; i++ {
			testFile := filepath.Join(mountPoint, fmt.Sprintf("somefile_%d", i))
			os.Remove(testFile)
			loginfo(fmt.Sprintf("New file: %s", testFile), nil)
			createFile(t, testFile, "some data")
			os.Remove(testFile)
		}
	})
}

func TestTruncate(t *testing.T) {

	withMount(t, "/", func(mountPoint string, hdfsAccessor HdfsAccessor) {
		//create a file, make sure that use and group information is correct
		testFile := filepath.Join(mountPoint, "somefile")
		os.Remove(testFile)

		loginfo(fmt.Sprintf("New file: %s", testFile), nil)
		data1 := "123456790"
		data2 := "abcde"
		createFile(t, testFile, data1)
		fi, _ := os.Stat(testFile)
		fileSize := fi.Size()

		if int(fileSize) != len(data1) {
			t.Errorf("Invalid file size. Expecting: %d Got: %d", len(data1), fileSize)
		}

		createFile(t, testFile, data2) // truncates if file already exists
		fi, _ = os.Stat(testFile)
		fileSize = fi.Size()

		if int(fileSize) != len(data2) {
			t.Errorf("Invalid file size. Expecting: %d Got: %d", len(data2), fileSize)
		}

		os.Remove(testFile)
	})
}

func TestTruncateGreaterLength(t *testing.T) {

	withMount(t, "/", func(mountPoint string, hdfsAccessor HdfsAccessor) {
		//create a file, make sure that use and group information is correct
		testFile1 := filepath.Join(mountPoint, "somefile1")
		os.Remove(testFile1)
		truncateLen := int64(1024 * 1024)

		file, err := os.Create(testFile1)
		if err != nil {
			t.Fatalf("Unable to create a new file")
		}

		stat, err := file.Stat()
		if err != nil {
			t.Fatalf("Unable to stat test file")
		}

		if stat.Size() != 0 {
			t.Fatalf("Wrong file size. Expecting: 0. Got: %d ", stat.Size())
		}

		err = file.Truncate(truncateLen)
		if err != nil {
			t.Fatalf("Truncate failed")
		}

		err = file.Close()
		if err != nil {
			t.Fatalf("Close failed")
		}

		fileReader, err := os.Open(testFile1)
		if err != nil {
			t.Fatalf("File open failed")
		}

		buffer := make([]byte, truncateLen)
		lenRead, err := fileReader.Read(buffer)
		if err != nil {
			t.Fatalf("File read failed")
		}

		if lenRead != int(truncateLen) {
			t.Fatalf("Expecting %d bytes to read. Got: %d", truncateLen, lenRead)
		}

		err = fileReader.Close()
		if err != nil {
			t.Fatalf("File close failed")
		}

		os.Remove(testFile1)
		logdebug("Done", nil)
	})
}

// testing multiple read write clients perfile
func TestMultipleRWCllients(t *testing.T) {

	withMount(t, "/", func(mountPoint string, hdfsAccessor HdfsAccessor) {
		//create a file, make sure that use and group information is correct
		// mountPoint = "/tmp"
		testFile1 := filepath.Join(mountPoint, "somefile")
		testFile2 := filepath.Join(mountPoint, "somefile.bak")
		loginfo(fmt.Sprintf("New file: %s", testFile1), nil)
		createFile(t, testFile1, "initial data\nadsf\n")

		c1, _ := os.OpenFile(testFile1, os.O_RDWR, 0600)
		c2, _ := os.OpenFile(testFile1, os.O_RDWR, 0600)
		c3, _ := os.OpenFile(testFile1, os.O_RDWR, 0600)

		c1.WriteString("First client\n")
		c1.Close()

		os.Rename(testFile1, testFile2)

		c2.WriteString("Second client\nSecond client\n")
		c3.WriteString("Third client\nThird client\nThird Client\n")
		c2.Close()
		c3.Close()

		c5, err := os.Open(testFile2)

		if err != nil {
			t.Error("The file should have opened successfully")
		} else {
			loginfo("File opened successfully", nil)
			buffer := make([]byte, 1024)
			c5.Read(buffer)
			//fmt.Printf("%s", buffer)
		}
		c5.Close()

		os.Remove(testFile1)
		os.Remove(testFile2)
	})
}

func TestMountSubDir(t *testing.T) {
	//mount and create some files and dirs
	dirs := 5
	filesPdir := 3
	withMount(t, "/", func(mountPoint string, hdfsAccessor HdfsAccessor) {
		//create some directories and files
		for i := 0; i < dirs; i++ {
			dir := filepath.Join(mountPoint, "dir"+strconv.Itoa(i))
			mkdir(t, dir)
			for j := 0; j < filesPdir; j++ {
				f := filepath.Join(dir, "file"+strconv.Itoa(j))
				createFile(t, f, "initial data")
			}
		}

		content := listDir(t, mountPoint)
		if len(content) < dirs {
			t.Errorf("Failed. Expected >= %d, Got %d", dirs, len(content))
		}
	})

	// remount only one dir
	withMount(t, "/dir1", func(mountPoint string, hdfsAccessor HdfsAccessor) {
		content := listDir(t, mountPoint)
		if len(content) != filesPdir {
			t.Errorf("Failed. Expected == %d, Got %d ", filesPdir, len(content))
			for _, ent := range content {
				loginfo(fmt.Sprintf("file/dir %s", ent.Name()), nil)
			}
		}
	})

	// remount every thing for cleanup
	withMount(t, "/", func(mountPoint string, hdfsAccessor HdfsAccessor) {
		//delete all the files and dirs created in this test
		for i := 0; i < dirs; i++ {
			dir := filepath.Join(mountPoint, "dir"+strconv.Itoa(i))
			for j := 0; j < filesPdir; j++ {
				f := filepath.Join(dir, "file"+strconv.Itoa(j))
				rmFile(t, f)
			}
			rmFile(t, dir)
		}
	})
}

func TestGitClone(t *testing.T) {
	withMount(t, "/", func(mountPoint string, hdfsAccessor HdfsAccessor) {

		cloneDir := "cloneDir"
		fullPath := filepath.Join(mountPoint, cloneDir)

		//delete the dir if it already exists
		_, err := os.Stat(fullPath)
		if os.IsExist(err) {
			err := rmDir(t, fullPath)
			if err != nil {
				t.Errorf("Faile to remove  %s. Error: %v", fullPath, err)
			}
		}

		_, err = exec.Command("git", "clone", "https://github.com/logicalclocks/ndb-chef", fullPath).Output()
		if err != nil {
			t.Errorf("Unable to clone the repo. Error: %v", err)
		}

		//clean
		err = rmDir(t, fullPath)
		if err != nil {
			t.Errorf("Faile to remove  %s. Error: %v", fullPath, err)
		}
	})
}

func withMount(t testing.TB, srcDir string, fn func(mntPath string, hdfsAccessor HdfsAccessor)) {
	t.Helper()
	//initLogger("debug", false, "")
	hdfsAccessor, _ := NewHdfsAccessor("localhost:8020", WallClock{}, TLSConfig{TLS: false}, "hdfs")
	err := hdfsAccessor.EnsureConnected()
	if err != nil {
		t.Fatalf(fmt.Sprintf("Error/NewHdfsAccessor: %v ", err), nil)
	}

	// Wrapping with FaultTolerantHdfsAccessor
	retryPolicy := NewDefaultRetryPolicy(WallClock{})
	retryPolicy.MaxAttempts = 1 // for quick failure
	ftHdfsAccessor := NewFaultTolerantHdfsAccessor(hdfsAccessor, retryPolicy)

	// Creating the virtual file system
	fileSystem, err := NewFileSystem([]HdfsAccessor{ftHdfsAccessor}, srcDir, []string{"*"}, false, retryPolicy, WallClock{})
	if err != nil {
		t.Fatalf(fmt.Sprintf("Error/NewFileSystem: %v ", err), nil)
	}

	mountOptions := getMountOptions(false)
	mnt, err := fstestutil.MountedT(t, fileSystem, nil, mountOptions...)
	if err != nil {
		t.Fatal(fmt.Sprintf("Unable to mount fs: Error %v", err), nil)

	}
	defer mnt.Close()
	loginfo(fmt.Sprintf("Connected to HopsFS. Mount point is %s", mnt.Dir), nil)
	fn(mnt.Dir, hdfsAccessor)
}

func mkdir(t testing.TB, dir string) {
	t.Helper()
	err := os.Mkdir(dir, 0700)
	if err != nil {
		t.Errorf("Faile to create directory %s. Error: %v", dir, err)
	}

}

func createFile(t testing.TB, filePath string, data string) {
	t.Helper()
	out, err := os.Create(filePath)
	if err != nil {
		t.Errorf("Faile to create test file %s. Error: %v", filePath, err)
	}
	out.WriteString(data)
	out.Close()
}

func listDir(t testing.TB, dir string) []fs.FileInfo {
	t.Helper()
	content, err := ioutil.ReadDir(dir)
	if err != nil {
		t.Errorf("Faile to list dir %s. Error: %v", dir, err)
	}
	return content
}

func rmFile(t testing.TB, path string) {
	t.Helper()
	err := os.Remove(path)
	if err != nil {
		t.Errorf("Faile to remove  %s. Error: %v", path, err)
	}
}

func rmDir(t testing.TB, dir string) error {
	t.Helper()
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()

	names, err := d.Readdirnames(-1)
	if err != nil {
		return err
	}

	for _, name := range names {
		err = os.RemoveAll(filepath.Join(dir, name))
		if err != nil {
			return err
		}
	}

	err = os.Remove(dir)
	if err != nil {
		return err
	}

	return nil
}
