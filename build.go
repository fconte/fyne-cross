package main

import (
	"flag"
	"fmt"
	"go/build"
	"log"
	"os"
	"os/exec"
	"os/user"
	"strings"
)

const dockerImage = "lucor/fyne-cross"

// targetWithBuildOpts represents the list of supported GOOS/GOARCH with the relative
// options to build
var targetWithBuildOpts = map[string][]string{
	"darwin/amd64":  []string{"GOOS=darwin", "GOARCH=amd64", "CC=o32-clang"},
	"darwin/386":    []string{"GOOS=darwin", "GOARCH=386", "CC=o32-clang"},
	"linux/amd64":   []string{"GOOS=linux", "GOARCH=amd64", "CC=gcc"},
	"linux/386":     []string{"GOOS=linux", "GOARCH=386   CC=gcc"},
	"windows/amd64": []string{"GOOS=windows", "GOARCH=amd64", "CC=x86_64-w64-mingw32-gcc"},
	"windows/386":   []string{"GOOS=windows", "GOARCH=386", "CC=x86_64-w64-mingw32-gcc"},
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
	// verbosity represents the verbosity setting
	verbose bool
)

// builder is the command implementing the fyne app command interface
type builder struct{}

func (b *builder) addFlags() {
	defautlTarget := strings.Join([]string{build.Default.GOOS, build.Default.GOARCH}, "/")
	flag.StringVar(&targetList, "targets", defautlTarget, fmt.Sprintf("The list of targets to build separated by comma. Default to current GOOS/GOARCH %s", defautlTarget))
	flag.StringVar(&output, "output", "", "The named output file. Default to package name")
	flag.StringVar(&pkgRootDir, "dir", "", "The package root directory. Default current dir")
	flag.BoolVar(&verbose, "v", false, "Enable verbosity flag for go commands. Default to false")
}

func (b *builder) printHelp(indent string) {
	fmt.Println("Usage: fyne-cross [parameters] package")
	fmt.Println()
	fmt.Println("Cross compile a Fyne application")

	flag.PrintDefaults()
	fmt.Println()

	fmt.Println("Supported targets:")
	for target := range targetWithBuildOpts {
		fmt.Println(indent, "- ", target)
	}
	fmt.Println()

	fmt.Println("Example: fyne-cross --targets=linux/amd64,windows/amd64 --output=test cmd/hello")
}

func (b *builder) run(args []string) {
	var err error

	if len(args) != 1 {
		fmt.Println("Missing required package argument after flags")
		os.Exit(1)
	}

	flag.Parse()

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

	db := dockerBuilder{
		pkg:     args[0],
		workDir: pkgRootDir,
		targets: targets,
		output:  output,
		verbose: verbose,
	}

	fmt.Println("Downloading dependencies")
	err = db.goGet()
	if err != nil {
		log.Fatal(err)
	}

	for _, target := range targets {
		fmt.Printf("Building for %s\n", target)
		err = db.goBuild(target)
		if err != nil {
			log.Fatal(err)
		}
		fmt.Printf("Built as %s\n", db.targetOutput(target))
	}
}

// dockerBuilder represents the docker builder
type dockerBuilder struct {
	targets []string
	output  string
	pkg     string
	workDir string
	verbose bool
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
	args := append(d.defaultArgs(), d.goBuildArgs(target)...)
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
func (d *dockerBuilder) targetOutput(target string) string {
	output := d.output
	if output == "" {
		parts := strings.Split(d.pkg, "/")
		output = parts[len(parts)-1]
	}

	normalizedTarget := strings.ReplaceAll(target, "/", "-")

	ext := ""
	if strings.HasPrefix(target, "windows") {
		ext = ".exe"
	}
	return fmt.Sprintf("%s-%s%s", output, normalizedTarget, ext)
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
	args = append(args, "-w", fmt.Sprintf("/app/%s", d.pkg))

	// mount root dir package under image GOPATH/src
	args = append(args, "-v", fmt.Sprintf("%s:/app/%s", d.workDir, d.pkg))

	// mount a temporary cache dir for dependencies (GOROOT/pkg and GOROOT/src)
	args = append(args, "-v", fmt.Sprintf("%s/fyne-cross-cache:/go", os.TempDir()))

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
func (d *dockerBuilder) goBuildArgs(target string) []string {

	// enable CGO
	args := []string{"-e", "CGO_ENABLED=1"}

	// add compile target options
	if buildOpts, ok := targetWithBuildOpts[target]; ok {
		for _, o := range buildOpts {
			args = append(args, "-e", o)
		}
	}

	output := d.targetOutput(target)

	buildCmd := fmt.Sprintf("go build -o build/%s -a %s %s", output, d.verbosityFlag(), d.pkg)

	return append(args, dockerImage, buildCmd)
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
