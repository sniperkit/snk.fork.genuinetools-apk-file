/*
Sniperkit-Bot
- Status: analyzed
*/

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"

	"github.com/PuerkitoBio/goquery"
	"github.com/agrison/go-tablib"
	"github.com/genuinetools/pkg/cli"
	"github.com/sirupsen/logrus"

	"github.com/sniperkit/snk.fork.genuinetools-apk-file/version"
)

const (
	alpineContentsSearchURI = "https://pkgs.alpinelinux.org/contents"
)

type fileInfo struct {
	path, pkg, branch, repo, arch string
}

var (
	errEmpty = errors.New("stdin is empty")
)

var (
	arch   string
	branch string
	repo   string

	wildcard   string
	query      string
	filterType string

	outputPrefix   string
	outputBasename string
	outputFormat   string
	outputType     string

	tree  bool
	debug bool
	stdin bool

	validWildcards   = []string{"*", "?"}
	validFilterTypes = []string{"file", "path", "package"}

	validOutputTypes   = []string{"stdout", "file"}
	validOutputFormats = []string{"markdown", "csv", "yaml", "json", "xlsx", "xml", "tsv", "mysql", "postgres", "html", "ascii"}

	validArches   = []string{"x86", "x86_64", "armhf", "aarch64", "ppc64le", "s390x"}
	validRepos    = []string{"main", "community", "testing"}
	validBranches = []string{"edge", "v3.8", "v3.7", "v3.6", "v3.5", "v3.4", "v3.3"}
)

func main() {
	// Create a new cli program.
	p := cli.NewProgram()
	p.Name = "apk-file"
	p.Description = "Search apk package contents via the command line"

	// Set the GitCommit and Version.
	p.GitCommit = version.GITCOMMIT
	p.Version = version.VERSION

	// Setup the global flags.
	p.FlagSet = flag.NewFlagSet("global", flag.ExitOnError)

	p.FlagSet.StringVar(&query, "query", "", "query to lookup")
	p.FlagSet.StringVar(&wildcard, "wildcard", "", "query wildcard ("+strings.Join(validWildcards, ", ")+")")

	// to extract from env
	// use cases:
	// - inside docker container
	//   - for converting a list of packages from apt, yum, pacman or many others ... (nb. with the mapping provided by repology api v1)
	//   - for build or lddtree linked shared libraries
	p.FlagSet.StringVar(&branch, "branch", "v3.8", "alpine branch ("+strings.Join(validBranches, ", ")+")")
	p.FlagSet.StringVar(&repo, "repo", "main", "repository to search in ("+strings.Join(validRepos, ", ")+")")
	p.FlagSet.StringVar(&arch, "arch", "x86_64", "arch to search for ("+strings.Join(validArches, ", ")+")")

	p.FlagSet.StringVar(&filterType, "filter", "", "pattern filter ("+strings.Join(validFilterTypes, ", ")+")")

	p.FlagSet.StringVar(&outputType, "output-type", "stdin", "output results to "+strings.Join(validOutputTypes, "or "))
	p.FlagSet.StringVar(&outputPrefix, "output-prefix", "output", "output results to prefix_path (default: ./output).")
	p.FlagSet.StringVar(&outputBasename, "output-basename", "results", "output results to prefix_path (default: [OUTPUT_PREFIX/results.[FORMAT]).")
	p.FlagSet.StringVar(&outputFormat, "output", "yaml", "output results with  ("+strings.Join(validOutputFormats, ", ")+") format.")

	p.FlagSet.BoolVar(&stdin, "stdin", false, "enable stdin mode")
	p.FlagSet.BoolVar(&tree, "tree", false, "enable tree mode")
	p.FlagSet.BoolVar(&debug, "debug", false, "enable debug logging")

	// Set the before function.
	p.Before = func(ctx context.Context) error {
		// Set the log level.
		if debug {
			logrus.SetLevel(logrus.DebugLevel)
		}

		if wildcard != "" && !stringInSlice(wildcard, validWildcards) {
			return fmt.Errorf("%s is not a valid pattern type allowed: "+strings.Join(validWildcards, ", "), wildcard)
		}

		if filterType != "" && !stringInSlice(filterType, validFilterTypes) {
			return fmt.Errorf("%s is not a valid pattern type allowed: "+strings.Join(validFilterTypes, ", "), filterType)
		}

		if branch != "" && !stringInSlice(branch, validBranches) {
			return fmt.Errorf("%s is not a valid version, allowed: "+strings.Join(validBranches, ", "), branch)
		}

		if arch != "" && !stringInSlice(arch, validArches) {
			return fmt.Errorf("%s is not a valid arch, allowed: "+strings.Join(validArches, ", "), arch)
		}

		if repo != "" && !stringInSlice(repo, validRepos) {
			return fmt.Errorf("%s is not a valid repo, allowed: "+strings.Join(validRepos, ", "), repo)
		}

		return nil
	}

	// Set the main program action.
	//p.Action = func(ctx context.Context) error {
	p.Action = func(ctx context.Context, args []string) error {

		// pp.Println("args: ", args)

		// nb. check if input is a string or a stdin

		var input string
		if stdin {
			if ok, err := checkStdin(); ok {
				input = readStdin()
				input = strings.TrimSuffix(input, "\n")
			} else {
				return fmt.Errorf("stdin is invalid, msg: %s", err)
			}
		} else {
			if p.FlagSet.NArg() < 1 {
				return errors.New("must pass a file to search for")
			}
			input = p.FlagSet.Arg(0)
		}

		input = fmt.Sprintf("%s%s", input, wildcard)

		logrus.Infoln("args: ", args)
		logrus.Infoln("input: ", input)
		logrus.Infoln("wildcard: ", wildcard)
		logrus.Infoln("branch: ", branch)

		// todo: a foreach for multiple patterns matching
		f, p := getFileAndPath(input)

		query := url.Values{
			"file":   {f},
			"path":   {p},
			"branch": {branch},
			"repo":   {repo},
			"arch":   {arch},
		}

		uri := fmt.Sprintf("%s?%s", alpineContentsSearchURI, query.Encode())
		logrus.Debugf("requesting from %s", uri)
		resp, err := http.Get(uri)
		if err != nil {
			logrus.Fatalf("requesting %s failed: %v", uri, err)
			// return err
		}
		defer resp.Body.Close()
		doc, err := goquery.NewDocumentFromReader(resp.Body)
		if err != nil {
			logrus.Fatalf("creating document failed: %v", err)
			// return err
		}

		files := getFilesInfo(doc)
		contentDataset := tablib.NewDataset([]string{"file", "package", "branch", "repository", "architecture"})

		for _, f := range files {
			contentDataset.AppendValues(f.path, f.pkg, f.branch, f.repo, f.arch)
		}

		if _, err := tabularResults(contentDataset); err != nil {
			return err
		}

		return nil
	}

	// Run our program.
	p.Run()
}

func tabularResults(ds *tablib.Dataset) (result *tablib.Exportable, err error) { // (result *tablib.Dataset, err error) {

	// ds = ds.Sort("package")
	// ds = ds.Filter("package")
	// fmt.Println(ds)

	switch outputFormat {
	case "csv":
		result, err = ds.CSV()
	case "tsv":
		result, err = ds.TSV()
	case "yaml":
		result, err = ds.YAML()
	case "json":
		result, err = ds.JSON()
	case "xlsx":
		result, err = ds.XLSX()
	case "xml":
		result, err = ds.XML()
	case "mysql":
		result = ds.MySQL(outputBasename)
	case "postgres":
		result = ds.Postgres(outputBasename)
	case "html":
		result, err = ds.XLSX()
	case "ascii":
	default:
		result = ds.Tabular("grid" /* tablib.TabularGrid */)
	}

	/*
		ds.ConstrainColumn("Year", func(val interface{}) bool { return val.(int) > 2008 })
		ds.ValidFailFast() // false
		if !ds.Valid() {   // validate the whole dataset, errors are retrieved in Dataset.ValidationErrors
			ds.ValidationErrors[0] // Row: 4, Column: 2
		}
	*/

	/*
		if save == true {
			if result.WriteFile(prefixPath+"/"+filename+"."+output, 0644) != nil {
				fmt.Println(err)
			}
		}
	*/
	fmt.Println("results")
	fmt.Println(result)

	packages, err := ds.Filter("luxury").CSV()
	if err != nil {
		return nil, err
	}
	fmt.Println("packages")
	fmt.Println(packages)

	// return nil
	return
}

func getFilesInfo(d *goquery.Document) []fileInfo {
	files := []fileInfo{}
	d.Find(".pure-table tr:not(:first-child)").Each(func(j int, l *goquery.Selection) {
		f := fileInfo{}
		rows := l.Find("td")
		rows.Each(func(i int, s *goquery.Selection) {
			switch i {
			case 0:
				f.path = s.Text()
			case 1:
				f.pkg = s.Text()
			case 2:
				f.branch = s.Text()
			case 3:
				f.repo = s.Text()
			case 4:
				f.arch = s.Text()
			default:
				logrus.Warn("Unmapped value for column %d with value %s", i, s.Text())
			}
		})
		files = append(files, f)
	})
	return files
}

func readStdin() (s string) {
	if input, err := ioutil.ReadAll(os.Stdin); err != nil {
		s = err.Error()
	} else {
		s = string(input)
	}
	return
}

// CheckStdin return true when Stdin is connected to a pipe,
// false otherwise
func checkStdin() (bool, error) {
	fi, err := os.Stdin.Stat()
	if err != nil {
		return false, err
	}
	if fi.Mode()&os.ModeNamedPipe == 0 {
		return false, nil
	}
	return true, nil
}

func getFileAndPath(arg string) (file string, dir string) {
	file = "*" + path.Base(arg) + "*"
	dir = path.Dir(arg)
	if dir != "" && dir != "." {
		dir = "*" + dir
		file = strings.TrimPrefix(file, "*")
	} else {
		dir = ""
	}
	return file, dir
}

func stringInSlice(a string, list []string) bool {
	for _, b := range list {
		if b == a {
			return true
		}
	}
	return false
}
