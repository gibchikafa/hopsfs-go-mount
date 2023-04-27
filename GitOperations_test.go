// Copyright (c) Hopsworks AB. All rights reserved.
// Licensed under the MIT license. See LICENSE file in the project root for details.
package main

import (
	"bytes"
	"errors"
	"fmt"
	"github.com/go-git/go-git/v5/plumbing"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5"
	go_git_config "github.com/go-git/go-git/v5/config"
	"github.com/stretchr/testify/require"
)

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

func TestGit2(t *testing.T) {
	withMount(t, "/", func(mountPoint string, hdfsAccessor HdfsAccessor) {

		// mountPoint := "/tmp/mnt"
		// repoName := "kube-hops-chef.git"
		repoName := "hops-examples.git"
		cloneDir := "cloneDir0"

		repoPath := filepath.Join(mountPoint, cloneDir)

		//delete the dir if it already exists
		_, err := os.Stat(repoPath)
		if os.IsExist(err) {
			err := rmDir(t, repoPath)
			if err != nil {
				t.Errorf("Faile to remove  %s. Error: %v", repoPath, err)
			}
		}

		// clone repo
		loginfo(fmt.Sprintf("Cloning at path: %s ", repoPath), nil)
		gitCloneOptions := &git.CloneOptions{
			URL:               fmt.Sprintf("%s%s", "https://github.com/gibchikafa/", repoName),
			RecurseSubmodules: git.DefaultSubmoduleRecursionDepth,
			SingleBranch:      false,
		}
		repo, err := git.PlainClone(repoPath, false, gitCloneOptions)

		require.Nil(t, err)
		require.NotNil(t, repo)

		// Add a new remote, with the default fetch refspec
		remoteName := "logicalclocks"
		remoteUrl := fmt.Sprintf("%s%s", "https://github.com/logicalclocks/", repoName)
		loginfo(fmt.Sprintf("Adding remote. Remote name: %s, remote url: %s", remoteName, remoteUrl), nil)
		_, err = repo.CreateRemote(&go_git_config.RemoteConfig{
			Name: remoteName,
			URLs: []string{remoteUrl},
		})

		if err != nil {
			t.Errorf("Failed %v", err.Error())
		} else {
			loginfo(fmt.Sprintf("Successfully added remote %s. Url %s", remoteName, remoteUrl), nil)
		}

		//Get new remote list
		_, err = repo.Remotes()
		if err != nil {
			t.Errorf("Failed %v", err.Error())
		}

		//status
		_, err = repo.Worktree()
		if err != nil {
			t.Errorf("Failed %v", err.Error())
		}

		// get current branch name
		ref, err := repo.Head()
		if err != nil {
			t.Errorf("Failed %v", err.Error())
		}
		currentBranch := strings.ReplaceAll(ref.Name().String(), "refs/heads/", "")

		//pull from master
		branchName := "master"
		remoteName = "logicalclocks"
		committerName := "Admin"
		committerEmail := "admin@hopsworks.ai"

		// Fetch all remotes
		refspec := go_git_config.RefSpec("+refs/heads/" + branchName + ":refs/remotes/" + remoteName + "/" + branchName)
		fetchOptions := &git.FetchOptions{
			// Auth: &git_http.BasicAuth{
			// Username: gitUsername,
			// Password: gitToken,
			// },
			Force: true,
			RefSpecs: []go_git_config.RefSpec{
				refspec,
			},
			Progress:   os.Stdout,
			RemoteName: remoteName}
		err = repo.Fetch(fetchOptions)
		if err != nil && err.Error() != "already up-to-date" {
			t.Errorf("Failed %v", err.Error())
		}

		//set config
		cmd := "git config user.name " + "\"" + committerName + "\""
		loginfo(fmt.Sprintf("Set git user.name config command: %s", cmd), nil)
		err = ExecuteOnPath(repoPath, cmd)
		if err != nil {
			t.Errorf("Failed %v", err.Error())
		}

		cmd = "git config user.email " + "\"" + committerEmail + "\""
		loginfo(fmt.Sprintf("Set git user.email config command: %s", cmd), nil)
		err = ExecuteOnPath(repoPath, cmd)
		if err != nil {
			t.Errorf("Failed %v", err.Error())
		}

		//Apply rebase
		cmd = "git rebase "
		if branchName != "" && remoteName != "" {
			cmd = cmd + remoteName + "/" + branchName + " " + currentBranch
			loginfo(fmt.Sprintf("Applying git rebase:  `%s`", cmd), nil)

			if err = ExecuteOnPath(repoPath, cmd); err != nil && err.Error() != "already up-to-date" {
				logerror(err.Error(), nil)
				logerror("Aborting rebase", nil)
				ExecuteOnPath(repoPath, "git rebase --abort") //Noted if an error occurs the HEAD is detached
				t.Errorf("Fail %s, %v", cmd, err)
			}
		} else {
			t.Errorf("provide branch and origin")
		}

		//clean
		err = rmDir(t, repoPath)
		if err != nil {
			t.Errorf("Faile to remove  %s. Error: %v", repoPath, err)
		}
	})
}

// test operations using go-git
func TestGit3(t *testing.T) {
	withMount(t, "/", func(mountPoint string, hdfsAccessor HdfsAccessor) {
		repoName := "hops-examples.git"
		cloneDir := "cloneDir1"

		repoPath := filepath.Join(mountPoint, cloneDir)

		//delete the dir if it already exists
		_, err := os.Stat(repoPath)
		if os.IsExist(err) {
			err := rmDir(t, repoPath)
			if err != nil {
				t.Errorf("Faile to remove  %s. Error: %v", repoPath, err)
			}
		}

		// clone repo
		loginfo(fmt.Sprintf("Cloning at path: %s ", repoPath), nil)
		gitCloneOptions := &git.CloneOptions{
			URL:               fmt.Sprintf("%s%s", "https://github.com/logicalclocks/", repoName),
			RecurseSubmodules: git.DefaultSubmoduleRecursionDepth,
			SingleBranch:      false,
		}
		repo, err := git.PlainClone(repoPath, false, gitCloneOptions)
		require.Nil(t, err)
		require.NotNil(t, repo)

		// checkout to branch
		branchName := "new_test_branch"
		loginfo(fmt.Sprintf("Creating new branch: %s", branchName), nil)
		headRef, err := repo.Head()
		require.Nil(t, err)
		// check if branch exist and return proper error
		exist, _ := CheckIfBranchExits(repo, branchName)
		require.Equal(t, exist, false)
		branch := plumbing.ReferenceName(fmt.Sprintf("refs/heads/%s", branchName))
		// Create a new plumbing.HashReference object with the name of the branch
		// and the hash from the HEAD
		ref := plumbing.NewHashReference(branch, headRef.Hash())
		// The created reference is saved in the storage.
		err = repo.Storer.SetReference(ref)
		require.Nil(t, err)
	})
}

func ExecuteOnPath(path string, cmd string) error {
	loginfo(fmt.Sprintf("Executing command `%s` on path %s", cmd, path), nil)
	args := strings.Split(cmd, " ")
	c := exec.Command(args[0], args[1:]...)
	c.Dir = path
	c.Env = os.Environ()

	buf := bytes.NewBuffer(nil)
	c.Stderr = buf
	err := c.Run()
	if err != nil {
		return errors.New(err.Error() + ". " + buf.String())
	}
	return nil
}

func CheckIfBranchExits(repository *git.Repository, branchName string) (bool, error) {
	exist := false
	branches, err := repository.Branches()
	if err != nil {
		return exist, err
	}
	branches.ForEach(func(branch *plumbing.Reference) error {
		if branch.Name().Short() == branchName {
			exist = true
		}
		return nil
	})
	return exist, err
}
