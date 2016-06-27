// Copyright (c) 2012-present José Carlos Nieto, https://menteslibres.net/xiam
//
// Permission is hereby granted, free of charge, to any person obtaining
// a copy of this software and associated documentation files (the
// "Software"), to deal in the Software without restriction, including
// without limitation the rights to use, copy, modify, merge, publish,
// distribute, sublicense, and/or sell copies of the Software, and to
// permit persons to whom the Software is furnished to do so, subject to
// the following conditions:
//
// The above copyright notice and this permission notice shall be
// included in all copies or substantial portions of the Software.
//
// THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND,
// EXPRESS OR IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF
// MERCHANTABILITY, FITNESS FOR A PARTICULAR PURPOSE AND
// NONINFRINGEMENT. IN NO EVENT SHALL THE AUTHORS OR COPYRIGHT HOLDERS BE
// LIABLE FOR ANY CLAIM, DAMAGES OR OTHER LIABILITY, WHETHER IN AN ACTION
// OF CONTRACT, TORT OR OTHERWISE, ARISING FROM, OUT OF OR IN CONNECTION
// WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE SOFTWARE.

package main

import (
	"bytes"
	"crypto/sha1"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
	"path"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rwcarlsen/goexif/exif"
	"github.com/rwcarlsen/goexif/tiff"
)

var (
	errlog   = log.New(os.Stderr, "ERROR ", log.LstdFlags)
	debuglog = log.New(os.Stdout, "DEBUG ", log.LstdFlags)
)

type fileLocker struct {
	table    map[string]bool
	watchers map[string][]chan bool
	mu       sync.Mutex
}

func (f *fileLocker) AcquireLock(s string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.table[s]; ok {
		return false
	}
	f.table[s] = true
	return true
}

func (f *fileLocker) WatchUnlock(s string) chan bool {
	f.mu.Lock()
	defer f.mu.Unlock()

	c := make(chan bool)
	f.watchers[s] = append(f.watchers[s], c)
	return c
}

func (f *fileLocker) Unlock(s string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.watchers[s] {
		c <- true
	}
	delete(f.table, s)
	delete(f.watchers, s)
}

var fileLocks = fileLocker{
	table:    make(map[string]bool),
	watchers: make(map[string][]chan bool),
}

const (
	pathSeparator = string(os.PathSeparator)
	fileSeparator = "-"
)

const (
	numberedMonth = `%02d_%s`       // 01_January
	numberedDay   = `%02d_%s`       // 02_Wednesday
	datedFile     = `%02dH%02dM.%s` // 16h05m_AABBCCDD.jpg
	numberedFile  = `%02d_%s.%s`    // 01_Unknown_AABBCCDD.mp3
	namedFile     = `%s.%s`         // Unknown_AABBCCDD.pdf
)

var wg sync.WaitGroup

type task struct {
	srcFile string
	dstDir  string
	wg      *sync.WaitGroup
}

type tagsWalker struct {
	v map[string]string
}

func (tw *tagsWalker) Walk(name exif.FieldName, tag *tiff.Tag) error {
	tw.v[string(name)] = tag.String()
	return nil
}

type token struct{}

var tasks chan token

var (
	errUnknownFile       = errors.New(`Could not identify file using EXIF reader`)
	errNotADirectory     = errors.New(`%s: is not a directory`)
	errMissingCreateTime = errors.New(`Missing create time`)
)

var (
	reDateTime = regexp.MustCompile(`(\d{4}):(\d{2}):(\d{2}) (\d{2}):(\d{2}):(\d{2})`)
)

var stats Stats

const (
	statUnknownFiles int = iota
	statDeletedFiles
	statDuplicatedFiles
	statSkippedFiles
	statCopiedFiles
	statMovedFiles
	statErroredTasks
)

var restrictToExtensions map[string]bool

var (
	// This map is used to define what extensions to look for if the users wants
	// "video" or "document".
	knownTypes = map[string][]string{
		"video":    []string{"mp4", "avi", "m4v", "mov", "lrv", "mts"},
		"photo":    []string{"jpeg", "jpg", "raw", "arw"},
		"audio":    []string{"m4a", "waveform"},
		"music":    []string{"mp3", "wav"},
		"document": []string{"pdf", "doc", "xls"},
	}
)

var (
	flagRestrict       = flag.String("restrict", "", "Restrict files to certain extension (-restrict jpg,mp4,raw) or types (-restrict video,photo,music).")
	flagFrom           = flag.String("source", "", "Scan for files on this directory (recursive).")
	flagDest           = flag.String("destination", "", "Move files into this directory.")
	flagDeleteOriginal = flag.Bool("delete-original", false, "Delete original file after copying it.")
	flagDryRun         = flag.Bool("dry-run", false, "Prints what would be done without actually doing it.")
	flagMaxProcs       = flag.Int("max-procs", runtime.NumCPU()*2, "The maximum number of tasks running at the same time.")
	flagExifTool       = flag.Bool("exiftool", false, "Use exiftool instead of libexif (slower. requires exiftool http://owl.phy.queensu.ca/~phil/exiftool/).")
	flagTryExifTool    = flag.Bool("try-exiftool", false, "Fallback to exiftool if libexif fails (requires exiftool http://owl.phy.queensu.ca/~phil/exiftool/).")
	flagAcceptAll      = flag.Bool("all", false, "Accept all kinds of files, including those that do not have EXIF data.")
	flagAllowHidden    = flag.Bool("allow-hidden", false, "Accept hidden files.")
)

func restrictExtensions() {
	restrictToExtensions = make(map[string]bool)

	related := strings.Split(*flagRestrict, ",")
	for _, x := range related {
		x = strings.ToLower(strings.TrimSpace(x))
		if x == "" {
			continue
		}
		if len(knownTypes[x]) > 0 {
			// Translating "-restrict something" into a list of extensions.
			for _, x := range knownTypes[x] {
				restrictToExtensions["."+x] = true
			}
			continue
		}
		if !strings.HasPrefix(x, ".") {
			x = "." + x
		}
		restrictToExtensions[x] = true
	}
}

// fileHash returns the SHA1 hash of the file.
func fileHash(file string) (string, error) {
	fh, err := os.Open(file)
	if err != nil {
		return "", err
	}
	defer fh.Close()

	h := sha1.New()
	chunk := make([]byte, 8192)

	for {
		n, err := fh.Read(chunk)
		if err == io.EOF {
			return fmt.Sprintf("%x", h.Sum(nil)), nil
		}
		if err != nil {
			return "", err
		}
		h.Write(chunk[:n])
	}

	panic("reached")
}

// getExifData attempts to retrieve EXIF data from a file.
func getExifData(file string) (map[string]string, error) {
	if !*flagExifTool || *flagTryExifTool {
		fp, err := os.Open(file)
		if err != nil {
			return nil, err
		}
		defer fp.Close()

		ex, err := exif.Decode(fp)
		if err == nil {
			tags := tagsWalker{make(map[string]string)}
			if err = ex.Walk(&tags); err != nil {
				return nil, err
			}
			return tags.v, nil
		}
	}

	if *flagExifTool || *flagTryExifTool {
		var out bytes.Buffer
		cmd := exec.Command("exiftool", file)

		cmd.Stdout = &out
		if err := cmd.Run(); err != nil {
			return nil, err
		}

		tags := make(map[string]string)

		data := strings.Trim(out.String(), " \r\n")
		lines := strings.Split(data, "\n")

		for _, line := range lines {
			k, v := strings.Replace(strings.TrimSpace(line[0:32]), " ", "", -1), strings.TrimSpace(line[33:])
			k = normalizeEXIFTag(k)
			tags[k] = v
		}

		return tags, nil
	}

	return nil, errUnknownFile
}

// verifyDirectory returns nil if the path is a directory.
func verifyDirectory(dpath string) error {
	var err error
	var stat os.FileInfo

	if stat, err = os.Stat(dpath); err != nil {
		return err
	}

	if !stat.IsDir() {
		return fmt.Errorf(errNotADirectory.Error(), dpath)
	}

	return nil
}

// copyFile atomically copies a file from src to dst.
func copyFile(src string, dst string) error {
	input, err := os.Open(src)
	if err != nil {
		return err
	}
	defer input.Close()

	output, err := ioutil.TempFile(path.Dir(dst), ".tmp")
	if err != nil {
		return err
	}

	_, err = io.Copy(output, input)
	if err != nil {
		output.Close()
		return err
	}

	// Need to close "output" before renaming.
	if err = output.Close(); err != nil {
		return err
	}

	return os.Rename(output.Name(), dst)
}

// moveFile moves a file from src to dst.
func moveFile(src string, dst string) error {
	var err error

	// Attempt to rename the file.
	if err = os.Rename(src, dst); err != nil {
		// If the file could not be renamed copy it atomically and remove the
		// origin.
		if err = copyFile(src, dst); err != nil {
			return err
		}

		if err = os.Remove(src); err != nil {
			return err
		}
	}

	return nil
}

// pick returns the first non-empty value.
func pick(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}

// getExifCreateDate attempts to get the given file's original creation date
// from its EXIF tags.
func getExifCreateDate(tags map[string]string) (time.Time, error) {
	// Looking for the first tag that sounds like a date.
	dateTimeFields := []string{
		"DateAndTimeOriginal",
		"DateTimeOriginal",
		"Date/TimeOriginal",
		"CreateDate",
		"MediaCreateDate",
		"TrackCreateDate",
		"FileModificationDateTime",
		"FileAccessDateTime",
	}

	toInt := func(s string) (i int) {
		i, _ = strconv.Atoi(s)
		return
	}

	for _, field := range dateTimeFields {
		taken, ok := tags[field]
		if !ok {
			continue
		}

		all := reDateTime.FindAllStringSubmatch(taken, -1)

		if len(all) < 1 || len(all[0]) < 6 {
			return time.Time{}, errMissingCreateTime
		}

		y := toInt(all[0][1])
		if y == 0 {
			continue
		}

		t := time.Date(
			y,
			time.Month(toInt(all[0][2])),
			toInt(all[0][3]),
			toInt(all[0][4]),
			toInt(all[0][5]),
			toInt(all[0][6]),
			0,
			time.Local,
		)

		if t.IsZero() {
			continue
		}

		return t, nil
	}

	return time.Time{}, errMissingCreateTime
}

// guessFileDestination attempts to guess the best destination name for the
// source file.
func guessFileDestination(srcFile string, dstDir string) (string, error) {
	tags, err := getExifData(srcFile)
	if err != nil {
		// Unknown file.
		if !*flagAcceptAll {
			return "", err
		}
	}

	fileExtension := strings.TrimLeft(strings.ToLower(path.Ext(srcFile)), ".")

	dstParts := []string{dstDir}

	if _, ok := tags["Track"]; ok { // Must be a music file.
		dstParts = append(dstParts,
			[]string{
				normalizeFilename(pick(tags["Artist"], "Unknown Artist")),
				normalizeFilename(pick(tags["Album"], "Unknown Album")),
			}...,
		)

		trackN, err := strconv.Atoi(tags["Track"])
		if err == nil {
			dstParts = append(dstParts, fmt.Sprintf(numberedFile, trackN, pick(tags["Title"], "Unknown Title"), fileExtension))
		} else {
			dstParts = append(dstParts, fmt.Sprintf(namedFile, pick(tags["Title"], "Unknown Title"), fileExtension))
		}

		return strings.Join(dstParts, pathSeparator), nil
	}

	guessedVendor := pick(
		tags["Manufacturer"],
		tags["Make"],
		tags["VendorID"],
		tags["HandlerVendorID"],
		tags["CompressorName"],
		tags["HandlerType"],
	)

	if guessedVendor != "" {
		guessedDevice := pick(
			tags["CameraID"],
			tags["CameraModelName"],
			tags["Model"],
			tags["CompressorName"],
			tags["SoftwareVersion"],
			tags["HandlerDescription"],
		)

		switch guessedVendor {
		case ".GoPro AVC encoder":
			guessedVendor = "GoPro"
			guessedDevice = "HERO"
		}

		timeTaken, err := getExifCreateDate(tags)
		if err == nil {
			dstParts = append(dstParts,
				[]string{
					normalizeFilename(guessedVendor),
					normalizeFilename(pick(guessedDevice, "Other")),
					strconv.Itoa(timeTaken.Year()),
					fmt.Sprintf(numberedMonth, timeTaken.Month(), timeTaken.Month()),
					fmt.Sprintf(numberedDay, timeTaken.Day(), timeTaken.Weekday()),
					fmt.Sprintf(datedFile, timeTaken.Hour(), timeTaken.Minute(), fileExtension),
				}...,
			)
			return strings.Join(dstParts, pathSeparator), nil
		}
	}

	// Any other file.
	baseFile := path.Base(srcFile)
	baseFile = baseFile[:len(baseFile)-len(fileExtension)-1]

	dstParts = append(dstParts,
		[]string{
			"Other",
			normalizeFilename(pick(strings.ToUpper(path.Ext(srcFile)), "Unknown")),
			fmt.Sprintf(namedFile, baseFile, fileExtension),
		}...,
	)

	return strings.Join(dstParts, pathSeparator), nil
}

// processFile moves a file from srcFile to dstDir using an special file name.
func processFile(srcFile string, dstDir string, next chan bool) error {
	debuglog.Printf("Processing file %q", srcFile)

	x := strings.ToLower(path.Ext(srcFile))
	if len(restrictToExtensions) > 0 {
		if !restrictToExtensions[x] {
			next <- true

			debuglog.Printf("Skipping file %q", srcFile)
			stats.Count(statSkippedFiles, 1)
			return nil
		}
	}

	dstFile, err := guessFileDestination(srcFile, dstDir)
	if err != nil || dstFile == "" {
		next <- true

		stats.Count(statUnknownFiles, 1)
		return fmt.Errorf("Unknown file type %q: %q", srcFile, err)
	}

	dstFileDir := path.Dir(dstFile)
	dstFileExt := path.Ext(dstFile)

	dstFileBase := path.Base(dstFile)
	dstFileBase = dstFileBase[:len(dstFileBase)-len(dstFileExt)]

	// Find a file suffix, if neccessary.
	for i := 0; true; i++ {
		dstFile = dstFileDir + pathSeparator + fmt.Sprintf("%s-%03d", dstFileBase, i) + dstFileExt

		_, err := os.Stat(dstFile)
		if err != nil {
			// Destination does not exist, attempt to acquire exclusive lock.
			if fileLocks.AcquireLock(dstFile) {
				// We acquired the lock!
				defer fileLocks.Unlock(dstFile)
				break
			}
			// Already locked by another goroutine, we will not be able to write to
			// this file, but we still need to wait for it to be unlocked in order to
			// tell if this file is going to be a duplicated file.
			<-fileLocks.WatchUnlock(dstFile)
		}

		srcHash, err := fileHash(srcFile)
		if err != nil {
			next <- true
			return err
		}

		dstHash, err := fileHash(dstFile)
		if err != nil {
			next <- true
			return err
		}

		if srcHash != dstHash {
			continue // Destination exists and it's not the same file, don't touch it.
		}

		// Process another file.
		next <- true

		stats.Count(statDuplicatedFiles, 1)

		if *flagDeleteOriginal {
			if *flagDryRun {
				log.Printf("Found duplicated files %q and %q, would remove source file.\n", dstFile, srcFile)
				return nil
			}

			log.Printf("Found duplicated files %q and %q, removing source file.\n", dstFile, srcFile)
			if err := os.Remove(srcFile); err != nil {
				return err
			}
			stats.Count(statDeletedFiles, 1)

			return nil
		}

		log.Printf("Found duplicated files %q and %q, skipping.\n", dstFile, srcFile)
		stats.Count(statSkippedFiles, 1)

		return nil
	}

	if err := verifyDirectory(dstFileDir); err != nil {
		if *flagDryRun {
			log.Printf("Would create directory %q", dstFileDir)
		} else {
			log.Printf("Creating directory %q", dstFileDir)
			if err = os.MkdirAll(dstFileDir, os.ModeDir|0750); err != nil {
				return err
			}
		}
	}

	// Process another file.
	next <- true

	if *flagDeleteOriginal {
		// Move the file.
		if *flagDryRun {
			log.Printf("Would move file: %q -> %q\n", srcFile, dstFile)
		} else {
			log.Printf("Moving file: %q -> %q\n", srcFile, dstFile)
			if err = moveFile(srcFile, dstFile); err != nil {
				return err
			}
			stats.Count(statMovedFiles, 1)
		}
	} else {
		// Just copy it.
		if *flagDryRun {
			log.Printf("Would copy file: %s -> %s\n", srcFile, dstFile)
		} else {
			log.Printf("Copying file: %s -> %s\n", srcFile, dstFile)
			if err = copyFile(srcFile, dstFile); err != nil {
				return err
			}
			stats.Count(statCopiedFiles, 1)
		}
	}

	return nil
}

// processDirectory looks for files in srcDir and copies them to dstDir using a
// sane file and directory layout.
func processDirectory(srcDir string, dstDir string) error {
	log.Printf("Entering directory %q", srcDir)
	defer log.Printf("Leaving directory %q", srcDir)

	dir, err := os.Open(srcDir)
	if err != nil {
		return err
	}
	defer dir.Close()

	files, err := dir.Readdir(-1)
	if err != nil {
		return err
	}

	var wg sync.WaitGroup

	for _, file := range files {
		srcFile := srcDir + pathSeparator + file.Name()

		baseName := path.Base(srcFile)
		if strings.HasPrefix(baseName, ".") && !*flagAllowHidden {
			continue
		}

		if file.IsDir() {
			if err := processDirectory(srcFile, dstDir); err != nil {
				stats.Count(statErroredTasks, 1)
				fmt.Fprintf(os.Stderr, "Could not open directory", srcFile)
			}
		} else {
			select {
			case token := <-tasks:
				wg.Add(1)

				next := make(chan bool)
				go func(next chan bool) {
					defer func() {
						wg.Done()
						tasks <- token
					}()
					err := processFile(srcFile, dstDir, next)
					if err != nil {
						stats.Count(statErroredTasks, 1)
						errlog.Printf("Could not move %q into %q: %s", srcFile, dstDir, err.Error())
					}
				}(next)
				<-next // ensures that files are processed in order

			}
		}
	}

	wg.Wait()

	return nil
}

func main() {

	flag.Parse()

	if *flagFrom == "" || *flagDest == "" {
		fmt.Println("Scanvenger, by J. Carlos Nieto.")
		fmt.Println("A command line tool for importing photos and media files into a sane file layout.\n")
		fmt.Println("Sample usage:\n\n\tscavenger -source /Volumes/external -destination ~/Photos -dry-run\n")
		flag.PrintDefaults()
		fmt.Println("")
	} else {
		timeStart := time.Now()

		restrictExtensions()

		tasks = make(chan token, *flagMaxProcs)
		for i := 0; i < *flagMaxProcs; i++ {
			tasks <- token{}
		}

		var err error

		// Verifying source directory.
		if err = verifyDirectory(*flagFrom); err != nil {
			log.Fatalf(err.Error())
		}

		// Verifying destination directory.
		if err = verifyDirectory(*flagDest); err != nil {
			log.Fatalf(err.Error())
		}

		processDirectory(*flagFrom, *flagDest)

		// Execution summary.
		log.Println("")
		log.Printf("Summary:\n")
		log.Printf("\tTime taken: %v\n", time.Duration(time.Duration(int(time.Since(timeStart)/time.Second))*time.Second))
		log.Printf("\tCopied files: %d\n", stats.Get(statCopiedFiles))
		log.Printf("\tMoved files: %d\n", stats.Get(statMovedFiles))
		log.Printf("\tDuplicated files: %d\n", stats.Get(statDuplicatedFiles))
		log.Printf("\tSkipped files: %d\n", stats.Get(statSkippedFiles))
		log.Printf("\tDeleted files: %d\n", stats.Get(statDeletedFiles))
		log.Printf("\tUnknown files (no EXIF data): %d\n", stats.Get(statUnknownFiles))
		log.Printf("\tErrors: %d\n", stats.Get(statErroredTasks))
	}
}
