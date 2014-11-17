/*
Terminology

Example git-hooks directory layout:

	githooks
	├── commit-msg
	│   └── signed-off-by
	└── pre-commit
		└── bsd

trigger: pre-commit
hook: bsd
*/
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/blang/semver"
	"github.com/cattail/go-exclude"
	"github.com/codegangsta/cli"
	"github.com/google/go-github/github"
	"github.com/mitchellh/go-homedir"
	. "github.com/tj/go-debug"
	"github.com/wsxiaoys/terminal/color"
	"io"
	"io/ioutil"
	"net/http"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
)

var VERSION = "v0.7.1"
var NAME = "git-hooks"
var TRIGGERS = [...]string{"applypatch-msg", "commit-msg", "post-applypatch", "post-checkout", "post-commit", "post-merge", "post-receive", "pre-applypatch", "pre-auto-gc", "pre-commit", "prepare-commit-msg", "pre-rebase", "pre-receive", "update", "pre-push"}

var CONTRIB_PATH = ".hooks"

var tplPreInstall = `#!/usr/bin/env bash
echo \"git hooks not installed in this repository.  Run 'git hooks --install' to install it or 'git hooks -h' for more information.\"`
var tplPostInstall = `#!/usr/bin/env bash
git-hooks run "$0" "$@"`

var logger = struct {
	Error   func(...interface{})
	Warn    func(...interface{})
	Info    func(...interface{})
	Errorln func(...interface{})
	Warnln  func(...interface{})
	Infoln  func(...interface{})
}{
	Error: func(msgs ...interface{}) {
		msgs = append([]interface{}{"@r"}, msgs...)
		color.Print(msgs...)
		os.Exit(1)
	},
	Warn: func(msgs ...interface{}) {
		msgs = append([]interface{}{"@y"}, msgs...)
		color.Print(msgs...)
	},
	Info: func(msgs ...interface{}) {
		color.Print(msgs...)
	},
	Errorln: func(msgs ...interface{}) {
		msgs = append([]interface{}{"@r"}, msgs...)
		color.Println(msgs...)
	},
	Warnln: func(msgs ...interface{}) {
		msgs = append([]interface{}{"@y"}, msgs...)
		color.Println(msgs...)
	},
	Infoln: func(msgs ...interface{}) {
		color.Println(msgs...)
	},
}

var debug = Debug("main")

func main() {
	app := cli.NewApp()
	app.Name = NAME
	app.Usage = "tool to manage project, user, and global Git hooks"
	app.Version = VERSION
	app.Action = bind(list)
	app.Commands = []cli.Command{
		{
			Name:      "install",
			ShortName: "i",
			Usage:     "Tell repo to use git-hooks by replace existing hooks with a call to git-hooks. Old hooks will be reserved in hooks.old",
			Action:    bind(install, true),
		},
		{
			Name:   "uninstall",
			Usage:  "Stop using git-hooks and restore old hooks",
			Action: bind(uninstall),
		},
		{
			Name:   "install-global",
			Usage:  "Whenever a git repository is created or cloned user will be remind to install git-hooks",
			Action: bind(installGlobal),
		},
		{
			Name:   "uninstall-global",
			Usage:  "Turn off the global reminder",
			Action: bind(uninstallGlobal),
		},
		{
			Name:   "update",
			Usage:  "Check and update git-hooks",
			Action: bind(update),
		},
		{
			Name:  "run",
			Usage: "Run hooks",
			Action: func(c *cli.Context) {
				run(c.Args()...)
			},
		},
		{
			Name:      "identity",
			ShortName: "id",
			Usage:     "Repo identity",
			Action:    bind(identity),
		},
	}

	app.Run(os.Args)
}

// List directory base hooks and configuration file based hooks
func list() {
	root, err := getGitRepoRoot()
	if err != nil {
		logger.Infoln("Current directory is not a git repo")
	} else {
		preCommitHook := filepath.Join(root, ".git/hooks/pre-commit")
		hook, err := ioutil.ReadFile(preCommitHook)
		if err == nil && strings.EqualFold(string(hook), tplPostInstall) {
			logger.Infoln("Git hooks ARE installed in this repository.")
		} else {
			logger.Infoln("Git hooks are NOT installed in this repository. (Run 'git hooks install' to install it)")
		}
	}

	for scope, dir := range hookDirs() {
		logger.Infoln(scope + " hooks")
		config, err := listHooksInDir(scope, dir)
		if err == nil {
			for trigger, hooks := range config {
				logger.Infoln("  " + trigger)
				for _, hook := range hooks {
					logger.Infoln("    - " + hook)
				}
			}
			logger.Infoln()
		}
	}

	logger.Infoln("Community hooks")
	for scope, configPath := range hookConfigs() {
		logger.Infoln(scope + " hooks")
		config, err := listHooksInConfig(configPath)
		if err == nil {
			for trigger, repo := range config {
				logger.Infoln("  " + trigger)
				for repoName, hooks := range repo {
					logger.Infoln("  " + repoName)
					for _, hook := range hooks {
						logger.Infoln("    - " + hook)
					}
				}
			}
		}
	}
}

// Install git-hook into current git repo
func install(isInstall bool) {
	dirPath, err := getGitDirPath()
	if err != nil {
		logger.Errorln("Current directory is not a git repo")
	}

	if isInstall {
		isExist, _ := exists(filepath.Join(dirPath, "hooks.old"))
		if isExist {
			logger.Errorln("@rhooks.old already exists, perhaps you already installed?")
		}
		installInto(dirPath, tplPostInstall)
	} else {
		isExist, _ := exists(filepath.Join(dirPath, "hooks.old"))
		if !isExist {
			logger.Errorln("Error, hooks.old doesn't exists, aborting uninstall to not destroy something")
		}
		os.RemoveAll(filepath.Join(dirPath, "hooks"))
		os.Rename(filepath.Join(dirPath, "hooks.old"), filepath.Join(dirPath, "hooks"))
		logger.Infoln("Restore hooks.old")
	}
}

// Uninstall git-hooks from current git repo
func uninstall() {
	install(false)
}

// Install git-hooks global by setup init.tempdir in ~/.gitconfig
func installGlobal() {
	templatedir := ".git-template-with-git-hooks"
	home, err := homedir.Dir()
	if err == nil {
		templatedir = filepath.Join(home, templatedir)
	}
	isExist, _ := exists(templatedir)
	if !isExist {
		defaultdir := "/usr/share/git-core/templates"
		isExist, _ = exists(defaultdir)
		if isExist {
			os.Link(defaultdir, templatedir)
		} else {
			os.Mkdir(filepath.Join(templatedir, "hooks"), 0755)
		}
		installInto(templatedir, tplPreInstall)
	}
	gitExec("config --global init.templatedir " + templatedir)
	os.Rename(filepath.Join(templatedir, "hooks.old"), filepath.Join(templatedir, "hooks.original"))
	logger.Infoln("Git global config init.templatedir is now set to " + templatedir)
}

// Reset init.tempdir
func uninstallGlobal() {
	gitExec("config --global --unset init.templatedir")
}

// Check latest version of git-hooks by github release
// If there are new version of git-hooks, download and replace the current one
func update() {
	logger.Infoln("Current git-hooks version is " + VERSION)
	logger.Infoln("Check latest version...")

	client := github.NewClient(nil)
	releases, _, _ := client.Repositories.ListReleases(
		"git-hooks", "git-hooks", &github.ListOptions{})
	release := releases[0]
	version := *release.TagName
	logger.Infoln("Latest version is " + version)

	// compare version
	current, err := semver.New(VERSION[1:])
	if err != nil {
		logger.Errorln("Semver parse error " + err.Error())
	}
	latest, err := semver.New(version[1:])
	if err != nil {
		logger.Errorln("Semver parse error " + err.Error())
	}
	debug("Current version %s, latest version %s", current, latest)

	if latest.GT(current) {
		logger.Infoln("Download latest version...")
		target := fmt.Sprintf("git-hooks_%s_%s", runtime.GOOS, runtime.GOARCH)
		for _, asset := range release.Assets {
			if *asset.Name == target {
				file, err := downloadFromUrl(*asset.BrowserDownloadUrl)
				if err != nil {
					logger.Errorln("Download error", err.Error())
				}
				logger.Infoln("Download complete")

				// replace current version
				file.Chmod(0755)
				name, err := absExePath()
				if err != nil {
					logger.Errorln(err.Error())
				}

				debug("Replace %s with temp file %s", name, file.Name())
				out, err := os.Create(name)
				if err != nil {
					logger.Errorln("Create error " + err.Error())
				}
				defer out.Close()
				in, err := os.Open(file.Name())
				if err != nil {
					logger.Errorln("Open error " + err.Error())
				}
				defer in.Close()
				_, err = io.Copy(out, in)
				if err != nil {
					logger.Errorln("Copy error " + err.Error())
				}
				logger.Infoln(NAME + " update to " + version)
				break
			}
		}
	} else {
		logger.Infoln("Your " + NAME + " is update to date")
	}
}

func identity() {
	identity, err := gitExec("rev-list --max-parents=0 HEAD")
	if err != nil {
		logger.Errorln(err.Error())
	}

	logger.Infoln(identity)
}

// Execute project, semi, user and global scope hooks
func run(cmds ...string) {
	t := filepath.Base(cmds[0])
	args := cmds[1:]
	for scope, dir := range hookDirs() {
		config, err := listHooksInDir(scope, dir)
		if err == nil {
			for trigger, hooks := range config {
				// semi scope
				if trigger == t || trigger == ("_"+t) {
					for _, hook := range hooks {
						out, err := runHook(filepath.Join(dir, trigger, hook), args...)
						if err != nil {
							logger.Error(out)
						} else {
							if out != "" {
								logger.Info(out)
							}
						}
					}
				}
			}
		}
	}

	// find contrib directory
	home, err := homedir.Dir()
	contrib := CONTRIB_PATH
	if err == nil {
		contrib = filepath.Join(home, CONTRIB_PATH)
	}
	for _, configPath := range hookConfigs() {
		config, err := listHooksInConfig(configPath)
		if err == nil {
			for trigger, repo := range config {
				if trigger == t {
					for repoName, hooks := range repo {
						// check if repo exist in local file system
						isExist, _ := exists(filepath.Join(contrib, repoName))
						if !isExist {
							logger.Infoln("Cloning repo " + repoName)
							_, err := gitExec(fmt.Sprintf("clone https://%s %s", repoName, filepath.Join(contrib, repoName)))
							if err != nil {
								continue
							}
						}
						// execute hook
						for _, hook := range hooks {
							out, err := runHook(filepath.Join(contrib, repoName, hook, "hook"), args...)
							if err != nil {
								logger.Error(out)
							} else {
								if out != "" {
									logger.Info(out)
								}
							}
						}
					}
				}
			}
		}
	}
}

// Execute specific hook with arguments
// Return error message as out if error occured
func runHook(hook string, args ...string) (out string, err error) {
	debug("Execute contrib hook %s %s", hook, args)

	wd, err := os.Getwd()
	if err != nil {
		return err.Error(), err
	}

	cmd := exec.Command(hook, args...)
	cmd.Dir = wd
	result, err := cmd.Output()
	if err != nil {
		return err.Error(), err
	} else {
		return string(result), nil
	}
}

// List available hooks inside directory
// Under trigger directory,
// Treate file as a hook if it's executable,
// Treate directory as a hook if it contain an executable file with the name of `trigger`
// Example:
// githooks
//     ├── _pre-commit
//     │   ├── test
//     │   └── whitespace
//     └── pre-commit
//         ├── dir
//         │   └── pre-commit
//         └── whitespace
func listHooksInDir(scope, dirname string) (hooks map[string][]string, err error) {
	hooks = make(map[string][]string)

	dirs, err := ioutil.ReadDir(dirname)
	if err != nil {
		return
	}

	for _, dir := range dirs {
		files, err := ioutil.ReadDir(filepath.Join(dirname, dir.Name()))
		if err == nil {
			hooks[dir.Name()] = make([]string, 0)
			for _, file := range files {
				// filter files or directories
				file, err := os.Stat(filepath.Join(dirname, dir.Name(), file.Name()))
				if err == nil {
					if file.IsDir() {
						libs, err := ioutil.ReadDir(filepath.Join(dirname, dir.Name(), file.Name()))
						if err == nil {
							for _, lib := range libs {
								libname := lib.Name()
								extension := filepath.Ext(libname)
								if isExecutable(lib) && libname[0:len(libname)-len(extension)] == dir.Name() {
									hooks[dir.Name()] = append(hooks[dir.Name()], filepath.Join(file.Name(), libname))
								}
							}
						}
					} else {
						if isExecutable(file) {
							hooks[dir.Name()] = append(hooks[dir.Name()], file.Name())
						}
					}
				}
			}
		}
	}
	debug("%s scope hooks %s", scope, hooks)

	//
	// exclude
	//
	// exclude only works for user and global scope
	if scope == "user" || scope == "global" {
		file, err := ioutil.ReadFile(filepath.Join(dirname, "excludes.json"))
		if err == nil {
			var excludes interface{}
			json.Unmarshal(file, &excludes)
			debug("excludes %s", excludes)

			wrapper := make(map[string]interface{})
			// repoid will be empty string if not in a git repo or don't have any commit yet
			repoid, _ := gitExec("rev-list --max-parents=0 HEAD")

			if scope == "user" {
				wrapper[repoid] = hooks
				exclude.Exclude(wrapper, excludes)
				hooks = wrapper[repoid].(map[string][]string)
			} else {
				// global scope exclude
				user, err := user.Current()
				username := ""
				if err == nil {
					username = user.Username
				}
				wrapper[username] = make(map[string]interface{})
				wrapper[username].(map[string]interface{})[repoid] = hooks
				exclude.Exclude(wrapper, excludes)
				hooks = wrapper[username].(map[string]interface{})[repoid].(map[string][]string)
			}
		}
	}
	debug("%s scope hooks %s after exclusion", scope, hooks)

	return hooks, nil
}

func listHooksInConfig(config string) (hooks map[string]map[string][]string, err error) {
	hooks = make(map[string]map[string][]string)

	file, err := ioutil.ReadFile(config)
	if err != nil {
		return
	}

	json.Unmarshal(file, &hooks)
	return
}

func installInto(dir string, template string) {
	// backup
	os.Rename(filepath.Join(dir, "hooks"), filepath.Join(dir, "hooks.old"))
	os.Mkdir(filepath.Join(dir, "hooks"), 0755)
	for _, hook := range TRIGGERS {
		logger.Infoln("Install ", hook)
		f, _ := os.Create(filepath.Join(dir, "hooks", hook))
		f.WriteString(template)
		f.Sync()
		f.Chmod(0755)
	}
}

func hookDirs() map[string]string {
	dirs := make(map[string]string)

	// project scope
	root, err := getGitRepoRoot()
	if err == nil {
		path := filepath.Join(root, "githooks")
		isExist, _ := exists(path)
		if isExist {
			dirs["project"] = path
		}
	}

	// user scope
	home, err := homedir.Dir()
	if err == nil {
		path := filepath.Join(home, ".githooks")
		isExist, _ := exists(path)
		if isExist {
			dirs["user"] = path
		}
	}

	// global scope
	// NOTE: git-hooks global hook actually configured via git --system
	// configuration file
	global, err := gitExec("config --get --system hooks.global")
	if err == nil {
		path := global
		isExist, _ := exists(path)
		if isExist {
			dirs["global"] = path
		}
	}

	return dirs
}

func hookConfigs() map[string]string {
	configs := make(map[string]string)

	root, err := getGitRepoRoot()
	if err == nil {
		path := filepath.Join(root, "githooks.json")
		isExist, _ := exists(path)
		if isExist {
			configs["project"] = path
		}
	}

	home, err := homedir.Dir()
	if err == nil {
		path := filepath.Join(home, ".githooks.json")
		isExist, _ := exists(path)
		if isExist {
			configs["user"] = path
		}
	}

	global, err := gitExec("config --get --system hooks.globalconfig")
	if err == nil {
		path := global
		isExist, _ := exists(path)
		if isExist {
			configs["global"] = path
		}
	}

	return configs
}

func getGitRepoRoot() (string, error) {
	return gitExec("rev-parse --show-toplevel")
}

func getGitDirPath() (string, error) {
	return gitExec("rev-parse --git-dir")
}

func gitExec(args ...string) (string, error) {
	args = strings.Split(strings.Join(args, " "), " ")
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}

	cmd := exec.Command("git", args...)
	cmd.Dir = wd

	if out, err := cmd.Output(); err == nil {
		return string(bytes.Trim(out, "\n")), nil
	} else {
		return "", err
	}
}

func bind(f interface{}, args ...interface{}) func(c *cli.Context) {
	callable := reflect.ValueOf(f)
	arguments := make([]reflect.Value, len(args))
	for i, arg := range args {
		arguments[i] = reflect.ValueOf(arg)
	}
	return func(c *cli.Context) {
		callable.Call(arguments)
	}
}

func exists(path string) (bool, error) {
	_, err := os.Stat(path)
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}

// download to temp file by url
// return the temp file
func downloadFromUrl(url string) (file *os.File, err error) {
	debug("Downloading %s", url)

	file, err = ioutil.TempFile(os.TempDir(), NAME)
	fileName := file.Name()
	output, err := os.Create(fileName)
	if err != nil {
		return
	}
	defer output.Close()

	response, err := http.Get(url)
	if err != nil {
		return
	}
	defer response.Body.Close()

	n, err := io.Copy(output, response.Body)
	if err != nil {
		return
	}

	debug("Download success")
	debug("%n bytes downloaded.", n)
	return
}

// return fullpath to executable file.
func absExePath() (name string, err error) {
	name = os.Args[0]

	if name[0] == '.' {
		name, err = filepath.Abs(name)
		if err == nil {
			name = filepath.Clean(name)
		}
	} else {
		name, err = exec.LookPath(filepath.Clean(name))
	}
	return
}

func isExecutable(info os.FileInfo) bool {
	return info.Mode()&0111 != 0
}
