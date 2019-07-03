package main

import (
	"flag"
	"fmt"
	"go/build"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
)

const dockerImage = "lucor/fyne-cross"

// targetWithBuildOpts represents the list of supported GOOS/GOARCH with the relative
// options to build
var targetWithBuildOpts = map[string][]string{
	"darwin/amd64":  []string{"GOOS=darwin", "GOARCH=amd64", "CC=o32-clang"},
	"darwin/386":    []string{"GOOS=darwin", "GOARCH=386", "CC=o32-clang"},
	"linux/amd64":   []string{"GOOS=linux", "GOARCH=amd64", "CC=gcc"},
	"linux/386":     []string{"GOOS=linux", "GOARCH=386", "CC=gcc"},
	"windows/amd64": []string{"GOOS=windows", "GOARCH=amd64", "CC=x86_64-w64-mingw32-gcc"},
	"windows/386":   []string{"GOOS=windows", "GOARCH=386", "CC=x86_64-w64-mingw32-gcc"},
}

// targetLdflags represents the list of default ldflags to pass on build
// for a specified GOOS/GOARCH
var targetLdflags = map[string]string{
	"windows/amd64": "-H windowsgui",
	"windows/386":   "-H windowsgui",
}

var (
	// targetList represents a list of target to build on separated by comma
	targetList string
	// output represents the named output file
	output string
	// pkg represents the package to build
	pkg string
	// pkgRootDir represents the package root directory
	pkgRootDir string
	// cacheDir represents the cache directory
	cacheDir string
	// verbosity represents the verbosity setting
	verbose bool
	// ldflags represents the flags to pass to the external linker
	ldflags string
)

// builder is the command implementing the fyne app command interface
type builder struct{}

func (b *builder) addFlags() {
	defaultTarget := strings.Join([]string{build.Default.GOOS, build.Default.GOARCH}, "/")
	flag.StringVar(&targetList, "targets", defaultTarget, fmt.Sprintf("The list of targets to build separated by comma. Default to current GOOS/GOARCH %s", defaultTarget))
	flag.StringVar(&output, "output", "", "The named output file. Default to package name")
	flag.StringVar(&pkgRootDir, "dir", "", "The package root directory. Default current dir")
	flag.StringVar(&cacheDir, "cache-dir", "", "The directory used to cache package dependencies. Default to system cache root directory (i.e. $HOME/.cache)")
	flag.BoolVar(&verbose, "v", false, "Enable verbosity flag for go commands. Default to false")
	flag.StringVar(&ldflags, "ldflags", "", "flags to pass to the external linker")
}

func (b *builder) printHelp(indent string) {
	fmt.Println("Usage: fyne-cross [parameters] package")
	fmt.Println()
	fmt.Println("Cross compile a Fyne application")
	fmt.Println()

	fmt.Println("Package is the relative path to main.go file or main package. Default to '.'")
	fmt.Println()

	fmt.Println("Optional parameters:")
	flag.PrintDefaults()
	fmt.Println()

	fmt.Println("Supported targets:")
	for target := range targetWithBuildOpts {
		fmt.Println(indent, "- ", target)
	}
	fmt.Println()

	fmt.Println("Default ldflags per target:")
	for target, ldflags := range targetLdflags {
		fmt.Println(indent, "- ", target, ldflags)
	}
	fmt.Println()

	fmt.Println("Example: fyne-cross --targets=linux/amd64,windows/amd64 --output=test ./cmd/test")
}

func (b *builder) run(args []string) {
	var err error

	targets, err := parseTargets(targetList)
	if err != nil {
		fmt.Printf("Unable to parse targets option %s", err)
		os.Exit(1)
	}

	if pkgRootDir == "" {
		pkgRootDir, err = os.Getwd()
		if err != nil {
			fmt.Printf("Cannot get the path for current directory %s", err)
			os.Exit(1)
		}
	}

	if cacheDir == "" {
		cacheDir, err = os.UserCacheDir()
		if err != nil {
			fmt.Printf("Cannot get the path for cache directory %s", err)
			os.Exit(1)
		}
	}

	pkg := args[0]
	if pkg == "" {
		pkg, err = os.Getwd()
		if err != nil {
			fmt.Printf("Cannot get the path for current directory %s", err)
			os.Exit(1)
		}
	}

	db := dockerBuilder{
		pkg:      pkg,
		workDir:  pkgRootDir,
		cacheDir: cacheDir,
		targets:  targets,
		output:   output,
		verbose:  verbose,
		ldflags:  ldflags,
	}

	err = db.checkRequirements()
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}

	fmt.Println("Downloading dependencies")
	err = db.goGet()
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}

	fmt.Printf("Build output folder: %s/build\n", db.workDir)
	for _, target := range targets {
		fmt.Printf("Building for %s\n", target)
		err = db.goBuild(target)
		if err != nil {
			fmt.Println(err)
			os.Exit(1)
		}
		t, _ := db.targetOutput(target)
		fmt.Printf("Built as %s\n", t)
	}
}

// dockerBuilder represents the docker builder
type dockerBuilder struct {
	targets  []string
	output   string
	pkg      string
	workDir  string
	cacheDir string
	verbose  bool
	ldflags  string
}

// checkRequirements checks if all the build requirements are satisfied
func (d *dockerBuilder) checkRequirements() error {
	err := exec.Command("docker", "version").Run()
	if err != nil {
		return fmt.Errorf("Missed requirement: docker binary not found in PATH")
	}
	return nil
}

// goGet downloads the application dependencies via go get
func (d *dockerBuilder) goGet() error {
	args := append(d.defaultArgs(), d.goGetArgs()...)
	if d.verbose {
		fmt.Printf("docker %s\n", strings.Join(args, " "))
	}
	cmd := exec.Command("docker", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// goBuild runs the go build for target
func (d *dockerBuilder) goBuild(target string) error {
	buildArgs, err := d.goBuildArgs(target)
	if err != nil {
		return err
	}

	args := append(d.defaultArgs(), buildArgs...)
	if d.verbose {
		fmt.Printf("docker %s\n", strings.Join(args, " "))
	}
	cmd := exec.Command("docker", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// targetOutput returns the output file for the specified target.
// Default prefix is the package name. To override use the output option.
// Example: fyne-linux-amd64
func (d *dockerBuilder) targetOutput(target string) (string, error) {
	output := d.output
	if output == "" {
		if d.pkg == "." {
			files, err := filepath.Glob("./*.go")
			if err != nil {
				return "", err
			}
			if len(files) == 0 {
				return "", fmt.Errorf("Cannot found go files in current dir")
			}

			output = strings.TrimSuffix(files[0], ".go")
		} else {
			parts := strings.Split(d.pkg, "/")
			output = parts[len(parts)-1]
		}
	}

	normalizedTarget := strings.Replace(target, "/", "-", -1)

	ext := ""
	if strings.HasPrefix(target, "windows") {
		ext = ".exe"
	}
	return fmt.Sprintf("%s-%s%s", output, normalizedTarget, ext), nil
}

// verbosityFlag returns the string used to set verbosity with go commands
// according to current setting
func (d *dockerBuilder) verbosityFlag() string {
	v := ""
	if d.verbose {
		v = "-v"
	}
	return v
}

// defaultArgs returns the default arguments used to run a go command into the
// docker container
func (d *dockerBuilder) defaultArgs() []string {
	args := []string{
		"run",
		"--rm",
		"-t",
	}

	// set workdir
	args = append(args, "-w", fmt.Sprintf("/app"))

	// mount root dir package under image GOPATH/src
	args = append(args, "-v", fmt.Sprintf("%s:/app", d.workDir))

	// mount the cache user dir. Used to cache package dependencies (GOROOT/pkg and GOROOT/src)
	args = append(args, "-v", fmt.Sprintf("%s/fyne-cross:/go", d.cacheDir))

	// attempt to set fyne user id as current user id to handle mount permissions
	u, err := user.Current()
	if err == nil {
		args = append(args, "-e", fmt.Sprintf("fyne_uid=%s", u.Uid))
	}

	return args
}

// goGetArgs returns the arguments for the "go get" command
func (d *dockerBuilder) goGetArgs() []string {
	buildCmd := fmt.Sprintf("go get %s -d ./...", d.verbosityFlag())
	return []string{dockerImage, buildCmd}
}

// goGetArgs returns the arguments for the "go build" command for target
func (d *dockerBuilder) goBuildArgs(target string) ([]string, error) {
	// Start adding env variables
	args := []string{
		// enable CGO
		"-e", "CGO_ENABLED=1",
	}

	// add default compile target options env variables
	if buildOpts, ok := targetWithBuildOpts[target]; ok {
		for _, o := range buildOpts {
			args = append(args, "-e", o)
		}
	}

	// add docker image
	args = append(args, dockerImage)

	// add go build command
	args = append(args, "go", "build")

	// Start adding ldflags
	ldflags := []string{}
	// add defaults
	if ldflagsDefault, ok := targetLdflags[target]; ok {
		ldflags = append(ldflags, ldflagsDefault)
	}
	// add custom ldflags
	if d.ldflags != "" {
		ldflags = append(ldflags, d.ldflags)
	}

	// add ldflags to command, if any
	if len(ldflags) > 0 {
		args = append(args, "-ldflags", fmt.Sprintf("'%s'", strings.Join(ldflags, " ")))
	}

	// add target output
	targetOutput, err := d.targetOutput(target)
	if err != nil {
		return []string{}, err
	}
	args = append(args, "-o", fmt.Sprintf("build/%s", targetOutput))

	// add force compile option
	args = append(args, "-a")

	// add force compile option
	if d.verbose {
		args = append(args, "-v")
	}

	// add package
	args = append(args, d.pkg)
	return args, nil
}

// parseTargets parse comma separated target list and validate against the supported targets
func parseTargets(targetList string) ([]string, error) {
	targets := []string{}

	for _, target := range strings.Split(targetList, ",") {
		target = strings.TrimSpace(target)

		var isValid bool
		for oktarget := range targetWithBuildOpts {
			if target == oktarget {
				isValid = true
				targets = append(targets, target)
				break
			}
		}

		if isValid == false {
			return targets, fmt.Errorf("Unsupported target %q", target)
		}
	}

	return targets, nil
}
