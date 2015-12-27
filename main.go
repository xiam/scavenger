// Copyright (c) 2012-2014 José Carlos Nieto, https://menteslibres.net/xiam
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
	"github.com/xiam/exif"
	"io"
	"log"
	"math"
	"mime"
	"os"
	"os/exec"
	"path"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
)

var errlog = log.New(os.Stderr, "ERROR ", log.LstdFlags)

const (
	pathSeparator = string(os.PathSeparator)
	fileSeparator = "-"
)

var wg sync.WaitGroup

type task struct {
	srcFile string
	dstDir  string
	wg      *sync.WaitGroup
}

type token struct{}

var tasks chan token

var (
	errUnknownFile       = errors.New(`Could not identify file using EXIF data.`)
	errNotADirectory     = errors.New(`%s: is not a directory.`)
	errMissingCreateTime = errors.New(`Missing create time.`)
)

var (
	reSpecialA        = regexp.MustCompile(`[áäâãà]`)
	reSpecialE        = regexp.MustCompile(`[éëêẽè]`)
	reSpecialI        = regexp.MustCompile(`[íïîĩì]`)
	reSpecialO        = regexp.MustCompile(`[óöôõò]`)
	reSpecialU        = regexp.MustCompile(`[úüûũù]`)
	reSpecialN        = regexp.MustCompile(`[ñ]`)
	reSpecialNotAlpha = regexp.MustCompile(`[^a-z0-9]`)
	reSpecialSpaces   = regexp.MustCompile(` +`)
	reDateTime        = regexp.MustCompile(`(\d{4}):(\d{2}):(\d{2}) (\d{2}):(\d{2}):(\d{2})`)
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

var (
	flagFrom           = flag.String("source", "", "Scan for files on this directory (recursive).")
	flagDest           = flag.String("destination", "", "Move files into this directory.")
	flagDeleteOriginal = flag.Bool("delete-original", false, "Delete original file after copying it.")
	flagDryRun         = flag.Bool("dry-run", false, "Prints what would be done without actually doing it.")
	flagMaxProcs       = flag.Int("max-procs", runtime.NumCPU()*2, "The maximum number of tasks running at the same time.")
	flagExifTool       = flag.Bool("exiftool", false, "Use exiftool instead of libexif (slower. requires exiftool http://owl.phy.queensu.ca/~phil/exiftool/).")
	flagTryExifTool    = flag.Bool("try-exiftool", false, "Fallback to exiftool if libexif fails (requires exiftool http://owl.phy.queensu.ca/~phil/exiftool/).")
)

// fileHash returns the SHA1 hash of the file.
func fileHash(file string) (string, error) {
	const fileChunk = 8192

	fh, err := os.Open(file)
	if err != nil {
		return "", err
	}

	defer fh.Close()

	stat, err := fh.Stat()
	if err != nil {
		return "", err
	}

	size := stat.Size()

	chunks := uint64(math.Ceil(float64(size) / float64(fileChunk)))

	h := sha1.New()

	for i := uint64(0); i < chunks; i++ {
		csize := int(math.Min(fileChunk, float64(size-int64(i*fileChunk))))
		buf := make([]byte, csize)

		fh.Read(buf)
		io.WriteString(h, string(buf))
	}

	return fmt.Sprintf("%x", h.Sum(nil)), nil
}

// getExifData attempts to retrieve EXIF data from a file.
func getExifData(file string) (map[string]string, error) {
	var err error

	if !*flagExifTool || *flagTryExifTool {
		ex := exif.New()
		err = ex.Open(file)
		if err == nil {
			return ex.Tags, nil
		}
	}

	if *flagExifTool || *flagTryExifTool {
		cmd := exec.Command("exiftool", file)

		var out bytes.Buffer
		cmd.Stdout = &out

		if err := cmd.Run(); err != nil {
			return nil, err
		}

		tags := make(map[string]string)

		data := strings.Trim(out.String(), " \r\n")
		lines := strings.Split(data, "\n")

		var k, v string
		for _, line := range lines {
			k = strings.TrimSpace(line[0:32])
			v = strings.TrimSpace(line[33:])
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

// copyFile copies a file from src to dst.
func copyFile(src string, dst string) error {
	var input *os.File
	var output *os.File
	var err error

	if input, err = os.Open(src); err != nil {
		return err
	}
	defer input.Close()

	if output, err = os.Create(dst); err != nil {
		return err
	}
	defer output.Close()

	_, err = io.Copy(output, input)

	return err
}

// moveFile moves a file from src to dst.
func moveFile(src string, dst string) error {
	var err error

	// Attempt to rename the file.
	if err = os.Rename(src, dst); err != nil {
		// If the file could not be renamed copy it and remove it.
		if err = copyFile(src, dst); err != nil {
			return err
		}

		if err = os.Remove(src); err != nil {
			return err
		}
	}

	return nil
}

// Returns a normalized version of the input slice of strings.
func normalize(inputs ...string) string {
	sc := unicode.SpecialCase{}
	name := make([]string, 0, len(inputs))

	for _, input := range inputs {
		input = strings.TrimSpace(input)
		if input != "" {

			output := strings.ToLowerSpecial(sc, input)

			output = reSpecialA.ReplaceAllLiteralString(output, "a")

			output = reSpecialE.ReplaceAllLiteralString(output, "e")

			output = reSpecialI.ReplaceAllLiteralString(output, "i")

			output = reSpecialO.ReplaceAllLiteralString(output, "o")

			output = reSpecialU.ReplaceAllLiteralString(output, "u")

			output = reSpecialN.ReplaceAllLiteralString(output, "n")

			output = reSpecialNotAlpha.ReplaceAllLiteralString(output, " ")

			output = reSpecialSpaces.ReplaceAllLiteralString(output, " ")

			output = strings.Replace(strings.TrimSpace(output), " ", fileSeparator, -1)

			name = append(name, output)
		}
	}

	return strings.Join(name, fileSeparator)
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

// getExifCreateDate attempts to get the files original creation date.
func getExifCreateDate(tags map[string]string) (time.Time, error) {
	var taken string
	var ok bool

	// Looking for the first tag that sounds like a date.
	dateTimeFields := []string{
		"Date and Time (Original)",
		"Date/Time Original",
		"Create Date",
	}

	for _, field := range dateTimeFields {
		if taken, ok = tags[field]; ok {
			break
		}
	}

	if taken == "" {
		return time.Time{}, errMissingCreateTime
	}

	all := reDateTime.FindAllStringSubmatch(taken, -1)

	toInt := func(s string) (i int) {
		i, _ = strconv.Atoi(s)
		return
	}

	t := time.Date(
		toInt(all[0][1]),
		time.Month(toInt(all[0][2])),
		toInt(all[0][3]),
		toInt(all[0][4]),
		toInt(all[0][5]),
		toInt(all[0][6]),
		0,
		time.Local,
	)

	return t, nil
}

func guessFileDestination(srcFile string, dstDir string) (dstFile string, err error) {
	var tags map[string]string

	if tags, err = getExifData(srcFile); err != nil {
		return "", errUnknownFile
	}

	var fileType string
	var mimeType string

	fileExtension := strings.ToLower(path.Ext(srcFile))

	if tags["MIME Type"] != "" {
		mimeType = tags["MIME Type"]
	} else {
		mimeType = mime.TypeByExtension(fileExtension)
	}

	mimeTypeParts := strings.Split(mimeType, "/")

	if len(mimeTypeParts) > 0 {
		fileType = strings.ToUpper(mimeTypeParts[0])
	}

	hash, err := fileHash(srcFile)
	if err != nil {
		return "", err
	}

	// Guessing file contents.

	if _, ok := tags["Track"]; ok {
		// Music file.

		dstFile = strings.Join(
			[]string{
				dstDir,
				normalize(pick(tags["Artist"], "Unknown Artist")),
				normalize(pick(tags["Album"], "Unknown Album")),
				fmt.Sprintf("%s.%s%s", tags["Track"], normalize(fmt.Sprintf("%s-%s", pick(tags["Title"], "Unknown Title"), hash[0:8])), fileExtension),
			},
			pathSeparator,
		)
		return
	}

	cameraModel := pick(tags["Camera Model Name"], tags["Model"])

	if cameraModel != "" {
		// Digital photo.

		cameraManufacturer := pick(tags["Manufacturer"], tags["Make"])

		var timeTaken time.Time

		if timeTaken, err = getExifCreateDate(tags); err != nil {
			return
		}

		dstFile = strings.Join(
			[]string{
				dstDir,
				strings.ToUpper(normalize(cameraManufacturer)),
				strings.ToUpper(normalize(cameraModel)),
				fileType,
				strconv.Itoa(timeTaken.Year()),
				fmt.Sprintf("%02d.%s", timeTaken.Month(), timeTaken.Month()),
				fmt.Sprintf("%02d.%s", timeTaken.Day(), timeTaken.Weekday()),
				fmt.Sprintf("%02d%02d%02d-%s%s", timeTaken.Hour(), timeTaken.Minute(), timeTaken.Second(), strings.ToUpper(hash[0:8]), fileExtension),
			},
			pathSeparator,
		)
		return
	}

	if _, ok := tags["Vendor ID"]; ok {
		// Other file with EXIF data.

		var timeTaken time.Time

		if timeTaken, err = getExifCreateDate(tags); err != nil {
			return
		}

		dstFile = strings.Join(
			[]string{
				dstDir,
				strings.ToUpper(normalize(tags["Vendor ID"])),
				fileType,
				strconv.Itoa(timeTaken.Year()),
				fmt.Sprintf("%02d-%s", timeTaken.Month(), timeTaken.Month()),
				fmt.Sprintf("%02d-%s", timeTaken.Day(), timeTaken.Weekday()),
				fmt.Sprintf("%02d%02d%02d-%s%s", timeTaken.Hour(), timeTaken.Minute(), timeTaken.Second(), strings.ToUpper(hash[0:8]), fileExtension),
			},
			pathSeparator,
		)
		return
	}

	return "", errUnknownFile

	// Unknown file.

	/*
		if _, ok := tags["File Type"]; ok {
			// Is a regular file.
			dstFile = strings.Join(
				[]string{
					dstDir,
					strings.ToUpper(normalize(pick(path.Ext(srcFile)))),
					fmt.Sprintf("%s-%s", strings.ToUpper(hash[0:8]), path.Base(srcFile)),
				},
				pathSeparator,
			)
			goto OK
		}
	*/

	/*
		dstFile = strings.Join(
			[]string{
				dstDir,
				strings.ToUpper(path.Ext(srcFile)),
				fmt.Sprintf("%s%s", strings.ToUpper(hash[0:8]), strings.ToLower(path.Ext(srcFile))),
			},
			pathSeparator,
		)
	*/
}

// processFile moves a file from srcFile to dstDir using an special file srcFile.
func processFile(srcFile string, dstDir string) error {
	dstFile, err := guessFileDestination(srcFile, dstDir)

	if err != nil || dstFile == "" {
		stats.Count(statUnknownFiles, 1)
		return fmt.Errorf("Unknown file type %q: %q", srcFile, err)
	}

	_, err = os.Stat(dstFile)

	if err == nil {
		// Destination already exists.
		var srcHash, dstHash string

		if srcHash, err = fileHash(srcFile); err != nil {
			return err
		}

		if dstHash, err = fileHash(dstFile); err != nil {
			return err
		}

		if srcHash == dstHash {
			// The hash of the destination file matches the original file's hash.
			stats.Count(statDuplicatedFiles, 1)
			if *flagDryRun {
				log.Printf("Found duplicated files %q and %q, would remove source file.\n", dstFile, srcFile)
			} else {
				log.Printf("Found duplicated files %q and %q, removing source file.\n", dstFile, srcFile)
				os.Remove(srcFile)
				stats.Count(statDeletedFiles, 1)
			}
		} else {
			// Destination file is different from source, don't know what to do, it
			// would be safer to skip it.
			log.Printf("Destination %q for source file %q already exists and it's not a duplicate.\n", dstFile, srcFile)
			stats.Count(statSkippedFiles, 1)

			return fmt.Errorf("Destination already exists.", dstFile, srcFile)
		}
	} else {

		dstDir := path.Dir(dstFile)
		if *flagDryRun {
			log.Printf("Would create directory %q", dstDir)
		} else {
			log.Printf("Creating directory %q", dstDir)
			if err = os.MkdirAll(dstDir, os.ModeDir|0750); err != nil {
				return err
			}
		}

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
	}

	return nil

}

// processDirectory looks for files in srcDir and copies them to dstDir using a
// sane file and directory layout.
func processDirectory(srcDir string, dstDir string) error {
	log.Printf("Entering directory %q", srcDir)

	var err error
	var files []os.FileInfo

	var dir *os.File
	if dir, err = os.Open(srcDir); err != nil {
		return err
	}
	defer dir.Close()

	if files, err = dir.Readdir(-1); err != nil {
		return err
	}

	for _, file := range files {
		srcFile := srcDir + pathSeparator + file.Name()
		if file.IsDir() {
			if err := processDirectory(srcFile, dstDir); err != nil {
				stats.Count(statErroredTasks, 1)
				fmt.Fprintf(os.Stderr, "Could not open directory", srcFile)
			}
		} else {
			select {
			case token := <-tasks:
				err := processFile(srcFile, dstDir)
				if err != nil {
					stats.Count(statErroredTasks, 1)
					errlog.Printf("Could not move %q into %q: %s", srcFile, dstDir, err.Error())
				}
				tasks <- token
			}
		}
	}

	return nil
}

func main() {

	flag.Parse()

	if *flagFrom == "" || *flagDest == "" {
		fmt.Println("Photopy, by J. Carlos Nieto.")
		fmt.Println("A command line tool for importing photos and media files into a sane file layout.\n")
		fmt.Println("Sample usage:\n\n\tphotopy -source /Volumes/external -destination ~/Photos -dry-run\n")
		flag.PrintDefaults()
		fmt.Println("")
	} else {
		var err error

		tasks = make(chan token, *flagMaxProcs)

		for i := 0; i < *flagMaxProcs; i++ {
			tasks <- token{}
		}

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
		log.Printf("\tCopied files: %d\n", stats.Get(statCopiedFiles))
		log.Printf("\tMoved files: %d\n", stats.Get(statMovedFiles))
		log.Printf("\tSkipped files: %d\n", stats.Get(statSkippedFiles))
		log.Printf("\tDeleted files: %d\n", stats.Get(statDeletedFiles))
		log.Printf("\tUnknown files (no EXIF data): %d\n", stats.Get(statUnknownFiles))
		log.Printf("\tErrors: %d\n", stats.Get(statErroredTasks))
	}
}
